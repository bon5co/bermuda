package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

func TestJobAddEditAndClearReferenceThroughThePublicFlags(t *testing.T) {
	ctx := jobCmdEnv(t)
	if err := jobAdd([]string{"--id", "brief", "--prompt", "write", "--ref", "Ticket: Mixed / #42"}); err != nil {
		t.Fatal(err)
	}
	s := storeForEnv(t)
	j, err := s.Job(ctx, "brief")
	if err != nil {
		t.Fatal(err)
	}
	if j.Ref != "Ticket: Mixed / #42" {
		t.Fatalf("added ref = %q", j.Ref)
	}
	if err := jobEdit([]string{"brief", "--ref", "https://example.com/A?b=C"}); err != nil {
		t.Fatal(err)
	}
	j, err = s.Job(ctx, "brief")
	if err != nil {
		t.Fatal(err)
	}
	if j.Ref != "https://example.com/A?b=C" {
		t.Fatalf("edited ref = %q", j.Ref)
	}
	if err := jobEdit([]string{"brief", "--ref", ""}); err != nil {
		t.Fatal(err)
	}
	j, err = s.Job(ctx, "brief")
	if err != nil {
		t.Fatal(err)
	}
	if j.Ref != "" {
		t.Fatalf("cleared ref = %q", j.Ref)
	}
	if err := jobEdit([]string{"brief", "--ref", strings.Repeat("x", 513)}); err == nil {
		t.Fatal("accepted a reference longer than 512 bytes")
	}
}

func TestFlowRunRecordsItsReferenceAndExposesItToCommandSteps(t *testing.T) {
	s := flowStore(t)
	out := filepath.Join(t.TempDir(), "ref.txt")
	writeFlow(t, "ref-flow", "steps:\n  - id: write\n    run: printf '%s' \"$BERMUDA_REF\" > "+out+"\n")
	if err := flowRun([]string{"ref-flow", "--ref", "Ticket: Mixed / #42"}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Ref != "Ticket: Mixed / #42" {
		t.Fatalf("runs = %+v", runs)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != runs[0].Ref {
		t.Fatalf("step saw %q, want %q", b, runs[0].Ref)
	}
}

func TestAJobTriggeredFlowCopiesTheReferenceAndAResumedRunKeepsItAfterTheJobChanges(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	writeFlow(t, "gated", "steps:\n  - id: gate\n    run: test -f "+gate+"\n")
	j := store.Job{ID: "gated", Flow: "gated", Ref: "Ticket: Original", CWD: t.TempDir(), Enabled: true, Timeout: time.Minute}
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	run, err := Execute(ctx, s, j, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if string(run.Outcome) != "parked" {
		t.Fatalf("outcome = %s", run.Outcome)
	}
	j.Ref = "Ticket: New"
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, []byte("open"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := flowResume([]string{run.RunID}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Run(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != "done" || rec.Ref != "Ticket: Original" {
		t.Fatalf("resumed run = %+v", rec)
	}
	events, err := s.RunEvents(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Settlement != 1 || events[1].Settlement != 2 || events[1].PreviousOutcome != "parked" {
		t.Fatalf("flow settlement events = %+v", events)
	}
}

func TestFlowResumeCanReplaceAReferenceWithoutChangingTheEarlierSettlement(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	gate := filepath.Join(t.TempDir(), "gate")
	writeFlow(t, "gated-override", "steps:\n  - id: gate\n    run: test -f "+gate+"\n")
	j := store.Job{ID: "gated-override", Flow: "gated-override", CWD: t.TempDir()}
	rec := store.Run{ID: "run-override", JobID: j.ID, Flow: j.Flow, Outcome: "running", StartedAt: time.Now(), Ref: "Ticket: Original"}
	if _, err := runFlow(ctx, s, j, rec, flowOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, []byte("open"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := flowResume([]string{rec.ID, "--ref", "Ticket: Override"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != "Ticket: Override" || got.Outcome != "done" {
		t.Fatalf("resumed run = %+v", got)
	}
}

func TestFlowResumeUsageNamesTheReferenceOverride(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	err := flowResume(nil)
	if err == nil || !strings.Contains(err.Error(), "[--ref <value>]") {
		t.Fatalf("usage error = %v, want the reference override", err)
	}
}

func TestFlowRunUsageNamesTheReferenceFlag(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	err := flowRun(nil)
	if err == nil || !strings.Contains(err.Error(), "[--ref <value>]") {
		t.Fatalf("usage error = %v, want the reference flag", err)
	}
}
