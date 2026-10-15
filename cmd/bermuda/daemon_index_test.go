package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/index"
	"github.com/bon5co/bermuda/v3/internal/runner"
	"github.com/bon5co/bermuda/v3/internal/store"
)

func indexTestDaemon(t *testing.T) *daemon {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BERMUDA_STATE_DIR", dir)
	t.Setenv("HERDR_BIN_PATH", filepath.Join(t.TempDir(), "no-herdr"))
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &daemon{store: s, tick: time.Hour, reconcileEvery: time.Hour,
		indexEvery: 2 * time.Millisecond, slots: make(chan struct{}, 1)}
}

func waitIndexSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func runIndexDaemon(t *testing.T, d *daemon) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.run(ctx) }()
	t.Cleanup(func() { cancel(); waitIndexSignal(t, done, "daemon shutdown") })
	return cancel, done
}

func TestDaemonBlockedIndexDoesNotBlockJobsOrReconciliation(t *testing.T) {
	d := indexTestDaemon(t)
	entered, cancelled, finishIndex := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.indexVault = func(ctx context.Context) (index.Report, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-finishIndex // shutdown must wait for worker cleanup
		return index.Report{}, ctx.Err()
	}
	reconciled := make(chan struct{}, 1)
	d.reconcileEvery = 5 * time.Millisecond
	d.reconcile = func(context.Context, *store.Store) (int, error) {
		select {
		case reconciled <- struct{}{}:
		default:
		}
		return 0, nil
	}
	jobStarted, finishJob := make(chan struct{}), make(chan struct{})
	var jobCancelled atomic.Bool
	d.exec = func(ctx context.Context, _ *store.Store, _ store.Job, _ string) (*runner.Run, error) {
		close(jobStarted)
		<-finishJob
		jobCancelled.Store(ctx.Err() != nil)
		return &runner.Run{RunID: "test", Outcome: runner.OutcomeDone}, nil
	}
	// Only one fire is owed, and inflight prevents any second launch while
	// the test deliberately holds the job through daemon cancellation.
	d.tick = 10 * time.Millisecond
	cancel, done := runIndexDaemon(t, d)
	defer close(finishIndex)
	defer close(finishJob)
	waitIndexSignal(t, entered, "index start")
	job := store.Job{ID: "due", Name: "due", Prompt: "test", Enabled: true,
		Schedule: store.ScheduleInterval, IntervalSeconds: 3600, Catchup: store.CatchupLatest,
		CreatedAt: time.Now().Add(-3 * time.Hour)}
	if err := d.store.PutJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if err := d.store.PutRun(context.Background(), store.Run{ID: "prior", JobID: job.ID,
		Trigger: "scheduled", Outcome: store.OutcomeDone, StartedAt: time.Now().Add(-90 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	waitIndexSignal(t, jobStarted, "scheduled job during blocked index")
	waitIndexSignal(t, reconciled, "reconciliation during blocked index")
	cancel()
	waitIndexSignal(t, cancelled, "index cancellation")
	select {
	case <-done:
		t.Fatal("shutdown returned before index/job cleanup")
	default:
	}
	// Deferred releases occur before runIndexDaemon's cleanup waits for exit.
	t.Cleanup(func() {
		waitIndexSignal(t, done, "job and index cleanup")
		if jobCancelled.Load() {
			t.Error("daemon cancellation reached an already-running job")
		}
	})
}

func TestDaemonIndexCoalescesTicksAndCannotFollowUpAfterCancel(t *testing.T) {
	d := indexTestDaemon(t)
	first, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls, active, maximum atomic.Int32
	d.indexVault = func(ctx context.Context) (index.Report, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); old < n && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		switch calls.Add(1) {
		case 1:
			close(first)
			select {
			case <-release:
			case <-ctx.Done():
			}
		case 2:
			close(second)
			<-ctx.Done()
		default:
			t.Error("unexpected follow-up after cancellation")
		}
		return index.Report{}, nil
	}
	cancel, done := runIndexDaemon(t, d)
	waitIndexSignal(t, first, "first index sweep")
	// Many ticks while the first worker is held must become one pending bit.
	time.Sleep(40 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("index workers overlap while blocked")
	}
	close(release)
	waitIndexSignal(t, second, "coalesced sweep")
	time.Sleep(20 * time.Millisecond)
	cancel()
	waitIndexSignal(t, done, "coalesced worker cancellation")
	if calls.Load() != 2 || maximum.Load() != 1 {
		t.Fatalf("calls=%d max concurrent=%d, want 2 and 1", calls.Load(), maximum.Load())
	}
}

func TestDaemonIndexPendingSweepDiscardedWhenCancelled(t *testing.T) {
	d := indexTestDaemon(t)
	entered := make(chan struct{})
	var calls atomic.Int32
	d.indexVault = func(ctx context.Context) (index.Report, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-ctx.Done()
		return index.Report{}, ctx.Err()
	}
	cancel, done := runIndexDaemon(t, d)
	waitIndexSignal(t, entered, "index start")
	time.Sleep(30 * time.Millisecond)
	cancel()
	waitIndexSignal(t, done, "index shutdown")
	if calls.Load() != 1 {
		t.Fatalf("started %d sweeps despite cancellation", calls.Load())
	}
}

func TestDaemonIndexMissingPythonLatchesButTransientRetries(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "transient"}[missing], func(t *testing.T) {
			d := indexTestDaemon(t)
			var calls atomic.Int32
			entered, retry := make(chan struct{}), make(chan struct{})
			d.indexVault = func(context.Context) (index.Report, error) {
				n := calls.Add(1)
				if n == 1 {
					close(entered)
				}
				if n == 2 {
					close(retry)
				}
				if missing {
					return index.Report{}, index.ErrNoPython
				}
				return index.Report{}, errors.New("temporary failure")
			}
			cancel, done := runIndexDaemon(t, d)
			waitIndexSignal(t, entered, "first attempt")
			if missing {
				time.Sleep(30 * time.Millisecond)
			} else {
				waitIndexSignal(t, retry, "transient retry")
			}
			cancel()
			waitIndexSignal(t, done, "daemon exit")
			if missing && (calls.Load() != 1 || !d.indexOff) {
				t.Fatalf("calls=%d indexOff=%v", calls.Load(), d.indexOff)
			}
			if !missing && (calls.Load() < 2 || d.indexOff) {
				t.Fatalf("calls=%d indexOff=%v", calls.Load(), d.indexOff)
			}
		})
	}
}
