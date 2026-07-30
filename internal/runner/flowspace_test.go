package runner

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
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

// The label is what the thread id is slugged from, so it is checked here rather
// than being left to read well by luck: an opaque id would appear in every
// delivered mention and every `thread log` a human types.
func TestASpaceIsNamedAfterItsFlowAndRun(t *testing.T) {
	cases := []struct{ flow, run, want string }{
		{"triage", "20260730T101500Z-triage", "Flow triage 101500Z"},
		{"release-check", "20260730T004501Z-release-check", "Flow release-check 004501Z"},
		// A run id in some other shape gets no stamp rather than a guess: a wrong
		// timestamp is worse than a short label.
		{"triage", "manual-run", "Flow triage"},
	}
	for _, c := range cases {
		if got := SpaceLabel(c.flow, c.run); got != c.want {
			t.Errorf("SpaceLabel(%q, %q) = %q, want %q", c.flow, c.run, got, c.want)
		}
	}
	if got := store.SlugThread(SpaceLabel("triage", "20260730T101500Z-triage")); got != "flow-triage-101500z" {
		t.Errorf("thread id would be %q, want flow-triage-101500z", got)
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
