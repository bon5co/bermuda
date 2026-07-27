package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bon5co/bermuda/internal/runner"
	"github.com/bon5co/bermuda/internal/store"
)

// writeResult puts a result file where a finished run would have left one.
func writeResult(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The case that prompted this: the process supervising a run was killed, so the
// row stayed "running" even though the agent had finished and written its
// result.
func TestFinishedWorkIsAdoptedFromItsResultFile(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dir := writeResult(t, filepath.Join(t.TempDir(), "run"),
		`{"status":"ok","note":"merged as PR #10"}`)

	if err := s.PutRun(ctx, store.Run{
		ID: "r1", JobID: "job", Outcome: "running", RunDir: dir,
		AgentName: "gone", StartedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := reconcileRuns(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("resolved %d runs, want 1", n)
	}
	runs, _ := s.Runs(ctx, "", 10)
	if runs[0].Outcome != "done" {
		t.Errorf("outcome is %q, want done", runs[0].Outcome)
	}
	if runs[0].Note != "merged as PR #10" {
		t.Errorf("note is %q; the result file's note should survive", runs[0].Note)
	}
	if runs[0].EndedAt == nil {
		t.Error("an adopted run needs an end time")
	}
}

// A result that reports failure must not be laundered into success.
func TestFailedResultIsAdoptedAsFailed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dir := writeResult(t, filepath.Join(t.TempDir(), "run"),
		`{"status":"error","note":"could not reach the API"}`)
	s.PutRun(ctx, store.Run{ID: "r1", JobID: "job", Outcome: "running",
		RunDir: dir, StartedAt: time.Now()})

	if _, err := reconcileRuns(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.Runs(ctx, "", 10)
	if runs[0].Outcome != "failed" {
		t.Errorf("outcome is %q, want failed", runs[0].Outcome)
	}
}

// Without a result there is nothing to judge, and without an agent nothing can
// produce one — so it parks rather than being guessed at or rerun.
func TestUnjudgeableRunWithNoAgentIsLeftForAHuman(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	s.PutRun(ctx, store.Run{ID: "r1", JobID: "job", Outcome: "running",
		RunDir: t.TempDir(), StartedAt: time.Now()})

	// No agent name: nothing to ask, nothing to adopt.
	if _, err := reconcileRuns(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.Runs(ctx, "", 10)
	if runs[0].Outcome != "running" {
		t.Errorf("outcome is %q; a run with no evidence either way must be left alone", runs[0].Outcome)
	}
}

// Reconciliation must be safe to run repeatedly — the daemon calls it on every
// start, and the sentinel restarts the daemon often.
func TestReconcileIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dir := writeResult(t, filepath.Join(t.TempDir(), "run"), `{"status":"ok","note":"fine"}`)
	s.PutRun(ctx, store.Run{ID: "r1", JobID: "job", Outcome: "running",
		RunDir: dir, StartedAt: time.Now()})

	first, _ := reconcileRuns(ctx, s, nil)
	second, _ := reconcileRuns(ctx, s, nil)
	if first != 1 || second != 0 {
		t.Errorf("resolved %d then %d; the second pass should find nothing left", first, second)
	}
}

// The incident this exists for: a workflow was filed as "parked (no_result)"
// thirty seconds in, while the step it parked on went on to succeed and write
// its result ten minutes later. The row outlived the wrong verdict; the disk
// has the right one.
func TestParkedWorkflowIsCorrectedByItsStepResults(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dir := t.TempDir()

	for _, st := range []struct{ id, body string }{
		{"pull", `{"status":"ok","note":""}`},
		{"pick", `{"status":"ok","note":"n4_ame"}`},
		{"publish", `{"status":"ok","note":"published, playback verified"}`},
	} {
		writeResult(t, runner.StepDir(dir, st.id), st.body)
	}

	if err := s.PutRun(ctx, store.Run{
		ID: "r1", JobID: "shorts", Outcome: "parked", ParkReason: "no_result",
		RunDir: dir, Note: "2/3 steps, parked at publish (no_result)",
		StartedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	steps := []store.Step{{ID: "pull", Run: "true"}, {ID: "pick", Run: "true"},
		{ID: "publish", Agent: "publish it"}}
	if err := s.SeedRunSteps(ctx, "r1", steps); err != nil {
		t.Fatal(err)
	}
	for i, st := range steps {
		outcome, reason := store.StepDone, ""
		if st.ID == "publish" {
			outcome, reason = store.StepParked, "no_result"
		}
		if err := s.PutRunStep(ctx, store.RunStep{RunID: "r1", Index: i,
			StepID: st.ID, Kind: st.Label(), Outcome: outcome, ParkReason: reason,
			StepDir: runner.StepDir(dir, st.ID)}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := reconcileRuns(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.Run(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "done" || got.ParkReason != "" {
		t.Fatalf("run is %q/%q, want done with no park reason", got.Outcome, got.ParkReason)
	}
	if got.Note != "3/3 steps ok" {
		t.Errorf("note is %q, want %q", got.Note, "3/3 steps ok")
	}
	rows, err := s.RunSteps(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Outcome != store.StepDone {
			t.Errorf("step %s is %q, want done", row.StepID, row.Outcome)
		}
	}
	if rows[2].Note != "published, playback verified" {
		t.Errorf("publish note is %q, want the result file's", rows[2].Note)
	}
}

// A workflow that really did stop partway stays parked. Correcting the step
// that succeeded must not promote a run whose later steps never ran.
func TestParkedWorkflowWithUnrunStepsStaysParked(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeResult(t, runner.StepDir(dir, "first"), `{"status":"ok","note":"did it"}`)

	if err := s.PutRun(ctx, store.Run{ID: "r2", JobID: "j", Outcome: "parked",
		ParkReason: "agent_lost", RunDir: dir, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	steps := []store.Step{{ID: "first", Agent: "a"}, {ID: "second", Agent: "b"}}
	if err := s.SeedRunSteps(ctx, "r2", steps); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunStep(ctx, store.RunStep{RunID: "r2", Index: 0, StepID: "first",
		Kind: "agent", Outcome: store.StepParked, ParkReason: "no_result",
		StepDir: runner.StepDir(dir, "first")}); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcileRuns(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(ctx, "r2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "parked" {
		t.Fatalf("run is %q, want it to stay parked", got.Outcome)
	}
	rows, _ := s.RunSteps(ctx, "r2")
	if rows[0].Outcome != store.StepDone {
		t.Errorf("first step is %q, want done: its result file says so", rows[0].Outcome)
	}
}

// A park that means a human is needed is not a park for want of a result, and
// is left exactly as it is.
func TestBlockedParkIsNotReopened(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dir := writeResult(t, filepath.Join(t.TempDir(), "run"), `{"status":"ok","note":"n"}`)
	if err := s.PutRun(ctx, store.Run{ID: "r3", JobID: "j", Outcome: "parked",
		ParkReason: "blocked", RunDir: dir, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileRuns(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run(ctx, "r3")
	if got.Outcome != "parked" || got.ParkReason != "blocked" {
		t.Fatalf("run is %q/%q, want it untouched", got.Outcome, got.ParkReason)
	}
}

// A single-prompt run parked for want of a result is corrected the same way.
func TestParkedSingleRunAdoptsALateResult(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	dir := writeResult(t, filepath.Join(t.TempDir(), "run"),
		`{"status":"ok","note":"finished after we stopped looking"}`)
	if err := s.PutRun(ctx, store.Run{ID: "r4", JobID: "j", Outcome: "parked",
		ParkReason: "no_result", RunDir: dir, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileRuns(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run(ctx, "r4")
	if got.Outcome != "done" || got.Note != "finished after we stopped looking" {
		t.Fatalf("run is %q/%q, want done with the result's note", got.Outcome, got.Note)
	}
}
