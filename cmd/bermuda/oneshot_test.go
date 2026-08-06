package main

import (
	"context"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/runner"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// A one-shot job disables itself once it has actually run. Both halves of that
// sentence carry weight, and getting either wrong is silent: a job that never
// disables fires again on the next restart, and a job that disables on a run
// that never happened is a scheduled task that quietly stops existing.

// onceJob is a stored one-shot job due now.
func onceJob(t *testing.T, s *store.Store, id string) store.Job {
	t.Helper()
	at := time.Now().Add(-time.Minute)
	j := store.Job{
		ID: id, Name: id, Prompt: "p", Enabled: true,
		Schedule: store.ScheduleOnce, RunAt: &at,
		Catchup: store.CatchupLatest, CreatedAt: time.Now(),
	}
	if err := s.PutJob(context.Background(), j); err != nil {
		t.Fatalf("put job: %v", err)
	}
	return j
}

func enabled(t *testing.T, s *store.Store, id string) bool {
	t.Helper()
	j, err := s.Job(context.Background(), id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	return j.Enabled
}

func TestDisableOneShot(t *testing.T) {
	for _, tc := range []struct {
		name        string
		schedule    store.ScheduleType
		run         *runner.Run
		wantEnabled bool
	}{
		{
			// The case the rule exists for.
			name: "a one-shot that ran is disabled",
			run:  &runner.Run{Outcome: runner.OutcomeDone},
		},
		{
			// Failing still counts as having run: a one-shot that re-fires
			// until it succeeds is a retry loop nobody asked for.
			name: "a one-shot that failed is still disabled",
			run:  &runner.Run{Outcome: runner.OutcomeFailed},
		},
		{
			name: "a parked one-shot is disabled, because resuming is a separate act",
			run:  &runner.Run{Outcome: runner.OutcomeParked, ParkReason: runner.ParkNoResult},
		},
		{
			// Nothing was attempted, so the job keeps its turn.
			name:        "no run at all leaves the job alone",
			run:         nil,
			wantEnabled: true,
		},
		{
			// A run that never reached a verdict did not happen either.
			name:        "a run with no outcome leaves the job alone",
			run:         &runner.Run{},
			wantEnabled: true,
		},
		{
			// Every other schedule is meant to fire again.
			name:        "a repeating job is never disabled by a run",
			schedule:    store.ScheduleInterval,
			run:         &runner.Run{Outcome: runner.OutcomeDone},
			wantEnabled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			j := onceJob(t, s, "shot")
			if tc.schedule != "" {
				j.Schedule = tc.schedule
				j.IntervalSeconds = 3600
				if err := s.PutJob(context.Background(), j); err != nil {
					t.Fatalf("put job: %v", err)
				}
			}

			disableOneShot(context.Background(), s, j, tc.run)

			if got := enabled(t, s, j.ID); got != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

// Disabling is what stops the job, not deleting it: the run it produced is
// still readable from the board, and re-enabling it is one keystroke. A
// disabled one-shot that lost its schedule or its prompt could not be run
// again at all.
func TestDisableOneShotKeepsTheJobItself(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	j := onceJob(t, s, "shot")

	disableOneShot(ctx, s, j, &runner.Run{Outcome: runner.OutcomeDone})

	got, err := s.Job(ctx, j.ID)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if got.Enabled {
		t.Fatal("job is still enabled")
	}
	if got.Prompt != j.Prompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, j.Prompt)
	}
	if got.Schedule != store.ScheduleOnce {
		t.Errorf("schedule = %q, want %q", got.Schedule, store.ScheduleOnce)
	}
	if got.RunAt == nil {
		t.Error("run-at was cleared, so the job cannot be re-armed as it was")
	}
}

// Disabling twice is the same as disabling once. The daemon and the board can
// both arrive at the end of one run, and a second call must not error or
// resurrect anything.
func TestDisableOneShotIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	j := onceJob(t, s, "shot")
	run := &runner.Run{Outcome: runner.OutcomeDone}

	disableOneShot(ctx, s, j, run)
	disableOneShot(ctx, s, j, run)

	if enabled(t, s, j.ID) {
		t.Error("job is enabled after being disabled twice")
	}
}
