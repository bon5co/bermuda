package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// Rows written before the run directory was recorded up front carry an empty
// one. Reconciliation must still find the result the runner left, or those runs
// stay "running" forever with a finished result sitting on disk beside them.
func TestRunWithNoRecordedDirectoryIsFoundWhereTheRunnerPutsIt(t *testing.T) {
	state := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", state)
	s := newStore(t)
	ctx := context.Background()

	writeResult(t, runDirFor("r-old"), `{"status":"ok","note":"landed before we recorded the dir"}`)
	if err := s.PutRun(ctx, store.Run{ID: "r-old", JobID: "j", Outcome: "running",
		StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcileRuns(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(ctx, "r-old")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "done" {
		t.Fatalf("run is %q, want done: its result file is in the conventional directory", got.Outcome)
	}
	if got.RunDir != runDirFor("r-old") {
		t.Errorf("run dir is %q, want %q recorded so the board can open it", got.RunDir, runDirFor("r-old"))
	}
	if got.Note != "landed before we recorded the dir" {
		t.Errorf("note is %q, want the result file's", got.Note)
	}
}

// runDirFor must track the state directory, not a path captured at startup: the
// tests and the daemon disagree about where ~/.bermuda is.
func TestRunDirFollowsTheStateDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	if got, want := runDirFor("r1"), filepath.Join(dir, "runs", "r1"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The end time is when the work actually finished, not when a later process
// noticed — otherwise a run stranded overnight is filed as having taken all
// night, and every duration read off the board is wrong.
func TestEndedAtIsTheResultFilesTime(t *testing.T) {
	dir := writeResult(t, filepath.Join(t.TempDir(), "run"), `{"status":"ok","note":""}`)
	want := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "result.json"), want, want); err != nil {
		t.Fatal(err)
	}

	got := endedAt(dir)
	if got == nil {
		t.Fatal("no end time for a directory holding a result")
	}
	if !got.Truncate(time.Second).Equal(want) {
		t.Errorf("ended at %s, want the result file's %s", got, want)
	}
}

// With no result file there is nothing better to say than "now", but it must
// still be an answer: a nil end time would leave the row looking unfinished.
func TestEndedAtFallsBackToNow(t *testing.T) {
	before := time.Now().Add(-time.Second)
	got := endedAt(t.TempDir())
	if got == nil {
		t.Fatal("nil end time; the row would still look in flight")
	}
	if got.Before(before) {
		t.Errorf("ended at %s, want roughly now", got)
	}
}
