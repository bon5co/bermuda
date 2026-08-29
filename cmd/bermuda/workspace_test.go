package main

import (
	"context"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// stubWorkspace answers "which space is this process in" without a herdr socket,
// and puts the real lookup back when the test ends.
func stubWorkspace(t *testing.T, id, label string) {
	t.Helper()
	prev := currentWorkspace
	currentWorkspace = func(context.Context) (string, string) { return id, label }
	t.Cleanup(func() { currentWorkspace = prev })
}

// An agent that names no thread must land in its workspace's thread. The
// failure this guards against is silent: falling back to global puts one agent
// outside the conversation the rest are having, and nothing says so.
func TestWorkspaceThreadIsCreatedOnFirstUse(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	stubWorkspace(t, "w1", "Better Lingo")
	ctx := context.Background()

	first := workspaceThread(ctx)
	if first == "" {
		t.Fatal("no thread for a workspace that exists; the agent would fall back to global")
	}
	if second := workspaceThread(ctx); second != first {
		t.Errorf("second call returned %q, want the same thread %q", second, first)
	}
}

// Two agents in different spaces must not share a thread, or a broadcast in one
// space reaches work it has nothing to do with.
func TestWorkspaceThreadsAreDistinctPerSpace(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	ctx := context.Background()

	stubWorkspace(t, "w1", "shared label")
	a := workspaceThread(ctx)
	stubWorkspace(t, "w2", "shared label")
	b := workspaceThread(ctx)

	if a == "" || b == "" {
		t.Fatalf("threads are %q and %q; both spaces need one", a, b)
	}
	if a == b {
		t.Errorf("both spaces got thread %q; the same label must not merge two workspaces", a)
	}
}

// No herdr, no pane, no space: the caller falls back to global rather than
// failing. A thread command that refused to run because herdr was unreachable
// would make the coordination tool the thing that stops work.
func TestNoWorkspaceMeansNoThreadRatherThanAnError(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	stubWorkspace(t, "", "")
	if got := workspaceThread(context.Background()); got != "" {
		t.Errorf("got thread %q with no workspace, want empty so the caller uses global", got)
	}
}

// @all is bounded by the thread's workspace, not the speaker's: those differ
// whenever somebody passes --thread explicitly.
func TestThreadWorkspaceComesFromTheThread(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	th, err := s.EnsureWorkspaceThread(ctx, "w1", "Better Lingo")
	if err != nil {
		t.Fatal(err)
	}
	hand, err := s.NewThread(ctx, "by-hand", "made without a workspace")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		thread string
		want   string
	}{
		{"workspace thread reports its space", th.ID, "w1"},
		{"hand-made thread belongs to no space", hand.ID, ""},
		{"global belongs to no space", store.GlobalThread, ""},
		{"empty thread name", "", ""},
		{"unknown thread", "no-such-thread", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := threadWorkspace(ctx, s, tc.thread); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A nil store must not panic the caller: workspace lookup is decoration on the
// path of every post, and it degrades to "no space" like every other failure.
func TestThreadWorkspaceWithNoStoreIsEmpty(t *testing.T) {
	if got := threadWorkspace(context.Background(), nil, "some-thread"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
