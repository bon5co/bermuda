package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/runner"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// A restarted daemon must not start a second run of a job that is still going.
//
// inflight is rebuilt empty on every start, and the stale-build eviction
// restarts the scheduler on purpose, so the in-memory claim alone forgets what
// it was running. For a --keep-context job that means two agents writing into
// one conversation.
func TestSweepSkipsAJobAlreadyRunningInTheStore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		started time.Duration // how long ago the running row started
		want    int
	}{
		// Both rows are old enough that the job is due again -- the interval is
		// an hour -- so the only thing that can stop the fire is the guard.
		{"a run still inside its timeout blocks the fire", 90 * time.Minute, 0},
		{"a row older than the timeout is not a run any more", 30 * time.Hour, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("BERMUDA_STATE_DIR", dir)
			s, err := store.Open(dir)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer s.Close()

			ctx := context.Background()
			job := store.Job{
				ID: "hourly", Name: "hourly", Prompt: "p", Enabled: true,
				Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
				Catchup: store.CatchupLatest, Timeout: 6 * time.Hour,
				CreatedAt: time.Now().Add(-24 * time.Hour),
			}
			if err := s.PutJob(ctx, job); err != nil {
				t.Fatalf("put job: %v", err)
			}
			if err := s.PutRun(ctx, store.Run{
				ID: "r0", JobID: job.ID, Trigger: "scheduled",
				Outcome:   store.StepRunning,
				StartedAt: time.Now().Add(-tc.started),
			}); err != nil {
				t.Fatalf("put run: %v", err)
			}

			var mu sync.Mutex
			var runs int
			// A fresh daemon, as after a restart: nothing in inflight.
			d := &daemon{
				store: s, tick: time.Hour, slots: make(chan struct{}, 4),
				inflight: map[string]bool{},
				exec: func(context.Context, *store.Store, store.Job, string) (*runner.Run, error) {
					mu.Lock()
					runs++
					mu.Unlock()
					return &runner.Run{RunID: "r", Outcome: runner.OutcomeDone}, nil
				},
			}
			d.sweep(ctx)
			d.wg.Wait()

			mu.Lock()
			got := runs
			mu.Unlock()
			if got != tc.want {
				t.Fatalf("launched %d runs, want %d", got, tc.want)
			}
		})
	}
}
