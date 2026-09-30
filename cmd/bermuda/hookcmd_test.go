package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

func TestHookStatusShowsSkippedAndDeadEventsAndRedeliveryKeepsTheirIDs(t *testing.T) {
	ctx := jobCmdEnv(t)
	s := storeForEnv(t)
	now := time.Now().UTC()
	for _, id := range []string{"run-skipped", "run-dead"} {
		if err := s.PutRun(ctx, store.Run{ID: id, JobID: "brief", Outcome: "done", StartedAt: now, EndedAt: &now}); err != nil {
			t.Fatal(err)
		}
	}
	skipped, err := s.RunEvents(ctx, "run-skipped")
	if err != nil {
		t.Fatal(err)
	}
	dead, err := s.RunEvents(ctx, "run-dead")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRunEventAttempt(ctx, skipped[0], now, nil, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.MaxHookAttempts; i++ {
		e, err := s.RunEvent(ctx, dead[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RecordRunEventAttempt(ctx, e, now, errors.New("refused"), false); err != nil {
			t.Fatal(err)
		}
	}
	out, err := captureStdout(t, func() error { return hookCmd([]string{"status"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "skipped") || !strings.Contains(out, "dead") || !strings.Contains(out, "no hook") {
		t.Fatalf("hook status = %q", out)
	}
	if err := hookCmd([]string{"redeliver", strconv.FormatInt(skipped[0].ID, 10)}); err != nil {
		t.Fatal(err)
	}
	if err := hookCmd([]string{"redeliver", "--dead"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{skipped[0].ID, dead[0].ID} {
		e, err := s.RunEvent(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Status() != "pending" || e.Attempts != 0 {
			t.Fatalf("redelivered event = %+v", e)
		}
	}
}

func TestHookRedeliverRejectsMissingIDsAndMixingAnIDWithDead(t *testing.T) {
	jobCmdEnv(t)
	for _, argv := range [][]string{{"redeliver"}, {"redeliver", "1000000"}, {"redeliver", "--dead", "42"}, {"redeliver", "42", "--dead"}} {
		if err := hookCmd(argv); err == nil {
			t.Errorf("accepted %v", argv)
		}
	}
	if err := hookCmd([]string{"status", "extra"}); err == nil {
		t.Fatal("status accepted an extra argument")
	}
}
