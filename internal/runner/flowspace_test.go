package runner

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// These tests are about what a step is told and where it is put. A flow's steps
// are separate agents on purpose, and the thread is the only channel between
// them that survives a step ending — so a step launched into the wrong space, or
// launched without being told the thread exists, is a step whose findings are
// lost with nothing reporting the loss.

// TestEveryStepLandsInTheFlowsSpace is the membership rule. A thread belongs to
// a space, so two steps in two spaces are two conversations however carefully
// each writes to "the thread".
func TestEveryStepLandsInTheFlowsSpace(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote"), "verify": ok("checked")}}
	w := &Flow{Launch: agent.launch, Space: &FlowSpace{WorkspaceID: "ws-7", Thread: "flow-wf-101500z"}}

	job, def := flowJob(t), flowDef(
		store.Step{ID: "author", Agent: "write the thing"},
		store.Step{ID: "verify", Agent: "check the thing"},
	)
	if _, err := w.Execute(context.Background(), job, def, "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	if len(agent.calls) != 2 {
		t.Fatalf("launched %d agents, want 2", len(agent.calls))
	}
	for _, c := range agent.calls {
		if c.job.WorkspaceID != "ws-7" {
			t.Errorf("step %s went to space %q, want ws-7", c.stepID, c.job.WorkspaceID)
		}
		if got := c.job.Env[EnvThread]; got != "flow-wf-101500z" {
			t.Errorf("step %s got %s=%q, want the flow's thread", c.stepID, EnvThread, got)
		}
	}
}

// A step is a fresh agent that has read nothing. A shared thread it is never
// told about is a thread it never writes to, so the instruction goes in every
// step's prompt — and it has to name the thread and a command that resolves,
// because "post your findings" with neither is advice rather than an
// instruction.
func TestEveryAgentStepIsToldToPostItsFindings(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	w := &Flow{Launch: agent.launch, Space: &FlowSpace{WorkspaceID: "ws-7", Thread: "flow-wf-101500z"}}

	job, def := flowJob(t), flowDef(store.Step{ID: "author", Agent: "write the thing"})
	if _, err := w.Execute(context.Background(), job, def, "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	prompt := agent.calls[0].job.Prompt
	if !strings.Contains(prompt, "write the thing") {
		t.Fatalf("the step's own prompt is gone: %q", prompt)
	}
	for _, want := range []string{"flow-wf-101500z", "thread post", "thread log"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt never mentions %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Errorf("prompt still holds a placeholder:\n%s", prompt)
	}
}

// A command step has no prompt to instruct, but the shell it runs in should
// reach the same conversation: a test runner that knows which suite is flaky is
// exactly the finding a later step would otherwise rediscover.
func TestACommandStepsShellIsInTheThread(t *testing.T) {
	dir := t.TempDir()
	var env []string
	w := &Flow{
		Space: &FlowSpace{WorkspaceID: "ws-7", Thread: "flow-wf-101500z"},
		Shell: func(_ context.Context, _, _ string, e []string) ([]byte, error) {
			env = e
			return []byte("ok"), nil
		},
	}
	job, def := flowJob(t), flowDef(store.Step{ID: "check", Run: "true"})
	if _, err := w.Execute(context.Background(), job, def, "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	if !hasEnv(env, EnvThread, "flow-wf-101500z") {
		t.Errorf("%s missing from the command step's environment", EnvThread)
	}
}

// No herdr, no space, no thread — and the flow still runs. The thread is how
// steps compare notes; it is not what makes them run in order, and a flow that
// refused to start because a window could not be opened would trade the feature
// for the convenience.
func TestAFlowWithNoSpaceStillRuns(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	w := &Flow{Launch: agent.launch} // Space is nil

	job, def := flowJob(t), flowDef(store.Step{ID: "author", Agent: "write the thing"})
	wr, err := w.Execute(context.Background(), job, def, "", "run1", dir)
	if err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("outcome %s (%s), want done", wr.Outcome, wr.ParkReason)
	}
	call := agent.calls[0]
	if call.job.WorkspaceID != "" {
		t.Errorf("step named space %q with no flow space; it must fall back to bermuda's own",
			call.job.WorkspaceID)
	}
	if _, ok := call.job.Env[EnvThread]; ok {
		t.Errorf("step was handed %s with no thread to write to", EnvThread)
	}
	if strings.Contains(call.job.Prompt, "thread post") {
		t.Errorf("step was told to post into a thread that does not exist:\n%s", call.job.Prompt)
	}
}

// A space herdr gave us without the store naming its thread is still worth
// using: the steps sit together, they just have nobody to tell. Telling them to
// post anyway would be an instruction that fails on every call.
func TestASpaceWithNoThreadStillGroupsTheSteps(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	w := &Flow{Launch: agent.launch, Space: &FlowSpace{WorkspaceID: "ws-7"}}

	job, def := flowJob(t), flowDef(store.Step{ID: "author", Agent: "write the thing"})
	if _, err := w.Execute(context.Background(), job, def, "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	call := agent.calls[0]
	if call.job.WorkspaceID != "ws-7" {
		t.Errorf("step went to space %q, want ws-7", call.job.WorkspaceID)
	}
	if strings.Contains(call.job.Prompt, "thread post") {
		t.Errorf("step told to post with no thread named:\n%s", call.job.Prompt)
	}
}

// A flow's name makes its spaces recognizable, while the random suffix keeps
// two runs of that flow from asking herdr for the same label. The suffix is
// checked by shape because making it predictable for a test would also make it
// predictable for concurrent runs.
func TestASpaceIsNamedAfterItsFlow(t *testing.T) {
	cases := []struct{ flow, prefix string }{
		{"triage", "FLOWS:triage:"},
		{" release-check ", "FLOWS:release-check:"},
		// Empty flow ids should remain recognizable rather than producing an
		// empty segment that looks like a formatting bug.
		{" \t", "FLOWS:flow:"},
	}
	for _, c := range cases {
		got := SpaceLabel(c.flow)
		want := regexp.MustCompile("^" + regexp.QuoteMeta(c.prefix) + `[A-Za-z0-9_-]{6}$`)
		if !want.MatchString(got) {
			t.Errorf("SpaceLabel(%q) = %q, want shape %s", c.flow, got, want)
		}
	}
	// Two runs of one flow must not ask herdr for the same name.
	if a, b := SpaceLabel("triage"), SpaceLabel("triage"); a == b {
		t.Errorf("two spaces for one flow got the same label %q", a)
	}
}

// The prompt tells the step to run bermuda. Bermuda is commonly run from a
// checkout and is not necessarily on any PATH, so the bare word is an
// instruction that fails silently — the class of failure flows exist to remove.
func TestTheThreadContractNamesARunnableBermuda(t *testing.T) {
	bin := BermudaBin()
	if bin == "bermuda" {
		t.Skip("this test binary's own path could not be resolved")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("BermudaBin() = %q, which is not there: %v", bin, err)
	}
	if !strings.Contains(ThreadContract("flow-wf-101500z"), bin) {
		t.Errorf("the thread contract does not name %q", bin)
	}
}

func hasEnv(env []string, key, want string) bool {
	for _, e := range env {
		if e == key+"="+want {
			return true
		}
	}
	return false
}

// The room an operator is looking at has to say why it is still open. A space
// named exactly as it was while running says only that something happened.
func TestASpaceLabelCarriesTheParkAndGivesItBack(t *testing.T) {
	label := SpaceLabel("triage")
	parked := LabelParked(label, "verify", ParkLoopExhausted)
	for _, want := range []string{label, "verify", "loop_exhausted"} {
		if !strings.Contains(parked, want) {
			t.Errorf("the parked label %q does not mention %q", parked, want)
		}
	}
	// A resume renames it back, and it has to be the same room: SpaceLabel's
	// suffix is random, so a name that could not be recovered would have to be
	// reinvented.
	if got := LabelBase(parked); got != label {
		t.Errorf("stripping the park gives %q, want the original %q", got, label)
	}
	if got := LabelBase(label); got != label {
		t.Errorf("a label that was never parked came back as %q", got)
	}
	// Parking twice must not stack verdicts.
	if got := LabelBase(LabelParked(parked, "build", ParkStepFailed)); got != label {
		t.Errorf("a second park left %q behind", got)
	}
}

// A park with nothing to say still has to be readable: the reason is missing
// exactly when the flow died in a way nobody classified.
func TestAParkedLabelWithoutAVerdictStillNamesTheRoom(t *testing.T) {
	label := SpaceLabel("triage")
	got := LabelParked(label, "", "")
	if !strings.HasPrefix(got, label) || !strings.Contains(got, "parked") {
		t.Errorf("bare parked label is %q", got)
	}
	if LabelBase(got) != label {
		t.Errorf("stripping gives %q, want %q", LabelBase(got), label)
	}
}
