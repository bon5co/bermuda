package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAJobReferenceRoundTripsVerbatimAndARunKeepsTheReferenceItStartedWith(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	ref := "Ticket: Mixed Case / #42"
	j := Job{ID: "brief", Prompt: "write", Ref: ref}
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	gotJob, err := s.Job(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotJob.Ref != ref {
		t.Fatalf("job ref = %q, want %q", gotJob.Ref, ref)
	}
	r := Run{ID: "run-1", JobID: j.ID, Outcome: "running", StartedAt: time.Now(), Ref: gotJob.Ref}
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	j.Ref = "changed later"
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	gotRun, err := s.Run(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.Ref != ref {
		t.Fatalf("run ref = %q, want the original %q", gotRun.Ref, ref)
	}
}

func TestAReferenceRejectsNewlinesAndMoreThan512Bytes(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, ref := range []string{"line\nbreak", "line\rbreak", strings.Repeat("x", 513)} {
		if err := s.PutJob(context.Background(), Job{ID: "brief", Prompt: "write", Ref: ref}); err == nil {
			t.Errorf("accepted invalid reference %q", ref)
		}
	}
}
