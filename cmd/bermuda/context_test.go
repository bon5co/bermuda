package main

import (
	"context"
	"github.com/bon5co/bermuda/v3/internal/runner"
	"github.com/bon5co/bermuda/v3/internal/store"
	"strings"
	"testing"
	"time"
)

func TestContextLossFlagValidatesAndPreservesEdits(t *testing.T) {
	j := store.Job{Persistent: true, KeepContext: true, Catchup: store.CatchupLatest, Schedule: store.ScheduleManual}
	if err := applyArgs(t, &j, "--on-context-loss=park"); err != nil {
		t.Fatal(err)
	}
	if j.OnContextLoss != "park" || runner.FromStore(j).OnContextLoss != "park" {
		t.Fatal("policy dropped")
	}
	if err := applyArgs(t, &j, "--name=new"); err != nil || j.OnContextLoss != "park" {
		t.Fatal("unrelated edit reset policy")
	}
	if err := applyArgs(t, &j, "--on-context-loss=ignore"); err == nil {
		t.Fatal("invalid policy accepted")
	}
}

func TestPersistAndShowContext(t *testing.T) {
	runCmdEnv(t)
	s := storeForEnv(t)
	run := &runner.Run{RunID: "run", JobID: "mail", Outcome: runner.OutcomeDone, Context: "resumed", ContextSession: "session-123", ContextNote: "remembered",
		Result: &runner.Result{Status: "ok", Note: "PELICAN"}, StartedAt: time.Now(), EndedAt: time.Now()}
	// No transcript directory: usage remains optional bookkeeping.
	if err := persist(context.Background(), s, run, "manual", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if got.Context != "resumed" || got.ContextSession != "session-123" || got.ContextNote != "remembered" {
		t.Fatalf("dropped context: %+v", got)
	}
	out, err := captureStdout(t, func() error { return runShow([]string{"run"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"context", "resumed", "session-123", "PELICAN"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}
