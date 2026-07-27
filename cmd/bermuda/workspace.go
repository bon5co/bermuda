package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/store"
)

// The thread an agent is in without being asked.
//
// Every agent runs in a herdr pane, and every pane belongs to a workspace. That
// is a membership list nobody has to maintain, so bermuda uses it: an agent that
// names no thread writes into the thread for the space it is working in, which
// is created the first time anything is said there.
//
// The alternative — agents creating and joining threads themselves — has a
// failure that does not announce itself. The one agent that forgets writes into
// global, is not in the conversation the others are having, and nothing anywhere
// says so. Everyone reads a thread that looks complete.

// workspaceLookupTimeout bounds the herdr calls this makes.
//
// It is on the path of every thread command that did not name a thread, so a
// wedged herdr must cost a post a moment and not a hang. Falling back to global
// is a worse answer than the workspace thread and a much better one than not
// returning.
const workspaceLookupTimeout = 2 * time.Second

// currentWorkspace is which herdr workspace this process is running in.
//
// It is a variable so tests can answer without a herdr socket; the real one
// returns empty for anything herdr did not start, which is the honest answer for
// cron, for a bare shell, and for a machine with no herdr at all.
var currentWorkspace = func(ctx context.Context) (id, label string) {
	pane := strings.TrimSpace(os.Getenv("HERDR_PANE_ID"))
	if pane == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, workspaceLookupTimeout)
	defer cancel()

	c := herdrcli.New()
	if c == nil {
		return "", ""
	}
	panes, err := c.PaneList(ctx, "")
	if err != nil {
		return "", ""
	}
	for _, p := range panes {
		if p.PaneID != pane {
			continue
		}
		id = p.WorkspaceID
		break
	}
	if id == "" {
		return "", ""
	}
	// The label is decoration: it names the thread once, at creation. Losing it
	// costs a readable id, not the thread, so its failure is ignored.
	if spaces, err := c.WorkspaceList(ctx); err == nil {
		for _, w := range spaces {
			if w.WorkspaceID == id {
				label = w.Label
				break
			}
		}
	}
	return id, label
}

// workspaceThread is the thread for the space this process is in, created if it
// does not exist yet, or empty if there is no space to speak of.
//
// Every failure returns empty rather than an error, and the caller falls back to
// global. A thread command that refused to run because herdr was unreachable
// would make the coordination tool the thing that stops work, which is the wrong
// way round: the record matters more than which conversation it lands in.
func workspaceThread(ctx context.Context) string {
	id, label := currentWorkspace(ctx)
	if id == "" {
		return ""
	}
	s, err := openStore()
	if err != nil {
		return ""
	}
	defer s.Close()
	t, err := s.EnsureWorkspaceThread(ctx, id, label)
	if err != nil {
		return ""
	}
	return t.ID
}

// workspaceLabels is every live workspace's label, by id, for the SPACE column.
//
// An empty map is a fine answer: herdr may not be running, and a thread list
// that falls back to workspace ids is better than one that refuses to print.
func workspaceLabels(ctx context.Context) map[string]string {
	out := map[string]string{}
	c := herdrcli.New()
	if c == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, workspaceLookupTimeout)
	defer cancel()
	spaces, err := c.WorkspaceList(ctx)
	if err != nil {
		return out
	}
	for _, w := range spaces {
		out[w.WorkspaceID] = w.Label
	}
	return out
}

// threadWorkspace is the workspace a thread belongs to, empty for one made by
// hand and for global.
//
// It is what bounds `@all`, so it is read from the thread rather than from the
// speaker: the question is which space the conversation is for, not which space
// the agent writing happens to sit in. Those differ whenever somebody passes
// --thread explicitly, and taking the speaker's workspace there would scope a
// broadcast to a space the thread has nothing to do with.
func threadWorkspace(ctx context.Context, s *store.Store, thread string) string {
	if s == nil || thread == "" || thread == store.GlobalThread {
		return ""
	}
	threads, err := s.Threads(ctx)
	if err != nil {
		return ""
	}
	for _, t := range threads {
		if t.ID == thread {
			return t.WorkspaceID
		}
	}
	return ""
}
