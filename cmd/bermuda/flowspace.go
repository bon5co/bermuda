package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v2/internal/flow"
	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/runner"
	"github.com/bon5co/bermuda/v2/internal/store"
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
			label := ws.Label
			if base := runner.LabelBase(label); base != label {
				// The room was renamed when this run parked. It is running
				// again, so the verdict comes off — a space that still says
				// "parked verify" while verify is running is a screen that
				// contradicts itself, and the label is what an operator trusts
				// first.
				if err := h.WorkspaceRename(ctx, ws.WorkspaceID, base); err != nil {
					fmt.Fprintln(os.Stderr, "bermuda: this space still carries its last "+
						"park in its name:", err)
				} else {
					label = base
				}
			}
			space := &runner.FlowSpace{WorkspaceID: ws.WorkspaceID, Label: label,
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

	label := runner.SpaceLabel(def.ID)
	ws, root, err := h.WorkspaceCreate(ctx, label, cwd, nil)
	if err != nil || ws == nil {
		fmt.Fprintln(os.Stderr, "bermuda: could not open a space for this flow, so its steps "+
			"share no thread:", err)
		return nil
	}
	// The root tab comes with the workspace whether anybody wants it or not, and
	// nobody does: every step opens its own tab and the park path opens one more.
	// Keeping its id is the whole cost of being able to close it later, and the
	// alternative — leaving it — is a dead shell in every flow room forever.
	space := &runner.FlowSpace{WorkspaceID: ws.WorkspaceID, Label: firstNonEmpty(ws.Label, label)}
	if root != nil {
		space.RootTabID = strings.TrimSpace(root.TabID)
	}
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
		parkFlowSpace(ctx, s, space, def, rec, wr)
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

// spaceKeeper is the part of herdr the park path uses.
//
// An interface rather than the client itself so a test can watch what a park
// does to a room without a live herdr: the whole point of this path is which
// calls it makes, and a test that made them for real would rename and open tabs
// in whatever window the machine running the suite happens to have.
type spaceKeeper interface {
	PaneList(ctx context.Context, workspaceID string) ([]herdrcli.Pane, error)
	TabCreate(ctx context.Context, workspaceID, label, cwd string, env map[string]string) (*herdrcli.Pane, error)
	TabClose(ctx context.Context, tabID string) error
	WorkspaceRename(ctx context.Context, workspaceID, label string) error
}

// keeper returns the herdr client, or nil when there is none. A variable so a
// test can substitute one.
var keeper = func() spaceKeeper {
	h := herdrcli.New()
	if h == nil {
		return nil
	}
	return h
}

// parkFlowSpace leaves the room in a state an operator can act on.
//
// A parked run keeps its space, because a human has to look at it. That was
// half the mechanism: the space stayed open and nothing in it said why, so a
// flow of `run:` steps left an open room with a blank shell in it and the
// verdict only in the database. The record is written where somebody standing
// in the room can read it — the thread gets the ending, the label gets the
// verdict, and one tab lands on the artifacts.
//
// The space is not closed here and never will be: closing it is how the
// operator acknowledges the run, which is a thing only they can do. Herdr
// refuses to close the last tab in a workspace, so the room outlives every tab
// in it and the ack is unambiguous.
func parkFlowSpace(ctx context.Context, s *store.Store, space *runner.FlowSpace,
	def flow.Flow, rec store.Run, wr *runner.FlowRun) {

	note, stoppedAt, reason := "the run did not finish", "", runner.ParkReason("")
	if wr != nil {
		note, stoppedAt, reason = wr.Note(), wr.StoppedAt, wr.ParkReason
	}
	if space.HasThread() {
		// The ending, in the conversation that holds everything else about this
		// run. A thread that stops mid-sentence is the shape a reader has to
		// leave to interpret; the resume command is here because this is where
		// somebody is when they want it.
		//
		// The thread stays open, unlike a finished run's: a resume writes into
		// it, and closing it now would split one run's record at its least
		// useful moment.
		postToFlowThread(ctx, s, space, def, rec,
			fmt.Sprintf("flow %s parked: %s — resume with: bermuda flow resume %s",
				def.ID, note, rec.ID))
	}

	h := keeper()
	if h == nil {
		return
	}
	if label := strings.TrimSpace(space.Label); label != "" {
		if err := h.WorkspaceRename(ctx, space.WorkspaceID,
			runner.LabelParked(label, stoppedAt, reason)); err != nil {
			fmt.Fprintln(os.Stderr, "bermuda: this space keeps its running name:", err)
		}
	}
	// A tab sitting on the run directory, named for the verdict. An agent step
	// that failed already left its own tab and that is the better thing to read;
	// this is for the case there is none — a `run:` step has no agent and no
	// pane, so without it the room is a blank shell. It costs one tab in the
	// case where both exist, which is cheaper than the case where neither does.
	//
	// One per room, not one per park: a run that is resumed and parks again is
	// the ordinary case for a flow that needs a human, and a tab per attempt
	// turns the room an operator opened for the verdict into a pile of
	// identical shells. The one already sitting in the run directory is that
	// tab — no step's tab is there, because a step runs in the job's working
	// directory.
	if dir := strings.TrimSpace(rec.RunDir); dir != "" && !hasPaneIn(ctx, h, space.WorkspaceID, dir) {
		env := map[string]string{"BERMUDA_RUN_ID": rec.ID, "BERMUDA_RUN_DIR": dir}
		if space.HasThread() {
			env[runner.EnvThread] = space.Thread
		}
		if _, err := h.TabCreate(ctx, space.WorkspaceID, parkedTabLabel(stoppedAt, reason), dir, env); err != nil {
			fmt.Fprintln(os.Stderr, "bermuda: no tab for this run's evidence:", err)
		}
	}
	reapRootTab(ctx, h, space)
	fmt.Fprintf(os.Stderr, "bermuda: this run parked and its space is still open — "+
		"close the space to acknowledge it.\n  bermuda flow status %s\n", rec.ID)
	if space.HasThread() {
		fmt.Fprintf(os.Stderr, "  bermuda thread log --thread %s\n", space.Thread)
	}
}

// reapRootTab closes the empty tab herdr made along with the workspace, now that
// the room holds one somebody will actually read.
//
// It runs at park because that is the moment the room stops being a workspace a
// run is using and becomes a screen an operator opens: the label carries the
// verdict, one tab sits on the artifacts, and the shell that has been empty
// since the space was created is the only thing left that means nothing. A run
// that finishes never gets here — closeFlowSpace takes the whole workspace.
//
// The check that some other tab exists is explicit rather than delegated to
// herdr's own refusal to close the last tab in a workspace. Both end with the
// room intact, but leaning on the refusal turns the ordinary case — a `run:`
// step that parked before anything opened a second tab — into an error line
// about a failed close, which trains a reader to ignore the one line that would
// matter if the close failed for a real reason.
//
// A herdr that cannot list panes is treated as "do not touch it": the cost of
// that guess is the spare tab we already have, and the cost of the other guess
// is an empty room.
func reapRootTab(ctx context.Context, h spaceKeeper, space *runner.FlowSpace) {
	root := strings.TrimSpace(space.RootTabID)
	if root == "" {
		// Either herdr never told us which tab it made, or this is a resumed run
		// whose root tab the first attempt already reaped.
		return
	}
	panes, err := h.PaneList(ctx, space.WorkspaceID)
	if err != nil {
		return
	}
	occupied := false
	for _, p := range panes {
		if strings.TrimSpace(p.TabID) != root {
			occupied = true
			break
		}
	}
	if !occupied {
		return
	}
	if err := h.TabClose(ctx, root); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: this space keeps the empty tab it was opened with:", err)
		return
	}
	// One reap per space: saying it is gone stops a second park from asking herdr
	// to close a tab that no longer exists.
	space.RootTabID = ""
}

// hasPaneIn reports whether the space already holds a shell sitting in this
// directory.
//
// A herdr that cannot answer is treated as "no": the cost of the wrong guess is
// one spare tab, and the cost of the other wrong guess is a parked run with
// nothing in its room.
func hasPaneIn(ctx context.Context, h spaceKeeper, workspaceID, dir string) bool {
	panes, err := h.PaneList(ctx, workspaceID)
	if err != nil {
		return false
	}
	for _, p := range panes {
		if p.CWD == dir {
			return true
		}
	}
	return false
}

// parkedTabLabel names the tab an operator lands in.
func parkedTabLabel(stoppedAt string, reason runner.ParkReason) string {
	label := "parked"
	if stoppedAt != "" {
		label += ": " + stoppedAt
	}
	if reason != "" {
		label += " (" + string(reason) + ")"
	}
	return label
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
