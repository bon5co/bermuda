package board

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// A flow is one row on the runs list, because it was launched once. What
// the row has to say is where the sequence got to, and what expanding it has to
// say is which step and for how long — the two questions asked about a flow
// that has not finished.

// seedFlowRun adds a four-step flow run, two steps done and the third
// running, and reloads the model through the same message the tick uses.
func seedFlowRun(t *testing.T, m *Model) {
	t.Helper()
	ctx := context.Background()
	started := time.Now().Add(-10 * time.Minute)
	if err := m.store.PutRun(ctx, store.Run{ID: "wf1", JobID: "alpha",
		Trigger: "manual", Outcome: "running", Note: "wrote five stories",
		StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	steps := []store.Step{
		{ID: "sync", Run: "true"},
		{ID: "author", Agent: "write"},
		{ID: "verify", Agent: "review"},
		{ID: "ship", Agent: "ship"},
	}
	if err := m.store.SeedRunSteps(ctx, "wf1", steps); err != nil {
		t.Fatal(err)
	}
	done := func(id string, idx int, secs int) store.RunStep {
		end := started.Add(time.Duration(secs) * time.Second)
		return store.RunStep{RunID: "wf1", Index: idx, StepID: id, Kind: "agent",
			Outcome: store.StepDone, Note: "ok", StartedAt: started, EndedAt: &end}
	}
	for _, rs := range []store.RunStep{
		done("sync", 0, 2),
		done("author", 1, 240),
		{RunID: "wf1", Index: 2, StepID: "verify", Kind: "agent",
			Outcome: store.StepRunning, StartedAt: started},
	} {
		if err := m.store.PutRunStep(ctx, rs); err != nil {
			t.Fatal(err)
		}
	}
	m.apply(t, m.load()())
}

// The row says how far the sequence got, not what the last agent said: the note
// belongs to one step, and the row is about the run.
func TestAFlowRunReadsAsItsProgress(t *testing.T) {
	m := newTestModel(t)
	seedFlowRun(t, m)
	m.focus = focusRuns

	out := m.renderRuns(0, len(m.visibleRuns()))
	if !strings.Contains(out, "2/4 · verify") {
		t.Errorf("the runs list does not show the flow's progress:\n%s", out)
	}
	if strings.Contains(out, "wrote five stories") {
		t.Errorf("the row shows one step's note instead of the run's progress:\n%s", out)
	}
	// An ordinary run is unaffected: it has no steps, and its note is the whole
	// story.
	if !strings.Contains(out, "all good") {
		t.Errorf("a one-agent run lost its note:\n%s", out)
	}
}

// Which step, and for how long, is asked about one run at a time. The steps are
// a block under the row rather than a column on every row.
func TestSpaceExpandsAFlowRunIntoItsSteps(t *testing.T) {
	m := newTestModel(t)
	seedFlowRun(t, m)
	m.focus, m.cursor = focusRuns, 0 // newest run first, which is the flow

	if r, ok := m.selectedRun(); !ok || r.ID != "wf1" {
		t.Fatalf("the cursor is on %+v, want the flow run", r)
	}
	if strings.Contains(m.renderRuns(0, len(m.visibleRuns())), "author") {
		t.Fatal("the steps are showing before anybody asked for them")
	}

	m.press(t, " ")
	out := m.renderRuns(0, len(m.visibleRuns()))
	for _, want := range []string{"sync", "author", "verify", "ship"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded run does not list step %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "4m0s") {
		t.Errorf("expanded run does not show how long a step took:\n%s", out)
	}
	if !strings.Contains(out, "pending") {
		t.Errorf("the step that has not started is missing, so 4 steps read as 3:\n%s", out)
	}

	m.press(t, " ")
	if strings.Contains(m.renderRuns(0, len(m.visibleRuns())), "author") {
		t.Error("space did not close the block again")
	}
}

// Space on an ordinary run does nothing at all: a run that is one agent has no
// steps to open, and a key that appeared to do something would be a lie.
func TestSpaceOnAnOrdinaryRunDoesNothing(t *testing.T) {
	m := newTestModel(t)
	seedFlowRun(t, m)
	m.focus, m.cursor = focusRuns, 1 // the seeded one-agent run

	before := m.renderRuns(0, len(m.visibleRuns()))
	m.press(t, " ")
	if len(m.expanded) != 0 {
		t.Errorf("a run with no steps was marked expanded: %v", m.expanded)
	}
	if m.renderRuns(0, len(m.visibleRuns())) != before {
		t.Error("the list changed when space was pressed on a run with no steps")
	}
}

// The progress cell is the one thing a reader takes from a flow row, so it
// has to be right at both ends of the run.
func TestStepProgressCountsAndNamesTheCurrentStep(t *testing.T) {
	steps := []store.RunStep{
		{StepID: "sync", Outcome: store.StepDone},
		{StepID: "author", Outcome: store.StepDone},
		{StepID: "verify", Outcome: store.StepParked, ParkReason: "no_result"},
		{StepID: "ship", Outcome: store.StepPending},
	}
	if got := stepProgress(steps); got != "2/4 · verify" {
		t.Errorf("progress reads %q, want 2/4 · verify", got)
	}
	// A finished flow names no step: there is nothing it is on.
	for i := range steps {
		steps[i].Outcome = store.StepDone
	}
	if got := stepProgress(steps); got != "4/4" {
		t.Errorf("a finished flow reads %q, want 4/4", got)
	}
	// A flow parked at its first step still says how many there were.
	if got := stepProgress([]store.RunStep{
		{StepID: "sync", Outcome: store.StepFailed},
		{StepID: "author", Outcome: store.StepPending},
	}); got != "0/2 · sync" {
		t.Errorf("progress reads %q, want 0/2 · sync", got)
	}
}
