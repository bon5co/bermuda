package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A closed thread is not a deleted one, and the two lists exist to keep that
// distinction usable: Threads is read to choose where to write, ClosedThreads
// is read to find what was said. A thread that leaked from one into the other
// would either be offered as a write target that refuses every write, or lost
// from the record entirely — and losing it is the deletion nobody notices
// until they go looking.

// closing moves a thread from one list to the other. Both halves are asserted
// together because either alone would pass with the thread in both lists.
func TestAClosedThreadLeavesTheOpenListAndJoinsTheClosedOne(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.NewThread(ctx, "webapp", "the shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewThread(ctx, "vault", "notes"); err != nil {
		t.Fatal(err)
	}

	if err := s.CloseThread(ctx, "webapp", base); err != nil {
		t.Fatal(err)
	}

	open, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ids := threadIDs(open); contains(ids, "webapp") {
		t.Errorf("open threads %v still offer webapp, which refuses every write", ids)
	}
	if ids := threadIDs(open); !contains(ids, "vault") {
		t.Errorf("open threads %v lost vault, which nobody closed", ids)
	}

	closed, err := s.ClosedThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ids := threadIDs(closed); len(ids) != 1 || ids[0] != "webapp" {
		t.Fatalf("closed threads %v, want exactly webapp", ids)
	}
	if !closed[0].Closed() {
		t.Error("a thread from ClosedThreads reports itself open")
	}
	if closed[0].ClosedAt == nil || !closed[0].ClosedAt.Equal(base) {
		t.Errorf("closed at %v, want the instant it was closed, %v",
			closed[0].ClosedAt, base)
	}
}

// The record survives the window. A closed thread keeps its message count and
// its last activity, because that is what makes it worth keeping at all.
func TestAClosedThreadKeepsItsRecord(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.NewThread(ctx, "webapp", "the shop"); err != nil {
		t.Fatal(err)
	}
	for i, body := range []string{"deployed", "rolled back"} {
		if _, err := s.ThreadPost(ctx, ThreadMessage{Thread: "webapp",
			Kind: KindEvent, By: alice, Body: body,
			CreatedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseThread(ctx, "webapp", base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	closed, err := s.ClosedThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 {
		t.Fatalf("got %d closed threads, want 1", len(closed))
	}
	if closed[0].Messages != 2 {
		t.Errorf("closed thread reports %d messages, want the 2 it holds",
			closed[0].Messages)
	}
	if !closed[0].LastAt.Equal(base.Add(time.Minute)) {
		t.Errorf("last activity %v, want %v", closed[0].LastAt, base.Add(time.Minute))
	}

	// And the log still reads, which is the whole reason closing is not
	// deleting.
	log, err := s.ThreadLog(ctx, ThreadFilter{Thread: "webapp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 {
		t.Errorf("read %d messages back from a closed thread, want 2", len(log))
	}
}

// An open thread reports itself open, and Closed() is what every caller uses
// to decide. Asserting the negative case keeps a `return true` from passing.
func TestAnOpenThreadIsNotClosed(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	threads, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) == 0 {
		t.Fatal("global is created on open, so there is always one thread")
	}
	for _, th := range threads {
		if th.Closed() {
			t.Errorf("thread %s came from the open list reporting itself closed", th.ID)
		}
	}
	closed, err := s.ClosedThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Errorf("got %d closed threads before anything was closed", len(closed))
	}
}

// The two refusals must not be confused: one is a typo to fix, the other is a
// space that has been shut. Suggesting `thread new` for a closed thread would
// be actively wrong — it would tell an agent to recreate a conversation whose
// record already exists.
func TestAClosedThreadAndAnUnknownOneRefuseDifferently(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.NewThread(ctx, "webapp", "the shop"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseThread(ctx, "webapp", base); err != nil {
		t.Fatal(err)
	}

	_, closedErr := s.ThreadPost(ctx, ThreadMessage{Thread: "webapp",
		Kind: KindEvent, By: alice, Body: "too late", CreatedAt: base})
	if closedErr == nil {
		t.Fatal("a write into a closed thread succeeded")
	}
	if !strings.Contains(closedErr.Error(), "closed") {
		t.Errorf("closed-thread refusal %q does not say it is closed", closedErr)
	}
	if strings.Contains(closedErr.Error(), "thread new") {
		t.Errorf("closed-thread refusal %q suggests recreating it, which would "+
			"split the record in two", closedErr)
	}

	_, unknownErr := s.ThreadPost(ctx, ThreadMessage{Thread: "webapl",
		Kind: KindEvent, By: alice, Body: "typo", CreatedAt: base})
	if unknownErr == nil {
		t.Fatal("a write into a thread nobody created succeeded")
	}
	if !strings.Contains(unknownErr.Error(), "thread new") {
		t.Errorf("unknown-thread refusal %q does not say how to create it", unknownErr)
	}
}

func threadIDs(threads []Thread) []string {
	out := make([]string, 0, len(threads))
	for _, t := range threads {
		out = append(out, t.ID)
	}
	return out
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
