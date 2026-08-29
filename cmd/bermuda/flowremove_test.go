package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/flow"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// Removing a flow a job still points at.
//
// A job whose flow is gone does not fail when the flow is deleted. It fails at
// its next fire, at 04:00, with nothing said in between — the store deliberately
// does not validate the flow a job names, because the file is edited by people
// who never go near that code. `flow rm` is the last moment anybody is looking,
// so it is the only place that check can be useful.

func TestRemovingAFlowAJobUsesIsRefusedAndNamesTheJob(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	writeFlow(t, "nightly", "steps:\n  - id: one\n    run: true\n")
	if err := s.PutJob(ctx, store.Job{ID: "nightly-sweep", Name: "Sweep", Flow: "nightly"}); err != nil {
		t.Fatal(err)
	}

	err := flowRemove([]string{"nightly"})
	if err == nil {
		t.Fatal("removing a flow a job starts was allowed; the job now fails silently at its next fire")
	}
	if !strings.Contains(err.Error(), "nightly-sweep") {
		t.Errorf("the refusal %q does not name the job that has to change first", err)
	}
	if _, err := flow.Load(flowDir(), "nightly"); err != nil {
		t.Errorf("the flow was deleted despite the refusal: %v", err)
	}
}

// Every job that points at it, not the first one found. Fixing one and hitting
// the same refusal again is how a person concludes the check is broken.
func TestTheRefusalNamesEveryJobUsingTheFlow(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	writeFlow(t, "nightly", "steps:\n  - id: one\n    run: true\n")
	for _, id := range []string{"sweep", "report"} {
		if err := s.PutJob(ctx, store.Job{ID: id, Name: id, Flow: "nightly"}); err != nil {
			t.Fatal(err)
		}
	}

	err := flowRemove([]string{"nightly"})
	if err == nil {
		t.Fatal("removing a flow two jobs start was allowed")
	}
	for _, id := range []string{"sweep", "report"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("the refusal %q leaves out job %s", err, id)
		}
	}
}

// A flow nothing points at is removable, and the check must not become a reason
// nobody can delete anything.
func TestAnUnusedFlowIsRemoved(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	writeFlow(t, "nightly", "steps:\n  - id: one\n    run: true\n")
	writeFlow(t, "other", "steps:\n  - id: one\n    run: true\n")
	// A job on a different flow must not hold this one hostage.
	if err := s.PutJob(ctx, store.Job{ID: "sweep", Name: "Sweep", Flow: "other"}); err != nil {
		t.Fatal(err)
	}

	if err := flowRemove([]string{"nightly"}); err != nil {
		t.Fatalf("an unused flow could not be removed: %v", err)
	}
	if _, err := flow.Load(flowDir(), "nightly"); err == nil {
		t.Error("flow rm reported success but the file is still there")
	}
	if _, err := flow.Load(flowDir(), "other"); err != nil {
		t.Errorf("removing one flow took another with it: %v", err)
	}
}

// jobsUsingFlow answers the question the guard is built on, so its matching has
// to survive how the id was actually typed. A job stored as "Nightly " and a
// `flow rm nightly` that skips the check is the silent failure this exists to
// stop.
func TestJobsUsingFlowMatchesRegardlessOfCaseAndPadding(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	if err := s.PutJob(ctx, store.Job{ID: "sweep", Name: "Sweep", Flow: " Nightly "}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutJob(ctx, store.Job{ID: "unrelated", Name: "Other", Flow: "nightly-report"}); err != nil {
		t.Fatal(err)
	}
	// A prompt job names no flow at all and must never match.
	if err := s.PutJob(ctx, store.Job{ID: "plain", Name: "Plain", Prompt: "do a thing"}); err != nil {
		t.Fatal(err)
	}

	users, err := jobsUsingFlow("nightly")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0] != "sweep" {
		t.Fatalf("jobsUsingFlow(nightly) = %v, want [sweep]: a near-miss id must not match and a padded one must", users)
	}

	none, err := jobsUsingFlow("")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("an empty flow id matched %v, which would make every prompt job look like a flow user", none)
	}
}
