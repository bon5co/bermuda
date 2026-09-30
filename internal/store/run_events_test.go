package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSettlingARunEnqueuesOnceAndATimestampRewriteDoesNotEnqueueAgain(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 30, 7, 0, 0, 0, time.UTC)
	r := Run{ID: "run-1", JobID: "brief", Outcome: "running", StartedAt: now, Ref: "Ticket: Mixed / #42"}
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Outcome, r.Note, r.EndedAt = "done", "published", &now
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	changedEnd := now.Add(time.Second)
	r.EndedAt = &changedEnd
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	events, err := s.RunEvents(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Settlement != 1 || events[0].PreviousOutcome != "" {
		t.Fatalf("events = %+v", events)
	}
	var payload struct {
		Version         int    `json:"version"`
		Event           string `json:"event"`
		EventID         int64  `json:"event_id"`
		Settlement      int    `json:"settlement"`
		PreviousOutcome string `json:"previous_outcome"`
		Run             struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
			Ref     string `json:"ref"`
			Note    string `json:"note"`
			EndedAt string `json:"ended_at"`
		} `json:"run"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != 1 || payload.Event != "run.settled" || payload.EventID != events[0].ID || payload.Settlement != 1 || payload.Run.ID != r.ID || payload.Run.Outcome != "done" || payload.Run.Ref != r.Ref || payload.Run.Note != r.Note || payload.Run.EndedAt != now.Format(time.RFC3339) || string(payload.Result) != "null" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestSettlementPayloadMatchesTheVersionOneWireContract(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Date(2026, 9, 30, 7, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	r := Run{ID: "example-run", JobID: "example-job", Flow: "", Trigger: "scheduled", Outcome: "done", Note: "published", Ref: "Ticket: 42", StartedAt: start, EndedAt: &end, Model: "opus", InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheCreationTokens: 4}
	if err := s.PutRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	events, err := s.RunEvents(context.Background(), r.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["bermuda_version"] = "VERSION"
	got, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "run-settled-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Fatalf("payload drifted:\n%s\nwant:\n%s", got, want)
	}
}

func TestAParkedRunThatResumesThenFinishesGetsTwoOrderedSettlements(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().Truncate(time.Second)
	r := Run{ID: "run-2", JobID: "brief", Outcome: "parked", ParkReason: "blocked", StartedAt: now, EndedAt: &now}
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Outcome, r.ParkReason, r.EndedAt = "running", "", nil
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Second)
	r.Outcome, r.EndedAt = "done", &later
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	events, err := s.RunEvents(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Settlement != 1 || events[1].Settlement != 2 || events[1].PreviousOutcome != "parked" {
		t.Fatalf("events = %+v", events)
	}
}

func TestASettlementPayloadKeepsTheResultFileAsItWasWhenTheRunSettled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runDir := filepath.Join(dir, "runs", "run-3")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDir, "result.json")
	if err := os.WriteFile(path, []byte(`{"status":"ok","note":"first","data":{"value":42}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r := Run{ID: "run-3", JobID: "brief", Outcome: "done", RunDir: runDir, StartedAt: now, EndedAt: &now}
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"status":"error","note":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := s.RunEvents(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	var payload struct {
		Result struct {
			Status string         `json:"status"`
			Note   string         `json:"note"`
			Data   map[string]int `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Result.Status != "ok" || payload.Result.Note != "first" || payload.Result.Data["value"] != 42 {
		t.Fatalf("result changed: %+v", payload.Result)
	}
}

func TestTwoStoreHandlesWritingTheSameSettlementLeaveOneEventAndAReopenDoesNotDuplicateIt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := time.Now().Truncate(time.Second)
	r := Run{ID: "run-concurrent", JobID: "brief", Outcome: "running", StartedAt: now}
	if err := a.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Outcome, r.EndedAt = "done", &now
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func(s *Store) { defer wg.Done(); errs <- s.PutRun(ctx, r) }(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	events, err := reopened.RunEvents(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("concurrent and reopened rewrite produced %d events, want one", len(events))
	}
}

func TestAnOutboxInsertFailureRollsBackTheRunSettlement(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	r := Run{ID: "run-rollback", JobID: "brief", Outcome: "running", StartedAt: now}
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_run_event BEFORE INSERT ON run_events BEGIN SELECT RAISE(FAIL, 'test event failure'); END`); err != nil {
		t.Fatal(err)
	}
	r.Outcome, r.EndedAt = "done", &now
	if err := s.PutRun(ctx, r); err == nil {
		t.Fatal("settlement succeeded after the outbox rejected its event")
	}
	got, err := s.Run(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "running" {
		t.Fatalf("run outcome = %s after rollback, want running", got.Outcome)
	}
}
