package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A workspace thread exists because somebody opened a window, not because
// anybody created it. The first agent to say anything in a space gets the
// thread; every agent after it gets the same one.
func TestAWorkspaceThreadIsCreatedOnceAndSharedAfterwards(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	first, err := s.EnsureWorkspaceThread(ctx, "w1", "Better Lingo")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "better-lingo" {
		t.Errorf("named the thread %q, want the label slugged", first.ID)
	}

	second, err := s.EnsureWorkspaceThread(ctx, "w1", "Better Lingo")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("the second agent in the space got %q, want %q — one space is one "+
			"conversation", second.ID, first.ID)
	}
}

// The thread is bound to the workspace id, so renaming the space keeps the
// conversation. The id deliberately does not follow the rename: it is written
// into cron entries, scripts and messages already delivered, and an id that
// moves is an id that stops resolving.
func TestRenamingAWorkspaceKeepsItsThread(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	before, err := s.EnsureWorkspaceThread(ctx, "w1", "Better Lingo")
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.EnsureWorkspaceThread(ctx, "w1", "Shopee")
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID {
		t.Fatalf("after the rename the space resolved to %q, want %q", after.ID, before.ID)
	}
}

// Two spaces a person gave the same name must not share a conversation. The
// slug is a convenience; the workspace id is the identity.
func TestTwoWorkspacesWithOneNameGetTwoThreads(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	a, err := s.EnsureWorkspaceThread(ctx, "w1", "bermuda")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsureWorkspaceThread(ctx, "w2", "bermuda")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("both spaces got thread %q, want the second disambiguated", a.ID)
	}
	if !strings.HasPrefix(b.ID, "bermuda-") {
		t.Errorf("the second space got %q, want the slug with a suffix", b.ID)
	}
	if _, err := ParseThreadID(b.ID); err != nil {
		t.Errorf("generated thread id %q is not a legal one: %v", b.ID, err)
	}
}

// A space nobody labelled, and a space somebody called "global", both need an
// id that cannot collide with the thread that must always exist.
func TestAnUnlabelledWorkspaceGetsAnOpaqueID(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	for _, label := range []string{"", "   ", "global", "!!!"} {
		got, err := s.EnsureWorkspaceThread(ctx, "ws-for-"+label, label)
		if err != nil {
			t.Fatalf("label %q: %v", label, err)
		}
		if got.ID == GlobalThread {
			t.Fatalf("label %q produced the global thread", label)
		}
		if _, err := ParseThreadID(got.ID); err != nil {
			t.Errorf("label %q produced %q, not a legal id: %v", label, got.ID, err)
		}
	}
}

// Closing is not deleting. The window is shut; what changed on the machine
// while it was open is still true, and still readable.
func TestClosingAThreadKeepsEveryMessageAndRefusesNewOnes(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	by := Identity{Name: "ada"}

	th, err := s.EnsureWorkspaceThread(ctx, "w1", "Better Lingo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{
		Thread: th.ID, Kind: KindNote, By: by, Body: "gog is gmail only"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseThread(ctx, th.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Gone from the list you choose where to write from...
	open, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range open {
		if o.ID == th.ID {
			t.Fatalf("closed thread %q is still in the open list", th.ID)
		}
	}
	// ...but still readable, which is the whole reason for closing over deleting.
	log, err := s.ThreadLog(ctx, ThreadFilter{Thread: th.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].Body != "gog is gmail only" {
		t.Fatalf("read %d messages back from the closed thread, want the one that was written", len(log))
	}
	// And refuses new ones, saying which of the two failures this is.
	_, err = s.ThreadPost(ctx, ThreadMessage{
		Thread: th.ID, Kind: KindNote, By: by, Body: "too late"})
	if err == nil {
		t.Fatal("a closed thread accepted a message")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("the refusal was %q, want it to say the thread is closed", err)
	}
}

// The daemon closes the threads of spaces that have gone, and leaves alone both
// the ones still open and the ones nobody automated.
func TestVanishedWorkspacesAreClosedAndHandMadeThreadsAreNot(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	alive, err := s.EnsureWorkspaceThread(ctx, "w1", "alive")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.EnsureWorkspaceThread(ctx, "w2", "gone")
	if err != nil {
		t.Fatal(err)
	}
	byHand, err := s.NewThread(ctx, "by-hand", "somebody made this")
	if err != nil {
		t.Fatal(err)
	}

	closed, err := s.CloseVanishedWorkspaces(ctx, []string{"w1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || closed[0] != gone.ID {
		t.Fatalf("closed %v, want only %q", closed, gone.ID)
	}

	open, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	still := map[string]bool{}
	for _, o := range open {
		still[o.ID] = true
	}
	if !still[alive.ID] {
		t.Error("closed the thread of a workspace that is still open")
	}
	if !still[byHand.ID] {
		t.Error("closed a thread with no workspace: herdr's list says nothing about it")
	}
	if still[gone.ID] {
		t.Error("the vanished workspace's thread is still open")
	}
}

// A lease taken from a thread refuses the close, for the same reason it refuses
// a delete: the agent holding the resource has to be able to give it back in
// the conversation it took it in.
func TestAThreadIsNotClosedWhileSomethingIsHeldFromIt(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	th, err := s.EnsureWorkspaceThread(ctx, "w1", "Better Lingo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{
		Resource: "browser", By: Identity{Name: "ada"}, Thread: th.ID,
		TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	err = s.CloseThread(ctx, th.ID, time.Now())
	if err == nil {
		t.Fatal("closed a thread a live lease was taken from")
	}
	if !strings.Contains(err.Error(), "browser") {
		t.Errorf("the refusal was %q, want it to name what is held", err)
	}

	// And the daemon's sweep tolerates it rather than failing: the resource
	// coming back is how this resolves, and one stuck lease must not stop every
	// other thread from ever being tidied.
	other, err := s.EnsureWorkspaceThread(ctx, "w2", "tidy me")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.CloseVanishedWorkspaces(ctx, []string{}, time.Now())
	if err != nil {
		t.Fatalf("the sweep failed over a held resource: %v", err)
	}
	if len(closed) != 1 || closed[0] != other.ID {
		t.Fatalf("closed %v, want only %q — the held one skipped, not fatal", closed, other.ID)
	}
}

// Global is never closed. It is where every unqualified write lands, and a
// closed global would leave those writes with nowhere to go.
func TestGlobalCannotBeClosed(t *testing.T) {
	s := newThread(t)
	if err := s.CloseThread(context.Background(), GlobalThread, time.Now()); err == nil {
		t.Fatal("global was closed")
	}
}

func TestSlugThread(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Better Lingo", "better-lingo"},
		{"bermuda", "bermuda"},
		{"Tokyo Day Trip", "tokyo-day-trip"},
		{"a/b:c", "a-b-c"},
		{"  padded  ", "padded"},
		{"--leading--", "leading"},
		{"!!!", ""},
		{"", ""},
	} {
		if got := SlugThread(tc.in); got != tc.want {
			t.Errorf("SlugThread(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
