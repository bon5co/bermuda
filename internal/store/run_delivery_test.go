package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newSettledEvent(t *testing.T, s *Store, id string) RunEvent {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.PutRun(context.Background(), Run{ID: id, JobID: "brief", Outcome: "done", StartedAt: now, EndedAt: &now}); err != nil {
		t.Fatal(err)
	}
	events, err := s.RunEvents(context.Background(), id)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	return events[0]
}

func TestAFailedHookRetriesFourTimesThenDiesWithoutBlockingAnotherRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	first := newSettledEvent(t, s, "run-first")
	other := newSettledEvent(t, s, "run-other")
	now := time.Now().UTC()
	later := now.Add(time.Second)
	if err := s.PutRun(ctx, Run{ID: "run-first", JobID: "brief", Outcome: "parked", StartedAt: now, EndedAt: &later}); err != nil {
		t.Fatal(err)
	}
	laterEvents, err := s.RunEvents(ctx, "run-first")
	if err != nil || len(laterEvents) != 2 {
		t.Fatalf("later settlement events = %+v, err = %v", laterEvents, err)
	}
	laterEvent := laterEvents[1]
	for attempt := 1; attempt <= 5; attempt++ {
		if err := s.RecordRunEventAttempt(ctx, first, now, errors.New("hook refused"), false); err != nil {
			t.Fatal(err)
		}
		first, err = s.RunEvent(ctx, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if first.Attempts != attempt {
			t.Fatalf("attempts = %d, want %d", first.Attempts, attempt)
		}
		if attempt < 5 && first.Status() != "retrying" {
			t.Fatalf("status = %s after attempt %d", first.Status(), attempt)
		}
	}
	if first.Status() != "dead" {
		t.Fatalf("status = %s, want dead", first.Status())
	}
	due, err := s.DueRunEvents(ctx, now.Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != other.ID {
		t.Fatalf("due events = %+v, want only the other run while the first is dead", due)
	}
	if err := s.RedeliverRunEvent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	redelivered, err := s.RunEvent(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	due, err = s.DueRunEvents(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 || due[0].ID != redelivered.ID || due[1].ID != other.ID {
		t.Fatalf("due events after redelivery = %+v, want first and unrelated runs only", due)
	}
	if err := s.RecordRunEventAttempt(ctx, redelivered, time.Now().UTC(), nil, false); err != nil {
		t.Fatal(err)
	}
	due, err = s.DueRunEvents(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	foundLater := false
	for _, event := range due {
		if event.ID == laterEvent.ID {
			foundLater = true
		}
	}
	if !foundLater {
		t.Fatalf("later settlement %d was not released after its predecessor was delivered: %+v", laterEvent.ID, due)
	}
}

func TestRedeliveryKeepsTheFrozenPayloadAndRejectsAnOldInFlightAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e := newSettledEvent(t, s, "run-redeliver")
	original := string(e.Payload)
	if err := s.RedeliverRunEvent(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunEventAttempt(ctx, e, time.Now(), nil, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunEvent(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != e.ID || string(got.Payload) != original || got.Status() != "pending" || got.Generation == e.Generation {
		t.Fatalf("redelivered event = %+v", got)
	}
}
