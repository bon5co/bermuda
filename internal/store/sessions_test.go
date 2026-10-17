package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestJobSessionSurvivesReopenAndReplacement(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.JobSession(ctx, "mail"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	want := JobSession{JobID: "mail", Harness: "pi", Kind: "path", Value: "/tmp/session.jsonl", RunID: "run-1", CapturedAt: time.Unix(123, 0)}
	if err := s.PutJobSession(ctx, want); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.JobSession(ctx, "mail")
	if err != nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	want.Value, want.RunID = "new-session", "run-2"
	if err := s.PutJobSession(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.JobSession(ctx, "mail")
	if err != nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("replacement: %+v, %v", got, err)
	}
}

func TestJobSessionClearedAndRemovedWithJob(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutJob(ctx, Job{ID: "mail"}); err != nil {
		t.Fatal(err)
	}
	session := JobSession{JobID: "mail", Harness: "codex", Kind: "id", Value: "old", RunID: "run-1"}
	if err := s.PutJobSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteJobSession(ctx, "mail"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JobSession(ctx, "mail"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared session remains: %v", err)
	}
	if err := s.PutJobSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteJob(ctx, "mail"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JobSession(ctx, "mail"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted job retained old conversation: %v", err)
	}
}

func TestHadPriorConversationIgnoresCurrentRunAndFailedLaunch(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, r := range []Run{
		{ID: "current", JobID: "mail", Context: "fresh", Outcome: "running", TabID: "tab", Status: "idle", StartedAt: time.Unix(300, 0)},
		{ID: "failed", JobID: "mail", Context: "fresh", Outcome: "parked", StartedAt: time.Unix(200, 0)},
		{ID: "old-opted-out", JobID: "mail", Outcome: "done", TabID: "tab", Status: "idle", StartedAt: time.Unix(100, 0)},
	} {
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	had, err := s.HadPriorConversation(ctx, "mail", "current")
	if err != nil || had {
		t.Fatalf("false prior conversation: %t, %v", had, err)
	}
	if err := s.PutRun(ctx, Run{ID: "settled", JobID: "mail", Context: "lost", Outcome: "done",
		TabID: "tab", Status: "idle", StartedAt: time.Unix(150, 0)}); err != nil {
		t.Fatal(err)
	}
	had, err = s.HadPriorConversation(ctx, "mail", "current")
	if err != nil || !had {
		t.Fatalf("missed prior conversation: %t, %v", had, err)
	}
}

func FuzzContextRunRoundTrip(f *testing.F) {
	f.Add("resumed", "id", "session-123", "previous session changed")
	f.Add("lost", "path", "/tmp/a b.jsonl", "")
	f.Fuzz(func(t *testing.T, status, kind, value, note string) {
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ctx := context.Background()
		j := Job{ID: "mail", OnContextLoss: "park"}
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		job, err := s.Job(ctx, j.ID)
		if err != nil || job.OnContextLoss != "park" {
			t.Fatalf("job: %+v, %v", job, err)
		}
		want := Run{ID: "run", JobID: j.ID, Outcome: "done", Context: status, ContextSession: value, ContextNote: note, StartedAt: time.Unix(100, 0)}
		if err := s.PutRun(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.Run(ctx, want.ID)
		if err != nil || got.Context != status || got.ContextSession != value || got.ContextNote != note {
			t.Fatalf("run: %+v, %v", got, err)
		}
		session := JobSession{JobID: j.ID, Harness: "codex", Kind: kind, Value: value, RunID: want.ID, CapturedAt: time.Unix(100, 0)}
		if err := s.PutJobSession(ctx, session); err != nil {
			t.Fatal(err)
		}
		saved, err := s.JobSession(ctx, j.ID)
		if err != nil || !reflect.DeepEqual(*saved, session) {
			t.Fatalf("session: %+v, %v", saved, err)
		}
	})
}
