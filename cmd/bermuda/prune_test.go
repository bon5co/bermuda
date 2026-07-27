package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/internal/store"
)

// seedFinished fills a store with one of each kind of one-shot plus a
// recurring job whose last run is also done.
func seedFinished(t *testing.T, s *store.Store) context.Context {
	t.Helper()
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	jobs := []store.Job{
		{ID: "done-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "parked-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "failed-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "never-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "daily", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *"},
	}
	for _, j := range jobs {
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	runs := []store.Run{
		{ID: "r1", JobID: "done-once", Outcome: "done", StartedAt: at},
		{ID: "r2", JobID: "parked-once", Outcome: "parked", ParkReason: "needs a human", StartedAt: at},
		{ID: "r3", JobID: "failed-once", Outcome: "failed", StartedAt: at},
		{ID: "r4", JobID: "daily", Outcome: "done", StartedAt: at},
	}
	for _, r := range runs {
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

func TestListHidesOnlyTheFinishedOneShotAndSaysSo(t *testing.T) {
	s := newStore(t)
	ctx := seedFinished(t, s)

	var out bytes.Buffer
	if err := listJobs(ctx, s, &out, "", false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "done-once") {
		t.Error("the finished one-shot is still listed")
	}
	for _, id := range []string{"parked-once", "failed-once", "never-once", "daily"} {
		if !strings.Contains(got, id) {
			t.Errorf("%s should still be listed", id)
		}
	}
	if !strings.Contains(got, "1 finished hidden") {
		t.Errorf("the list does not say what it withheld:\n%s", got)
	}
}

func TestListAllShowsTheFinishedOneMarked(t *testing.T) {
	s := newStore(t)
	ctx := seedFinished(t, s)

	var out bytes.Buffer
	if err := listJobs(ctx, s, &out, "", true); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "done-once") {
		t.Fatal("--all did not show the finished one-shot")
	}
	if !strings.Contains(got, "(finished)") {
		t.Errorf("the finished one-shot is not marked:\n%s", got)
	}
	if strings.Contains(got, "finished hidden") {
		t.Error("--all still claims something is hidden")
	}
}

// The dry run is the default, and it must not touch the store.
func TestPruneWithoutYesDeletesNothing(t *testing.T) {
	s := newStore(t)
	ctx := seedFinished(t, s)

	var out bytes.Buffer
	if err := pruneJobs(ctx, s, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "done-once") {
		t.Errorf("the dry run does not say what it would remove:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "nothing removed") {
		t.Errorf("the dry run does not say it removed nothing:\n%s", out.String())
	}
	jobs, err := s.Jobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 5 {
		t.Fatalf("the dry run left %d jobs, want all 5", len(jobs))
	}
}

func TestPruneYesRemovesTheFinishedOneShotsAndKeepsTheirRuns(t *testing.T) {
	s := newStore(t)
	ctx := seedFinished(t, s)

	var out bytes.Buffer
	if err := pruneJobs(ctx, s, &out, true); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, j := range jobs {
		left = append(left, j.ID)
	}
	if got, want := strings.Join(left, ","), "parked-once,failed-once,never-once,daily"; !sameSet(got, want) {
		t.Errorf("after pruning the store holds %q, want %q", got, want)
	}
	if _, err := s.Job(ctx, "done-once"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the finished one-shot survived: %v", err)
	}
	// Deleting a job keeps its history, so pruning loses the schedule and not
	// the record of what happened.
	if _, err := s.Run(ctx, "r1"); err != nil {
		t.Errorf("the pruned job's run was lost too: %v", err)
	}
}

// A recurring job is never pruned, whatever its last run says.
func TestPruneNeverTouchesRecurringJobs(t *testing.T) {
	s := newStore(t)
	ctx := seedFinished(t, s)

	var out bytes.Buffer
	if err := pruneJobs(ctx, s, &out, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Job(ctx, "daily"); err != nil {
		t.Errorf("the recurring job was pruned: %v", err)
	}
}

func sameSet(a, b string) bool {
	as, bs := strings.Split(a, ","), strings.Split(b, ",")
	if len(as) != len(bs) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range as {
		seen[x] = true
	}
	for _, x := range bs {
		if !seen[x] {
			return false
		}
	}
	return true
}
