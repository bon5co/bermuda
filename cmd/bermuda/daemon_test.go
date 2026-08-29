package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The daemon must revisit parked runs while it is up, not only as it starts.
//
// The incident: rt-template-daily's agent wrote result.json eight minutes after
// the supervisor gave up on it, so the run sat as "parked (no_result)" with a
// successful publish on disk underneath it. reconcileRuns already knew how to
// correct that, but it ran on daemon start and on the ensure hook and nowhere
// else, so the wrong verdict stood for a day and the self-heal shift spent an
// agent re-diagnosing a job that was fine.
func TestDaemonReconcilesParkedRunsWhileRunning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	d := &daemon{
		store: s,
		// Long enough that no schedule sweep fires during the test: the only
		// thing under test is the second ticker.
		tick:           time.Hour,
		slots:          make(chan struct{}, 1),
		reconcileEvery: 5 * time.Millisecond,
		reconcile: func(context.Context, *store.Store) (int, error) {
			mu.Lock()
			calls++
			if calls == 2 {
				close(done)
			}
			mu.Unlock()
			return 0, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go d.run(ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("daemon never reconciled parked runs on its own")
	}
	cancel()
}

// An unset interval must not mean "every tick": reconciling reads every parked
// run and stats its result files, which is not work for a five-second loop.
func TestDaemonReconcileIntervalDefaultsToMinutes(t *testing.T) {
	if defaultReconcileEvery < time.Minute {
		t.Fatalf("defaultReconcileEvery is %s, want at least a minute", defaultReconcileEvery)
	}
}
