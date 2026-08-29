package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/runner"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// A sweep runs what the catchup policy says it owes.
//
// catchup=all launched exactly one run per sweep, whatever the backlog: the
// loop claimed the job per fire and stopped at the first refusal, and the next
// sweep measured from the run it had just started, which put the rest of the
// backlog behind the anchor where nothing would ever look again. So all,
// latest and skip were one behaviour with three names.
func TestSweepRunsTheWholeBacklog(t *testing.T) {
	for _, tc := range []struct {
		catchup string
		want    int
	}{
		{store.CatchupAll, 6},
		{store.CatchupLatest, 1},
		{store.CatchupSkip, 0}, // the missed window is dropped entirely
	} {
		t.Run(tc.catchup, func(t *testing.T) {
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
				Catchup: tc.catchup, CreatedAt: time.Now().Add(-24 * time.Hour),
			}
			if err := s.PutJob(ctx, job); err != nil {
				t.Fatalf("put job: %v", err)
			}
			// Six and a half hours of downtime: six fires owed, the newest of
			// them half an hour stale, so it was plainly missed rather than
			// current.
			started := time.Now().Add(-390 * time.Minute)
			if err := s.PutRun(ctx, store.Run{
				ID: "r0", JobID: job.ID, Trigger: "scheduled",
				Outcome: store.OutcomeDone, StartedAt: started,
			}); err != nil {
				t.Fatalf("put run: %v", err)
			}

			var mu sync.Mutex
			var runs int
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
			defer mu.Unlock()
			if runs != tc.want {
				t.Errorf("catchup=%s launched %d runs, want %d", tc.catchup, runs, tc.want)
			}
		})
	}
}

// A job already running is not launched again, however much it owes. Overlap
// is the one thing the claim exists to prevent.
func TestSweepDoesNotOverlapAJobWithItself(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	job := store.Job{
		ID: "busy", Name: "busy", Prompt: "p", Enabled: true,
		Schedule: store.ScheduleInterval, IntervalSeconds: 1,
		Catchup: store.CatchupAll, CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := s.PutJob(ctx, job); err != nil {
		t.Fatalf("put job: %v", err)
	}
	// A job that has never run measures from its creation time and drops every
	// fire older than the first-run grace, so it needs a run to anchor on
	// before it owes anything at all.
	if err := s.PutRun(ctx, store.Run{
		ID: "r0", JobID: job.ID, Trigger: "scheduled",
		Outcome: store.OutcomeDone, StartedAt: time.Now().Add(-10 * time.Second),
	}); err != nil {
		t.Fatalf("put run: %v", err)
	}

	release := make(chan struct{})
	var mu sync.Mutex
	var runs int
	d := &daemon{
		store: s, tick: time.Hour, slots: make(chan struct{}, 4),
		inflight: map[string]bool{},
		exec: func(context.Context, *store.Store, store.Job, string) (*runner.Run, error) {
			mu.Lock()
			runs++
			mu.Unlock()
			<-release // hold the job "running" across the second sweep
			return &runner.Run{RunID: "r", Outcome: runner.OutcomeDone}, nil
		},
	}

	d.sweep(ctx)
	// Wait for the first run to be in flight before sweeping again.
	for {
		mu.Lock()
		started := runs > 0
		mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	before := runsSoFar(&mu, &runs)
	d.sweep(ctx)
	if got := runsSoFar(&mu, &runs); got != before {
		t.Errorf("a second sweep launched %d more runs of a job already running", got-before)
	}
	close(release)
	d.wg.Wait()
}

func runsSoFar(mu *sync.Mutex, runs *int) int {
	mu.Lock()
	defer mu.Unlock()
	return *runs
}
