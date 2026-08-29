package main

import (
	"context"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// noWorkspace makes a test answer the workspace question itself.
//
// Without it these tests ask the real herdr on this machine, and the first run
// proved why that is not acceptable: resolveThread found the actual workspace
// the test was running in and created a thread for it in the real database. A
// test that mutates the state the user's own agents coordinate through is a
// test that has already done damage by the time it fails.
func noWorkspace(t *testing.T) {
	t.Helper()
	previous := currentWorkspace
	currentWorkspace = func(context.Context) (string, string) { return "", "" }
	t.Cleanup(func() { currentWorkspace = previous })
}

// Which thread a command writes into decides who ever reads it, so the
// selection rule is worth pinning: an agent that exported $BERMUDA_THREAD for
// its whole run must not have one stray command land in global, and a command
// that names a thread must not be overridden by the environment it inherited.
func TestResolveThreadPrefersTheFlagThenTheEnvironment(t *testing.T) {
	noWorkspace(t)
	t.Setenv("BERMUDA_THREAD", "tiktok-deals")
	if got, err := resolveThread("webapp"); err != nil || got != "webapp" {
		t.Errorf("--thread resolved to %q, %v: the flag has to beat the environment", got, err)
	}
	if got, err := resolveThread(""); err != nil || got != "tiktok-deals" {
		t.Errorf("with no flag the thread resolved to %q, %v, want $BERMUDA_THREAD", got, err)
	}

	t.Setenv("BERMUDA_THREAD", "")
	if got, err := resolveThread(""); err != nil || got != store.GlobalThread {
		t.Errorf("with nothing set the thread resolved to %q, %v, want global", got, err)
	}
}

// An agent that names no thread writes into the thread for the space it is
// working in, and does not have to create it or know that it exists.
//
// This is the step that makes threads usable by agents that were never told
// about them. Without it the default is global, and global is where an agent
// ends up precisely when nobody configured it — so the conversation the rest of
// its workspace is having is the one conversation it is not in.
func TestResolveThreadFallsBackToTheWorkspaceThread(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("BERMUDA_THREAD", "")
	previous := currentWorkspace
	currentWorkspace = func(context.Context) (string, string) { return "w7", "Better Lingo" }
	t.Cleanup(func() { currentWorkspace = previous })

	got, err := resolveThread("")
	if err != nil {
		t.Fatalf("resolveThread: %v", err)
	}
	if got != "better-lingo" {
		t.Fatalf("resolved to %q, want the workspace's own thread", got)
	}

	// Twice, because every agent in the space races to write its first message
	// and each one calls this. A second thread here would split the workspace
	// into two conversations, which is the failure the whole change is against.
	again, err := resolveThread("")
	if err != nil || again != got {
		t.Fatalf("resolved to %q, %v on the second call, want %q both times", again, err, got)
	}

	// The flag and the environment still win: the workspace is a default, not an
	// override, and an agent that says where it is writing must be obeyed.
	t.Setenv("BERMUDA_THREAD", "tiktok-deals")
	if got, err := resolveThread(""); err != nil || got != "tiktok-deals" {
		t.Errorf("resolved to %q, %v, want $BERMUDA_THREAD to beat the workspace", got, err)
	}
	if got, err := resolveThread("webapp"); err != nil || got != "webapp" {
		t.Errorf("resolved to %q, %v, want --thread to beat the workspace", got, err)
	}
}

// A malformed id is refused rather than slugged, and that has to hold for the
// environment too: a shell exporting BERMUDA_THREAD="Better Lingo" should be
// told, not quietly given a second conversation nobody else is in.
func TestResolveThreadRefusesAMalformedID(t *testing.T) {
	noWorkspace(t)
	if _, err := resolveThread("Better Lingo"); err == nil {
		t.Error("--thread accepted an id that is not one")
	}
	t.Setenv("BERMUDA_THREAD", "Better Lingo")
	if _, err := resolveThread(""); err == nil {
		t.Error("$BERMUDA_THREAD accepted an id that is not one")
	}
}
