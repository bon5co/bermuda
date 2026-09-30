//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

func hookEvent(t *testing.T, dir string) (*store.Store, store.RunEvent) {
	t.Helper()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	r := store.Run{ID: "run-hook", JobID: "brief", Outcome: "done", Ref: "Ticket: 42", RunDir: filepath.Join(dir, "runs", "run-hook"), StartedAt: now, EndedAt: &now}
	if err := s.PutRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	events, err := s.RunEvents(context.Background(), r.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	return s, events[0]
}

func writeHook(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks", "run-settled"), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestExecutableHookReceivesFrozenPayloadAndReferenceThenMarksTheEventDelivered(t *testing.T) {
	dir := t.TempDir()
	s, e := hookEvent(t, dir)
	writeHook(t, dir, "cat > payload.json\nprintf '%s|%s|%s' \"$BERMUDA_EVENT_ID\" \"$BERMUDA_RUN_OUTCOME\" \"$BERMUDA_REF\" > env.txt\n")
	skipped, err := runSettledHook(context.Background(), dir, e, time.Second)
	if err != nil || skipped {
		t.Fatalf("hook skipped=%t err=%v", skipped, err)
	}
	if err := s.RecordRunEventAttempt(context.Background(), e, time.Now(), nil, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunEvent(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != "delivered" {
		t.Fatalf("status = %s", got.Status())
	}
	payload, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(payload)) != string(e.Payload) {
		t.Fatalf("hook payload = %s, want %s", payload, e.Payload)
	}
	env, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "|done|Ticket: 42") {
		t.Fatalf("hook env = %q", env)
	}
}

func TestNonzeroHookExitIsReportedForRetryWithoutChangingTheRunOutcome(t *testing.T) {
	dir := t.TempDir()
	s, e := hookEvent(t, dir)
	writeHook(t, dir, "echo refused >&2\nexit 17\n")
	skipped, err := runSettledHook(context.Background(), dir, e, time.Second)
	if skipped || err == nil {
		t.Fatalf("hook skipped=%t err=%v", skipped, err)
	}
	if err := s.RecordRunEventAttempt(context.Background(), e, time.Now(), err, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunEvent(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != "retrying" || got.Attempts != 1 || !strings.Contains(got.LastError, "17") {
		t.Fatalf("event = %+v", got)
	}
	run, err := s.Run(context.Background(), e.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != "done" {
		t.Fatalf("hook failure changed outcome to %s", run.Outcome)
	}
}

func TestTimedOutHookKillsItsChildProcessBeforeItCanWrite(t *testing.T) {
	dir := t.TempDir()
	_, e := hookEvent(t, dir)
	writeHook(t, dir, "(sleep 0.3; echo escaped > child-wrote) &\nwait\n")
	skipped, err := runSettledHook(context.Background(), dir, e, 40*time.Millisecond)
	if skipped || err == nil {
		t.Fatalf("timeout skipped=%t err=%v", skipped, err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "child-wrote")); !os.IsNotExist(err) {
		t.Fatalf("child survived timeout: %v", err)
	}
}

func TestAnEventRecordedWhileTheDaemonIsDownDeliversAfterTheStoreReopens(t *testing.T) {
	dir := t.TempDir()
	s, e := hookEvent(t, dir)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	writeHook(t, dir, "cat > after-restart.json\n")
	if err := deliverRunEvents(context.Background(), reopened, dir, runSettledHook); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.RunEvent(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != "delivered" {
		t.Fatalf("status = %s", got.Status())
	}
	if _, err := os.ReadFile(filepath.Join(dir, "after-restart.json")); err != nil {
		t.Fatal(err)
	}
}

func TestAReachingRetryHoldsOnlyLaterEventsForTheSameRun(t *testing.T) {
	dir := t.TempDir()
	s, first := hookEvent(t, dir)
	ctx := context.Background()
	r, err := s.Run(ctx, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	r.Outcome, r.EndedAt = "running", nil
	if err := s.PutRun(ctx, *r); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	r.Outcome, r.EndedAt = "failed", &later
	if err := s.PutRun(ctx, *r); err != nil {
		t.Fatal(err)
	}
	other := store.Run{ID: "run-other", JobID: "brief", Outcome: "done", StartedAt: later, EndedAt: &later}
	if err := s.PutRun(ctx, other); err != nil {
		t.Fatal(err)
	}
	var called []int64
	invoke := func(_ context.Context, _ string, e store.RunEvent, _ time.Duration) (bool, error) {
		called = append(called, e.ID)
		if e.ID == first.ID {
			return false, fmt.Errorf("try again")
		}
		return false, nil
	}
	if err := deliverRunEvents(ctx, s, dir, invoke); err != nil {
		t.Fatal(err)
	}
	if len(called) != 2 || called[0] != first.ID {
		t.Fatalf("called = %v, want first and unrelated run only", called)
	}
	events, err := s.RunEvents(ctx, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Status() != "retrying" || events[1].Status() != "pending" {
		t.Fatalf("same-run events = %+v", events)
	}
}

func TestInstallingAHookLaterRequiresExplicitRedeliveryOfASkippedEvent(t *testing.T) {
	dir := t.TempDir()
	s, e := hookEvent(t, dir)
	ctx := context.Background()
	if err := deliverRunEvents(ctx, s, dir, runSettledHook); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunEvent(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != "skipped" || got.LastError != "no hook" {
		t.Fatalf("missing hook event = %+v", got)
	}
	writeHook(t, dir, "cat > redelivered.json\n")
	if err := deliverRunEvents(ctx, s, dir, runSettledHook); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "redelivered.json")); !os.IsNotExist(err) {
		t.Fatalf("skipped event delivered without a request: %v", err)
	}
	if err := s.RedeliverRunEvent(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := deliverRunEvents(ctx, s, dir, runSettledHook); err != nil {
		t.Fatal(err)
	}
	got, err = s.RunEvent(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != "delivered" {
		t.Fatalf("status after redelivery = %s", got.Status())
	}
	if _, err := os.ReadFile(filepath.Join(dir, "redelivered.json")); err != nil {
		t.Fatal(err)
	}
}
