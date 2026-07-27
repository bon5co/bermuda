package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/internal/store"
)

// These tests are about the promise a workflow makes over one prompt that says
// "then do B, then do C": the steps happen in the declared order, and a step
// that cannot show it finished stops the ones after it. Every failure below is
// a workflow quietly doing four of five steps, which is the exact failure this
// feature exists to remove.
//
// No test here may start a real agent. The launcher is faked: a test of "what
// happens when the agent dies" that talked to the live herdr socket would start
// agents on this machine every time it ran.

// fakeAgent stands in for the runner's own launcher. It records what it was
// asked to start, writes whatever result.json the test dictates, and then
// classifies the step from that file exactly as the real runner does.
type fakeAgent struct {
	// results maps step id to the result its agent writes. A step with no entry
	// writes nothing, which is an agent that died.
	results map[string]Result
	calls   []agentCall
}

type agentCall struct {
	stepID string
	runID  string
	dir    string
	job    Job
}

func (f *fakeAgent) launch(_ context.Context, job Job, runID, dir string) (*Run, error) {
	stepID := job.Env["BERMUDA_STEP_ID"]
	f.calls = append(f.calls, agentCall{stepID: stepID, runID: runID, dir: dir, job: job})

	if res, ok := f.results[stepID]; ok {
		if err := writeResult(dir, res); err != nil {
			return nil, err
		}
	}
	run := &Run{JobID: job.ID, RunID: runID, RunDir: dir, AgentName: agentName(job.ID, runID)}
	res, err := readResult(dir)
	switch {
	case err != nil:
		run.Outcome, run.ParkReason = OutcomeParked, ParkNoResult
	case res.Status == "ok":
		run.Outcome, run.Result = OutcomeDone, res
	default:
		run.Outcome, run.Result = OutcomeFailed, res
	}
	return run, nil
}

func (f *fakeAgent) started() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.stepID)
	}
	return out
}

// ok is the result of an agent that did its work and said so.
func ok(note string) Result { return Result{Status: "ok", Note: note} }

// recorder collects the steps a workflow reported, in the order it reported
// them settling.
type recorder struct{ settled []string }

func (r *recorder) report(sr StepRun) {
	if sr.Outcome != OutcomeRunning {
		r.settled = append(r.settled, sr.ID+":"+string(sr.Outcome))
	}
}

func workflowJob(t *testing.T, steps ...store.Step) store.Job {
	t.Helper()
	return store.Job{ID: "wf", CWD: t.TempDir(), Kind: "claude",
		Model: "sonnet", Steps: steps}
}

// The order is the product. A workflow that ran its steps in any other order
// would be a slower version of handing one agent a list and hoping.
func TestStepsRunInTheOrderTheyAreDeclared(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{
		"author": ok("wrote"), "verify": ok("checked"),
	}}
	rec := &recorder{}
	w := &Workflow{Launch: agent.launch, Report: rec.report}

	job := workflowJob(t,
		store.Step{ID: "sync", Run: "true"},
		store.Step{ID: "author", Agent: "write the thing"},
		store.Step{ID: "verify", Agent: "check the thing"},
	)
	wr, err := w.Execute(context.Background(), job, "run1", dir)
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("outcome %s (%s), want done", wr.Outcome, wr.ParkReason)
	}
	want := []string{"sync:done", "author:done", "verify:done"}
	if strings.Join(rec.settled, ",") != strings.Join(want, ",") {
		t.Errorf("steps settled %v, want %v", rec.settled, want)
	}
	if got := strings.Join(agent.started(), ","); got != "author,verify" {
		t.Errorf("agents started for %q; the run step must not launch one", got)
	}
	if done, total := wr.Done(); done != 3 || total != 3 {
		t.Errorf("progress %d/%d, want 3/3", done, total)
	}
}

// The whole point: B must not run on A's unverified claim. A failing step parks
// the workflow where it failed, and everything after it stays unstarted.
func TestAFailingStepParksAndTheRestNeverStart(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{
		"author": ok("wrote"),
		"verify": {Status: "error", Note: "two stories are wrong"},
		"ship":   ok("shipped"),
	}}
	w := &Workflow{Launch: agent.launch}

	job := workflowJob(t,
		store.Step{ID: "author", Agent: "write"},
		store.Step{ID: "verify", Agent: "review"},
		store.Step{ID: "ship", Agent: "open a PR"},
	)
	wr, _ := w.Execute(context.Background(), job, "run1", dir)

	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkStepFailed {
		t.Fatalf("outcome %s/%s, want parked/step_failed", wr.Outcome, wr.ParkReason)
	}
	if wr.StoppedAt != "verify" {
		t.Errorf("parked at %q, want verify", wr.StoppedAt)
	}
	for _, c := range agent.calls {
		if c.stepID == "ship" {
			t.Fatal("ship ran after verify failed: a later step ran on a claim nobody could check")
		}
	}
	if _, err := os.Stat(StepDir(dir, "ship")); !os.IsNotExist(err) {
		t.Errorf("ship has a directory, so something started it: %v", err)
	}
	// A workflow that parked at step two of three is 1/3: reporting 1/2 would
	// present the steps it managed as the whole job.
	if done, total := wr.Done(); done != 1 || total != 3 {
		t.Errorf("progress %d/%d, want 1/3", done, total)
	}
	if !strings.Contains(wr.Note(), "1/3 steps, parked at verify") {
		t.Errorf("the run's note reads %q and does not say where it stopped", wr.Note())
	}
}

// An agent that exits without writing result.json has not been judged either
// way. Today a dead step is invisible and whatever came next proceeds on
// nothing, which is the failure this parks instead.
func TestAStepThatWritesNoResultParks(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	w := &Workflow{Launch: agent.launch}

	job := workflowJob(t,
		store.Step{ID: "author", Agent: "write"},
		store.Step{ID: "verify", Agent: "review"}, // writes nothing: the agent died
		store.Step{ID: "ship", Agent: "ship"},
	)
	wr, _ := w.Execute(context.Background(), job, "run1", dir)

	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkNoResult {
		t.Fatalf("outcome %s/%s, want parked/no_result", wr.Outcome, wr.ParkReason)
	}
	if wr.StoppedAt != "verify" {
		t.Errorf("parked at %q, want verify", wr.StoppedAt)
	}
	if got := strings.Join(agent.started(), ","); got != "author,verify" {
		t.Errorf("agents started for %q, want author,verify — ship must not have run", got)
	}
}

// Resume exists so that a failed workflow does not have to redo the expensive
// steps. If it re-ran them, the pressure to wave a failed step through — "just
// carry on" — would come straight back.
func TestResumeRestartsAtTheFailedStepAndNotBefore(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	job := workflowJob(t,
		store.Step{ID: "author", Agent: "write"},
		store.Step{ID: "verify", Agent: "review"},
		store.Step{ID: "ship", Agent: "ship"},
	)

	first := &fakeAgent{results: map[string]Result{
		"author": ok("wrote five"),
		"verify": {Status: "error", Note: "one is wrong"},
	}}
	w := &Workflow{Launch: first.launch}
	if wr, _ := w.Execute(ctx, job, "run1", dir); wr.StoppedAt != "verify" {
		t.Fatalf("first attempt parked at %q, want verify", wr.StoppedAt)
	}

	second := &fakeAgent{results: map[string]Result{
		"author": ok("rewrote everything"), // would overwrite, if it ran at all
		"verify": ok("fixed"),
		"ship":   ok("merged"),
	}}
	rec := &recorder{}
	w = &Workflow{Launch: second.launch, Report: rec.report}
	wr, err := w.Execute(ctx, job, "run1", dir)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("resume ended %s/%s, want done", wr.Outcome, wr.ParkReason)
	}
	if got := strings.Join(second.started(), ","); got != "verify,ship" {
		t.Errorf("resume started %q, want verify,ship: author was already done", got)
	}
	res, err := readResult(StepDir(dir, "author"))
	if err != nil {
		t.Fatalf("author's result vanished: %v", err)
	}
	if res.Note != "wrote five" {
		t.Errorf("author's result now reads %q: the completed step was re-run", res.Note)
	}
	// The completed step is not reported again either: its stored row already
	// holds how long that work took, and this attempt did not do it.
	for _, s := range rec.settled {
		if strings.HasPrefix(s, "author:") {
			t.Errorf("resume reported %q, which would overwrite the real duration with a skip", s)
		}
	}
	if !wr.Steps[0].Reused {
		t.Error("author is not marked reused, so nothing can tell it was skipped")
	}
}

// A retried step must be judged by what it does this time. Left in place, the
// previous attempt's result.json would classify an agent that died as though it
// had run and reported.
func TestARetriedStepDoesNotInheritTheOldVerdict(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	job := workflowJob(t, store.Step{ID: "verify", Agent: "review"})

	first := &fakeAgent{results: map[string]Result{
		"verify": {Status: "error", Note: "one is wrong"},
	}}
	if wr, _ := (&Workflow{Launch: first.launch}).Execute(ctx, job, "run1", dir); wr.ParkReason != ParkStepFailed {
		t.Fatalf("first attempt parked for %q, want step_failed", wr.ParkReason)
	}

	// This time the agent writes nothing at all.
	silent := &fakeAgent{results: map[string]Result{}}
	wr, _ := (&Workflow{Launch: silent.launch}).Execute(ctx, job, "run1", dir)
	if wr.ParkReason != ParkNoResult {
		t.Errorf("retry parked for %q, want no_result: it adopted the last attempt's file", wr.ParkReason)
	}
}

// Deterministic work belongs in the harness, not in a prompt. Most "the agent
// forgot" incidents are a shell command a model was asked to remember.
func TestARunStepNeedsNoAgent(t *testing.T) {
	dir := t.TempDir()
	job := workflowJob(t, store.Step{ID: "sync", Run: "echo pulled main"})

	// No launcher at all: if a run step reached for an agent this would park.
	wr, err := (&Workflow{}).Execute(context.Background(), job, "run1", dir)
	if err != nil {
		t.Fatalf("run step failed: %v", err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("outcome %s/%s, want done", wr.Outcome, wr.ParkReason)
	}
	res, err := readResult(StepDir(dir, "sync"))
	if err != nil || res.Status != "ok" {
		t.Fatalf("run step left result %+v, err %v: resume cannot tell it finished", res, err)
	}
	out, err := os.ReadFile(filepath.Join(StepDir(dir, "sync"), "output.txt"))
	if err != nil || !strings.Contains(string(out), "pulled main") {
		t.Errorf("output %q, err %v: a command's output is kept for the human", out, err)
	}
}

// A command that exits non-zero is a failed step, and a failed step parks the
// workflow like any other. Cheap deterministic checks are worth having only if
// they can stop the expensive steps behind them.
func TestAFailingRunStepStopsTheWorkflow(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	job := workflowJob(t,
		store.Step{ID: "sync", Run: "echo cannot pull >&2; exit 3"},
		store.Step{ID: "author", Agent: "write"},
	)
	wr, _ := (&Workflow{Launch: agent.launch}).Execute(context.Background(), job, "run1", dir)

	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkStepFailed {
		t.Fatalf("outcome %s/%s, want parked/step_failed", wr.Outcome, wr.ParkReason)
	}
	if len(agent.calls) != 0 {
		t.Errorf("an agent started after the setup step failed: %v", agent.started())
	}
	if !strings.Contains(wr.Steps[0].Note, "cannot pull") {
		t.Errorf("the step's note is %q and does not say what the command complained about", wr.Steps[0].Note)
	}
}

// Each agent step is its own process, and a step naming a different subagent is
// a different agent. Handing one agent's context to another charter would
// silently ignore one of the two settings.
func TestEachStepIsItsOwnAgentAndCarriesItsOwnConfig(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{
		"author": ok("wrote"), "verify": ok("checked"),
	}}
	job := workflowJob(t,
		store.Step{ID: "author", Agent: "write", Model: "opus", Effort: "high"},
		store.Step{ID: "verify", Agent: "review", Subagent: "cavecrew-reviewer"},
	)
	if wr, err := (&Workflow{Launch: agent.launch}).Execute(context.Background(), job, "run1", dir); err != nil || wr.Outcome != OutcomeDone {
		t.Fatalf("workflow ended %v (%v)", wr.Outcome, err)
	}
	if len(agent.calls) != 2 {
		t.Fatalf("started %d agents, want one per step", len(agent.calls))
	}
	a, v := agent.calls[0], agent.calls[1]
	if a.runID == v.runID {
		t.Errorf("both steps ran under run id %q, so they are the same agent", a.runID)
	}
	if a.dir == v.dir {
		t.Errorf("both steps wrote into %q, so one step's result would answer for the other", a.dir)
	}
	if a.job.Persistent || v.job.Persistent {
		t.Error("a step reused a persistent agent; each agent step is its own process")
	}
	args := strings.Join(a.job.AgentArgs, " ")
	if !strings.Contains(args, "--model opus") || !strings.Contains(args, "--effort high") {
		t.Errorf("author's argv is %q and does not carry its own model and effort", args)
	}
	if got := strings.Join(v.job.AgentArgs, " "); !strings.Contains(got, "--agent cavecrew-reviewer") {
		t.Errorf("verify's argv is %q and does not name its subagent", got)
	}
	// The step that named no model inherits the job's rather than the previous
	// step's: an override applies to the step that asked for it and no further.
	if got := strings.Join(v.job.AgentArgs, " "); !strings.Contains(got, "--model sonnet") {
		t.Errorf("verify's argv is %q; it should have inherited the job's sonnet, not author's opus", got)
	}
}

// A workflow validates its own steps before running any of them. A job stored
// before a rule existed must not run under the old rules.
func TestAWorkflowRefusesToStartOnInvalidSteps(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	job := workflowJob(t,
		store.Step{ID: "author", Agent: "write", Model: "haiku"},
	)
	wr, err := (&Workflow{Launch: agent.launch}).Execute(context.Background(), job, "run1", dir)
	if err == nil {
		t.Fatal("a haiku step ran; the floor is sonnet")
	}
	if wr.Outcome != OutcomeParked {
		t.Errorf("outcome %s, want parked", wr.Outcome)
	}
	if len(agent.calls) != 0 {
		t.Errorf("an agent started for an invalid workflow: %v", agent.started())
	}
}
