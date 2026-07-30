package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bon5co/bermuda/internal/flow"
	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/runner"
	"github.com/bon5co/bermuda/internal/store"
)

// The space a flow run works in.
//
// A flow hands one line down its chain — the previous step's published note —
// and that is on purpose: giving B the transcript of A would make the reviewer
// the author in a different costume. But the line is not everything a step
// learned, and until now everything else died with the process. The step that
// discovers the migration already ran cannot tell the step that is about to run
// it again.
//
// So a run opens a space of its own and every step's tab is created in it. A
// thread belongs to a space, and everybody in the space is in the thread without
// joining it, which means the steps get a shared record for free: findings go in
// as they are found, and the next step reads them before it starts. The chain
// still carries the verdict. The thread carries the evidence.
//
// Every failure here is degradation, never refusal. No herdr, no space, a store
// that cannot name a thread — the flow runs exactly as it did before this
// existed, with one line on stderr. A coordination convenience that can stop the
// work is worse than no convenience.

// openFlowSpace resolves the space and thread this attempt's steps share,
// recording both on the run.
//
// A resume reuses the space it recorded, because the steps that already ran
// posted their findings in that thread and a resumed run that opened a fresh one
// would split a single run's record in two with nothing saying so. When the
// recorded space has gone — herdr restarted, somebody closed the window — a new
// one is made and the old thread is named on stderr, so the earlier half is
// still findable.
func openFlowSpace(ctx context.Context, s *store.Store, def flow.Flow, rec *store.Run, cwd string) *runner.FlowSpace {
	h := herdrcli.New()
	if h == nil {
		fmt.Fprintln(os.Stderr, "bermuda: no herdr, so this flow's steps get no shared "+
			"space or thread; they still run in order")
		return nil
	}

	if id := strings.TrimSpace(rec.Space); id != "" {
		if ws, err := h.WorkspaceGet(ctx, id); err == nil && ws != nil {
			space := &runner.FlowSpace{WorkspaceID: ws.WorkspaceID, Label: ws.Label,
				Thread: strings.TrimSpace(rec.Thread)}
			if space.Thread == "" {
				space.Thread = ensureSpaceThread(ctx, s, ws.WorkspaceID, ws.Label)
			}
			rec.Thread = space.Thread
			return space
		}
		fmt.Fprintf(os.Stderr, "bermuda: the space this run was using is gone; opening a "+
			"new one. What earlier steps posted is still readable: bermuda thread log "+
			"--thread %s\n", firstNonEmpty(rec.Thread, store.GlobalThread))
	}

	label := runner.SpaceLabel(def.ID, rec.ID)
	ws, _, err := h.WorkspaceCreate(ctx, label, cwd, nil)
	if err != nil || ws == nil {
		fmt.Fprintln(os.Stderr, "bermuda: could not open a space for this flow, so its steps "+
			"share no thread:", err)
		return nil
	}
	space := &runner.FlowSpace{WorkspaceID: ws.WorkspaceID, Label: firstNonEmpty(ws.Label, label)}
	space.Thread = ensureSpaceThread(ctx, s, space.WorkspaceID, space.Label)
	rec.Space, rec.Thread = space.WorkspaceID, space.Thread
	return space
}

// ensureSpaceThread names the space's thread, or returns empty having said why.
//
// Empty is a usable answer: the steps still share the space, they just have
// nowhere to write. Failing the flow over it would trade the work for the
// record-keeping.
func ensureSpaceThread(ctx context.Context, s *store.Store, workspaceID, label string) string {
	t, err := s.EnsureWorkspaceThread(ctx, workspaceID, label)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: this flow's space has no thread:", err)
		return ""
	}
	return t.ID
}

// briefFlowSpace opens the thread with what this run is, so the first step reads
// a briefing rather than an empty conversation.
//
// It also means the thread stands on its own afterwards. Read back a week later
// it says which flow ran, what it was called with, and what the steps were —
// none of which is recoverable from a wall of findings.
func briefFlowSpace(ctx context.Context, s *store.Store, space *runner.FlowSpace, def flow.Flow, rec store.Run, resumed bool) {
	if !space.HasThread() {
		return
	}
	verb := "flow"
	if resumed {
		verb = "resuming flow"
	}
	body := fmt.Sprintf("%s %s, run %s: %s", verb, def.ID, rec.ID, stepLine(def))
	if in := strings.TrimSpace(rec.Input); in != "" {
		body += fmt.Sprintf("\ncalled with: %s", in)
	}
	postToFlowThread(ctx, s, space, def, rec, body)
}

// closeFlowSpace retires the space when the flow finished, and leaves it alone
// when it did not.
//
// The rule is the one runs already follow for tabs: a clean finish reclaims what
// it opened, and anything parked stays on screen because a human has to look at
// it. Closing is not deleting — `bermuda thread log --thread <id>` reads every
// word of a closed thread — so what the steps found outlives the window they
// found it in.
func closeFlowSpace(ctx context.Context, s *store.Store, space *runner.FlowSpace, def flow.Flow, rec store.Run, wr *runner.FlowRun) {
	if !space.Usable() {
		return
	}
	if wr == nil || wr.Outcome != runner.OutcomeDone {
		if space.HasThread() {
			fmt.Fprintf(os.Stderr, "bermuda: this run's space is still open, and what its "+
				"steps found is in thread %s\n", space.Thread)
		}
		return
	}
	if space.HasThread() {
		postToFlowThread(ctx, s, space, def, rec, fmt.Sprintf("flow %s finished: %s", def.ID, wr.Note()))
		// Closed here rather than left to the daemon's sweep, so a machine running
		// no daemon does not accumulate open threads for spaces that have gone. A
		// refusal — something still holds a lease taken from this thread — is not an
		// error worth failing a finished run over, and the sweep will get it once
		// the resource comes back.
		if err := s.CloseThread(ctx, space.Thread, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "bermuda: leaving this flow's thread open:", err)
		}
	}
	h := herdrcli.New()
	if h == nil {
		return
	}
	if err := h.WorkspaceClose(ctx, space.WorkspaceID); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: this flow's space is finished with but still open:", err)
	}
}

// postToFlowThread writes one line into the flow's thread as the run itself.
//
// The author is the run, not a step: these are the harness's own messages, and
// attributing them to whichever step happened to be next would read as that
// agent having said something it did not.
func postToFlowThread(ctx context.Context, s *store.Store, space *runner.FlowSpace, def flow.Flow, rec store.Run, body string) {
	_, err := s.ThreadPost(ctx, store.ThreadMessage{
		Thread: space.Thread,
		Kind:   store.KindNote,
		By:     store.Identity{Name: firstNonEmpty(def.ID, rec.JobID, "flow"), JobID: rec.JobID, RunID: rec.ID},
		Body:   body,
	})
	if err != nil {
		// The flow is what matters; a missing opening note costs a reader context
		// and costs the run nothing.
		fmt.Fprintln(os.Stderr, "bermuda: could not write to this flow's thread:", err)
	}
}

// stepLine names the steps in order, for the opening note.
func stepLine(def flow.Flow) string {
	ids := make([]string, 0, len(def.Steps))
	for _, st := range def.Steps {
		ids = append(ids, st.ID)
	}
	return fmt.Sprintf("%d steps — %s", len(ids), strings.Join(ids, " then "))
}
