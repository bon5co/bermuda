package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/flow"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// These tests are about the promise a flow makes over one prompt that says
// "then do B, then do C": the steps happen in the declared order, and a step
// that cannot show it finished stops the ones after it. Every failure below is
// a flow quietly doing four of five steps, which is the exact failure this
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
	// scripted maps step id to one result per attempt, consumed in order and
	// falling back to results once it runs out. It is what a loopback test needs
	// and a linear one does not: a step whose answer changes between attempts is
	// the entire premise of a flow that heals itself.
	scripted map[string][]Result
	calls    []agentCall
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

	if queue, ok := f.scripted[stepID]; ok && len(queue) > 0 {
		if err := writeResult(dir, queue[0]); err != nil {
			return nil, err
		}
		f.scripted[stepID] = queue[1:]
	} else if res, ok := f.results[stepID]; ok {
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

// recorder collects the steps a flow reported, in the order it reported
// them settling.
type recorder struct{ settled []string }

func (r *recorder) report(sr StepRun) {
	if sr.Outcome != OutcomeRunning {
		r.settled = append(r.settled, sr.ID+":"+string(sr.Outcome))
	}
}

// flowJob and flowDef are the two halves a flow run now needs: the job supplies
// the configuration steps run under, and the flow supplies the steps.
func flowJob(t *testing.T) store.Job {
	t.Helper()
	return store.Job{ID: "wf", CWD: t.TempDir(), Kind: "claude",
		Model: "sonnet", Flow: "wf"}
}

func flowDef(steps ...store.Step) flow.Flow {
	return flow.Flow{ID: "wf", Steps: steps}
}

// The order is the product. A flow that ran its steps in any other order
// would be a slower version of handing one agent a list and hoping.
func TestStepsRunInTheOrderTheyAreDeclared(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{
		"author": ok("wrote"), "verify": ok("checked"),
	}}
	rec := &recorder{}
	w := &Flow{Launch: agent.launch, Report: rec.report}

	job, def := flowJob(t), flowDef(
		store.Step{ID: "sync", Run: "true"},
		store.Step{ID: "author", Agent: "write the thing"},
		store.Step{ID: "verify", Agent: "check the thing"},
	)
	wr, err := w.Execute(context.Background(), job, def, "", "run1", dir)
	if err != nil {
		t.Fatalf("flow failed: %v", err)
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
// the flow where it failed, and everything after it stays unstarted.
func TestAFailingStepParksAndTheRestNeverStart(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{
		"author": ok("wrote"),
		"verify": {Status: "error", Note: "two stories are wrong"},
		"ship":   ok("shipped"),
	}}
	w := &Flow{Launch: agent.launch}

	job, def := flowJob(t), flowDef(
		store.Step{ID: "author", Agent: "write"},
		store.Step{ID: "verify", Agent: "review"},
		store.Step{ID: "ship", Agent: "open a PR"},
	)
	wr, _ := w.Execute(context.Background(), job, def, "", "run1", dir)

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
	// A flow that parked at step two of three is 1/3: reporting 1/2 would
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
	w := &Flow{Launch: agent.launch}

	job, def := flowJob(t), flowDef(
		store.Step{ID: "author", Agent: "write"},
		store.Step{ID: "verify", Agent: "review"}, // writes nothing: the agent died
		store.Step{ID: "ship", Agent: "ship"},
	)
	wr, _ := w.Execute(context.Background(), job, def, "", "run1", dir)

	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkNoResult {
		t.Fatalf("outcome %s/%s, want parked/no_result", wr.Outcome, wr.ParkReason)
	}
	if wr.StoppedAt != "verify" {
		t.Errorf("parked at %q, want verify", wr.StoppedAt)
	}
	// The overwatch is consulted where the flow would park -- it is the last
	// agent in the list, and ship is still not in it.
	if got := strings.Join(agent.started(), ","); got != "author,verify,overwatch" {
		t.Errorf("agents started for %q, want author,verify,overwatch — ship must not have run", got)
	}
}

// Resume exists so that a failed flow does not have to redo the expensive
// steps. If it re-ran them, the pressure to wave a failed step through — "just
// carry on" — would come straight back.
func TestResumeRestartsAtTheFailedStepAndNotBefore(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	job, def := flowJob(t), flowDef(
		store.Step{ID: "author", Agent: "write"},
		store.Step{ID: "verify", Agent: "review"},
		store.Step{ID: "ship", Agent: "ship"},
	)

	first := &fakeAgent{results: map[string]Result{
		"author": ok("wrote five"),
		"verify": {Status: "error", Note: "one is wrong"},
	}}
	w := &Flow{Launch: first.launch}
	if wr, _ := w.Execute(ctx, job, def, "", "run1", dir); wr.StoppedAt != "verify" {
		t.Fatalf("first attempt parked at %q, want verify", wr.StoppedAt)
	}

	second := &fakeAgent{results: map[string]Result{
		"author": ok("rewrote everything"), // would overwrite, if it ran at all
		"verify": ok("fixed"),
		"ship":   ok("merged"),
	}}
	rec := &recorder{}
	w = &Flow{Launch: second.launch, Report: rec.report}
	wr, err := w.Execute(ctx, job, def, "", "run1", dir)
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
	job, def := flowJob(t), flowDef(store.Step{ID: "verify", Agent: "review"})

	first := &fakeAgent{results: map[string]Result{
		"verify": {Status: "error", Note: "one is wrong"},
	}}
	if wr, _ := (&Flow{Launch: first.launch}).Execute(ctx, job, def, "", "run1", dir); wr.ParkReason != ParkStepFailed {
		t.Fatalf("first attempt parked for %q, want step_failed", wr.ParkReason)
	}

	// This time the agent writes nothing at all.
	silent := &fakeAgent{results: map[string]Result{}}
	wr, _ := (&Flow{Launch: silent.launch}).Execute(ctx, job, def, "", "run1", dir)
	if wr.ParkReason != ParkNoResult {
		t.Errorf("retry parked for %q, want no_result: it adopted the last attempt's file", wr.ParkReason)
	}
}

// Deterministic work belongs in the harness, not in a prompt. Most "the agent
// forgot" incidents are a shell command a model was asked to remember.
func TestARunStepNeedsNoAgent(t *testing.T) {
	dir := t.TempDir()
	job, def := flowJob(t), flowDef(store.Step{ID: "sync", Run: "echo pulled main"})

	// No launcher at all: if a run step reached for an agent this would park.
	wr, err := (&Flow{}).Execute(context.Background(), job, def, "", "run1", dir)
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
// flow like any other. Cheap deterministic checks are worth having only if
// they can stop the expensive steps behind them.
func TestAFailingRunStepStopsTheFlow(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	job, def := flowJob(t), flowDef(
		store.Step{ID: "sync", Run: "echo cannot pull >&2; exit 3"},
		store.Step{ID: "author", Agent: "write"},
	)
	wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), job, def, "", "run1", dir)

	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkStepFailed {
		t.Fatalf("outcome %s/%s, want parked/step_failed", wr.Outcome, wr.ParkReason)
	}
	// The overwatch is consulted on the park and nothing else starts: the
	// author step must not run on a sync that failed.
	if got := strings.Join(agent.started(), ","); got != "overwatch" {
		t.Errorf("agents started for %q, want the overwatch alone — author must not have run", got)
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
	job, def := flowJob(t), flowDef(
		store.Step{ID: "author", Agent: "write", Model: "opus", Effort: "high"},
		store.Step{ID: "verify", Agent: "review", Subagent: "cavecrew-reviewer"},
	)
	if wr, err := (&Flow{Launch: agent.launch}).Execute(context.Background(), job, def, "", "run1", dir); err != nil || wr.Outcome != OutcomeDone {
		t.Fatalf("flow ended %v (%v)", wr.Outcome, err)
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

// A flow validates its own steps before running any of them. A job stored
// before a rule existed must not run under the old rules.
func TestAFlowRefusesToStartOnInvalidSteps(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	job, def := flowJob(t), flowDef(
		store.Step{ID: "author", Agent: "write", Model: "haiku"},
	)
	wr, err := (&Flow{Launch: agent.launch}).Execute(context.Background(), job, def, "", "run1", dir)
	if err == nil {
		t.Fatal("a haiku step ran; the floor is sonnet")
	}
	if wr.Outcome != OutcomeParked {
		t.Errorf("outcome %s, want parked", wr.Outcome)
	}
	if len(agent.calls) != 0 {
		t.Errorf("an agent started for an invalid flow: %v", agent.started())
	}
}

// The loopback. A flow whose last step is a reviewer can only ever park and
// wait for a human, and most of what a reviewer catches is fixable by the step
// that produced it. These tests are about that handback being bounded: it goes
// back to the maker, it re-runs the span, and it stops on its own.

// looping is the shape every test below uses: a maker, and a checker that sends
// the flow back to it.
func looping(max int) flow.Flow {
	return flowDef(
		store.Step{ID: "implement", Agent: "write the thing"},
		store.Step{ID: "verify", Agent: "review the diff",
			OnFail: &store.OnFail{Goto: "implement", MaxLoops: max}},
	)
}

// The edge points at the maker, not at the checker. A retry in place would
// re-read the same unchanged diff and reject it again.
func TestAFailedStepSendsTheFlowBackToTheStepThatCausedIt(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "the retry check is inverted"},
			ok("clean"),
		}},
	}
	wr, err := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), looping(2), "", "run1", dir)
	if err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("outcome %s/%s, want done: the second attempt passed", wr.Outcome, wr.ParkReason)
	}
	if got := strings.Join(agent.started(), ","); got != "implement,verify,implement,verify" {
		t.Errorf("steps ran %q, want implement,verify,implement,verify", got)
	}
	if wr.Loops != 1 {
		t.Errorf("run recorded %d loops, want 1", wr.Loops)
	}
	// Two steps were declared and two are done. Counting the four records would
	// report 4/2, which says the flow did more than it was asked rather than
	// that it did the same thing twice.
	if done, total := wr.Done(); done != 2 || total != 2 {
		t.Errorf("progress %d/%d, want 2/2", done, total)
	}
	if !strings.Contains(wr.Note(), "1 retry") {
		t.Errorf("the run's note reads %q and does not say it healed", wr.Note())
	}
}

// The retried maker is handed the verdict that rejected it. Without it the
// agent is re-run with the prompt that produced the rejected work and no idea
// it was rejected, which reliably produces the same work again.
func TestTheRetriedStepIsToldWhyItIsRunningAgain(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "the retry check is inverted"},
			ok("clean"),
		}},
	}
	if _, err := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), looping(2), "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}

	first, second := agent.calls[0].job.Prompt, agent.calls[2].job.Prompt
	if strings.Contains(first, "rejected") {
		t.Errorf("the first attempt's prompt mentions a rejection: %q", first)
	}
	for _, want := range []string{"attempt 2", `rejected by step "verify"`,
		"the retry check is inverted", "thread", "write the thing"} {
		if !strings.Contains(second, want) {
			t.Errorf("the retried prompt does not mention %q:\n%s", want, second)
		}
	}
	// Whatever the retry says, the step still runs as its own fresh agent.
	if agent.calls[0].runID == agent.calls[2].runID {
		t.Error("both attempts ran under one run id, so the retry inherited the rejected attempt's context")
	}
}

// Going back to the maker without clearing what came after it would re-run the
// maker and then hand the checker the same verdict it just rejected, produced
// by an attempt that never happened.
func TestTheWholeSpanRerunsNotJustTheTarget(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it"), "format": ok("formatted")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "unhandled nil"},
			ok("clean"),
		}},
	}
	def := flowDef(
		store.Step{ID: "implement", Agent: "write the thing"},
		store.Step{ID: "format", Agent: "tidy the diff"},
		store.Step{ID: "verify", Agent: "review the diff",
			OnFail: &store.OnFail{Goto: "implement", MaxLoops: 2}},
	)
	wr, err := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), def, "", "run1", dir)
	if err != nil || wr.Outcome != OutcomeDone {
		t.Fatalf("flow ended %s/%s (%v), want done", wr.Outcome, wr.ParkReason, err)
	}
	want := "implement,format,verify,implement,format,verify"
	if got := strings.Join(agent.started(), ","); got != want {
		t.Errorf("steps ran %q, want %q: the step between the target and the checker was skipped as already done", got, want)
	}
}

// An unattended heal loop that cannot converge is the expensive failure mode,
// so the edge is bounded and running out is a park like any other.
func TestALoopThatRunsOutOfAttemptsParks(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		// Different every time, so it is the count that stops this and not the
		// no-progress check.
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "first complaint"},
			{Status: "error", Note: "second complaint"},
			{Status: "error", Note: "third complaint"},
		}},
	}
	wr, err := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), looping(2), "", "run1", dir)
	if err != nil {
		t.Fatalf("a loop running out is a park, not an error: %v", err)
	}
	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkLoopExhausted {
		t.Fatalf("outcome %s/%s, want parked/loop_exhausted", wr.Outcome, wr.ParkReason)
	}
	if wr.StoppedAt != "verify" {
		t.Errorf("parked at %q, want verify", wr.StoppedAt)
	}
	if got := strings.Count(strings.Join(agent.started(), ","), "implement"); got != 3 {
		t.Errorf("the maker ran %d times, want 3: the first attempt and two loops", got)
	}
	if wr.Loops != 2 {
		t.Errorf("run recorded %d loops, want 2", wr.Loops)
	}
	if !strings.Contains(wr.Note(), "2 retries") || !strings.Contains(wr.Note(), "loop_exhausted") {
		t.Errorf("the run's note reads %q and does not say how it ended", wr.Note())
	}
}

// A verdict identical to the last one means the retry changed nothing this step
// can see. Spending the rest of the budget to be told the same thing again is
// how an overnight loop burns a night.
func TestAnIdenticalVerdictParksRatherThanLoopingAgain(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{
			"implement": ok("wrote it"),
			"verify":    {Status: "error", Note: "unhandled nil at line 40"},
		},
	}
	// Room for five loops, and it stops after one: the cap is not what saved it.
	wr, _ := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), looping(5), "", "run1", dir)

	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkLoopStuck {
		t.Fatalf("outcome %s/%s, want parked/loop_stuck", wr.Outcome, wr.ParkReason)
	}
	if got := strings.Join(agent.started(), ","); got != "implement,verify,implement,verify,overwatch" {
		t.Errorf("steps ran %q: it kept looping on a verdict that never moved", got)
	}
	if wr.Loops != 1 {
		t.Errorf("run recorded %d loops, want 1", wr.Loops)
	}
}

// Only a verdict loops. A step that died wrote no judgement at all, and
// rewriting code because the machine fell over is a heal loop against something
// that was never wrong with the code.
func TestAStepThatDiesParksInsteadOfLoopingBack(t *testing.T) {
	dir := t.TempDir()
	// verify has no scripted result and no result: its agent writes nothing.
	agent := &fakeAgent{results: map[string]Result{"implement": ok("wrote it")}}
	wr, _ := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), looping(3), "", "run1", dir)

	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkNoResult {
		t.Fatalf("outcome %s/%s, want parked/no_result", wr.Outcome, wr.ParkReason)
	}
	if got := strings.Join(agent.started(), ","); got != "implement,verify,overwatch" {
		t.Errorf("steps ran %q, want implement,verify,overwatch: a dead step sent the flow back", got)
	}
	if wr.Loops != 0 {
		t.Errorf("run recorded %d loops, want 0", wr.Loops)
	}
}

// A run step is a checker too — `go test` is the cheapest reviewer there is —
// and it reaches the maker the same way, through the environment rather than
// through a rewritten command.
func TestARunStepCanSendTheFlowBack(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"implement": ok("wrote it")}}
	def := flowDef(
		store.Step{ID: "implement", Agent: "write the thing"},
		// Fails while the marker file is absent, and the maker's second attempt
		// is what creates it: a checker that changes its mind for a reason.
		store.Step{ID: "test", Run: "test -f " + filepath.Join(dir, "fixed"),
			OnFail: &store.OnFail{Goto: "implement", MaxLoops: 2}},
	)
	launch := func(ctx context.Context, job Job, runID, stepDir string) (*Run, error) {
		if strings.Contains(job.Prompt, "rejected by step") {
			if err := os.WriteFile(filepath.Join(dir, "fixed"), []byte("x"), 0o644); err != nil {
				return nil, err
			}
		}
		return agent.launch(ctx, job, runID, stepDir)
	}
	wr, err := (&Flow{Launch: launch}).Execute(
		context.Background(), flowJob(t), def, "", "run1", dir)
	if err != nil || wr.Outcome != OutcomeDone {
		t.Fatalf("flow ended %s/%s (%v), want done", wr.Outcome, wr.ParkReason, err)
	}
	if wr.Loops != 1 {
		t.Errorf("run recorded %d loops, want 1", wr.Loops)
	}
}

// Which attempt a step is on is on the record, or the board shows a flow that
// looks hung while it silently rewrites for forty minutes.
func TestEachAttemptIsNumberedOnTheRecord(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "wrong"},
			ok("clean"),
		}},
	}
	rec := &recorder{}
	wr, _ := (&Flow{Launch: agent.launch, Report: rec.report}).Execute(
		context.Background(), flowJob(t), looping(2), "", "run1", dir)

	want := []string{"implement:done", "verify:failed", "implement:done", "verify:done"}
	if strings.Join(rec.settled, ",") != strings.Join(want, ",") {
		t.Errorf("steps settled %v, want %v: the rejected attempt is part of what happened", rec.settled, want)
	}
	if len(wr.Steps) != 4 {
		t.Fatalf("the run holds %d step records, want 4", len(wr.Steps))
	}
	for i, want := range []int{1, 1, 2, 2} {
		if wr.Steps[i].Attempt != want {
			t.Errorf("step %s record %d says attempt %d, want %d",
				wr.Steps[i].ID, i, wr.Steps[i].Attempt, want)
		}
	}
}

// A loop that parked has to be readable afterwards, and the question a human
// asks first is what the earlier attempts said. Deleting the rejected verdict
// leaves only the one that parked.
func TestARejectedVerdictIsKeptBesideTheAttemptThatReplacedIt(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "the retry check is inverted"},
			ok("clean"),
		}},
	}
	if _, err := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), looping(2), "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}

	kept, err := os.ReadFile(filepath.Join(StepDir(dir, "verify"), "result.attempt-1.json"))
	if err != nil {
		t.Fatalf("the rejected verdict is gone: %v", err)
	}
	if !strings.Contains(string(kept), "the retry check is inverted") {
		t.Errorf("the kept verdict reads %q", kept)
	}
	// And the file everything reads is the attempt that replaced it.
	res, err := readResult(StepDir(dir, "verify"))
	if err != nil || res.Note != "clean" {
		t.Errorf("result.json is %+v (%v), want the passing attempt", res, err)
	}
}

// Resume after a loop parked is resume like any other: the steps before the
// span are not redone, and the budget the human just refilled is a fresh one.
func TestResumeAfterALoopParkStartsAtTheStepThatParked(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	job, def := flowJob(t), flowDef(
		store.Step{ID: "pull", Run: "true"},
		store.Step{ID: "implement", Agent: "write the thing"},
		store.Step{ID: "verify", Agent: "review the diff",
			OnFail: &store.OnFail{Goto: "implement", MaxLoops: 1}},
	)

	first := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "first complaint"},
			{Status: "error", Note: "second complaint"},
		}},
	}
	wr, _ := (&Flow{Launch: first.launch}).Execute(ctx, job, def, "", "run1", dir)
	if wr.ParkReason != ParkLoopExhausted {
		t.Fatalf("first attempt parked for %q, want loop_exhausted", wr.ParkReason)
	}

	second := &fakeAgent{results: map[string]Result{
		"implement": ok("would overwrite, if it ran"),
		"verify":    ok("clean"),
	}}
	wr, err := (&Flow{Launch: second.launch}).Execute(ctx, job, def, "", "run1", dir)
	if err != nil || wr.Outcome != OutcomeDone {
		t.Fatalf("resume ended %s/%s (%v), want done", wr.Outcome, wr.ParkReason, err)
	}
	if got := strings.Join(second.started(), ","); got != "verify" {
		t.Errorf("resume started %q, want verify: the loop's last attempt at the maker was already done", got)
	}
	if res, _ := readResult(StepDir(dir, "implement")); res == nil || res.Note != "wrote it" {
		t.Errorf("the maker's result is %+v: a completed step was re-run", res)
	}
}

// The bound has to outlive the process that set it.
//
// The loop counters used to live in Execute, so every `flow resume` began with
// a full budget. Nothing has to be malicious for that to be unbounded: a
// self-heal job that unparks whatever broke overnight refills an exhausted loop
// every morning, and each refill costs a whole flow.
func TestAResumeDoesNotHandBackTheLoopsAlreadySpent(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	job, def := flowJob(t), looping(1)

	first := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "first complaint"},
			{Status: "error", Note: "second complaint"},
		}},
	}
	wr, _ := (&Flow{Launch: first.launch}).Execute(ctx, job, def, "", "run1", dir)
	if wr.ParkReason != ParkLoopExhausted {
		t.Fatalf("first attempt parked for %q, want loop_exhausted", wr.ParkReason)
	}

	// The resume's checker keeps failing with something new every time, so only
	// the ledger can stop it.
	second := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it again")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "third complaint"},
			{Status: "error", Note: "fourth complaint"},
		}},
	}
	wr, _ = (&Flow{Launch: second.launch}).Execute(ctx, job, def, "", "run1", dir)

	if wr.ParkReason != ParkLoopExhausted {
		t.Fatalf("resume parked for %q, want loop_exhausted", wr.ParkReason)
	}
	if wr.Loops != 0 {
		t.Errorf("the resume took %d more loops on a budget that was already spent", wr.Loops)
	}
	if got := strings.Join(second.started(), ","); got != "verify,overwatch" {
		t.Errorf("the resume ran %q; the maker was sent round again on a spent budget", got)
	}
}

// The human who fixed the thing the loop kept failing on gets the allowance
// back — by saying so, not by calling resume.
func TestResetLoopsHandsTheBudgetBack(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	job, def := flowJob(t), looping(1)

	first := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "first complaint"},
			{Status: "error", Note: "second complaint"},
		}},
	}
	if wr, _ := (&Flow{Launch: first.launch}).Execute(ctx, job, def, "", "run1", dir); wr.ParkReason != ParkLoopExhausted {
		t.Fatalf("first attempt parked for %q", wr.ParkReason)
	}

	second := &fakeAgent{
		results:  map[string]Result{"implement": ok("fixed it properly")},
		scripted: map[string][]Result{"verify": {{Status: "error", Note: "one last thing"}, ok("clean")}},
	}
	wr, err := (&Flow{Launch: second.launch, ResetLoops: true}).Execute(ctx, job, def, "", "run1", dir)
	if err != nil || wr.Outcome != OutcomeDone {
		t.Fatalf("reset resume ended %s/%s (%v), want done", wr.Outcome, wr.ParkReason, err)
	}
	if wr.Loops != 1 {
		t.Errorf("the reset resume took %d loops, want 1", wr.Loops)
	}
}

// The no-progress check has to survive a resume too, or the first attempt after
// one is spent rediscovering that the verdict has not moved.
func TestAResumeRemembersTheVerdictItLastRejectedOn(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	job, def := flowJob(t), looping(5)
	same := Result{Status: "error", Note: "unhandled nil at line 40"}

	first := &fakeAgent{results: map[string]Result{"implement": ok("wrote it"), "verify": same}}
	if wr, _ := (&Flow{Launch: first.launch}).Execute(ctx, job, def, "", "run1", dir); wr.ParkReason != ParkLoopStuck {
		t.Fatalf("first attempt parked for %q, want loop_stuck", wr.ParkReason)
	}

	second := &fakeAgent{results: map[string]Result{"implement": ok("wrote it"), "verify": same}}
	wr, _ := (&Flow{Launch: second.launch}).Execute(ctx, job, def, "", "run1", dir)
	if wr.ParkReason != ParkLoopStuck {
		t.Errorf("resume parked for %q, want loop_stuck", wr.ParkReason)
	}
	if wr.Loops != 0 {
		t.Errorf("the resume spent %d loops on a verdict it had already seen", wr.Loops)
	}
	if got := strings.Join(second.started(), ","); got != "verify,overwatch" {
		t.Errorf("the resume ran %q, want verify,overwatch alone", got)
	}
}

// The ledger is a file in the run directory, like everything else that has to
// outlive the process. A reader — or a human diagnosing a loop — can see what
// was spent without the database.
func TestTheLoopLedgerIsOnDiskWithTheRun(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{
		results: map[string]Result{"implement": ok("wrote it")},
		scripted: map[string][]Result{"verify": {
			{Status: "error", Note: "the retry check is inverted"}, ok("clean"),
		}},
	}
	if _, err := (&Flow{Launch: agent.launch}).Execute(
		context.Background(), flowJob(t), looping(2), "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	ledger := readLoopLedger(dir)
	if ledger.spent("verify") != 1 {
		t.Errorf("the ledger says verify spent %d, want 1", ledger.spent("verify"))
	}
	if ledger.verdict("verify") != "the retry check is inverted" {
		t.Errorf("the ledger kept verdict %q", ledger.verdict("verify"))
	}
	// A step that never looped is not in it, so the file says what happened
	// rather than listing every step with a zero.
	if ledger.spent("implement") != 0 {
		t.Errorf("the maker is charged %d loops it never took", ledger.spent("implement"))
	}
	// An unreadable ledger must not stop a flow: the loop is still bounded by
	// the no-progress check and by the step count.
	if err := os.WriteFile(filepath.Join(dir, "loops.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readLoopLedger(dir); got.spent("verify") != 0 {
		t.Errorf("a corrupt ledger read as %+v, want empty", got)
	}
}

// What a parked flow leaves in its own directory. The run half of this landed
// first and left the flow half wordless: `Execute` sets the park reason itself
// and never passes through `classify`, so every instrument that walks run
// directories saw a flow park as a prompt, a `steps/` tree and no explanation.

// flowErrNote reads back what bermuda wrote about a flow run.
func flowErrNote(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ErrFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func TestAParkedFlowSaysWhichStepStoppedItAndWhy(t *testing.T) {
	cases := []struct {
		name  string
		def   flow.Flow
		agent *fakeAgent
		want  string
	}{
		{
			name: "a step that reported failure",
			def: flowDef(
				store.Step{ID: "author", Agent: "write"},
				store.Step{ID: "verify", Agent: "review"},
			),
			agent: &fakeAgent{results: map[string]Result{
				"author": ok("wrote"),
				"verify": {Status: "error", Note: "two stories are wrong"},
			}},
			want: "the flow parked at step verify: the step reported failure",
		},
		{
			name: "an edge that ran out of loops",
			def:  looping(1),
			agent: &fakeAgent{
				results: map[string]Result{"implement": ok("wrote it")},
				scripted: map[string][]Result{"verify": {
					{Status: "error", Note: "first complaint"},
					{Status: "error", Note: "second complaint"},
				}},
			},
			want: "the flow parked at step verify: its on_fail edge used every loop it declared",
		},
		{
			name: "a retry that changed nothing",
			def:  looping(5),
			agent: &fakeAgent{results: map[string]Result{
				"implement": ok("wrote it"),
				"verify":    {Status: "error", Note: "unhandled nil at line 40"},
			}},
			want: "the flow parked at step verify: it failed twice running with the same verdict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			wr, _ := (&Flow{Launch: tc.agent.launch}).Execute(
				context.Background(), flowJob(t), tc.def, "", "run1", dir)
			if wr.Outcome != OutcomeParked {
				t.Fatalf("outcome %s, want parked", wr.Outcome)
			}
			if got := flowErrNote(t, dir); got != tc.want {
				t.Errorf("the run directory says %q, want %q", got, tc.want)
			}
		})
	}
}

// A step whose agent blocked on a human is the same fact about the same agent
// as a bare run's, so the flow repeats bermuda's own wording for it: the
// instruments match those sentences, and a park that reworded them would
// classify as `unknown` all over again.
func TestAFlowRepeatsTheAgentsOwnParkWording(t *testing.T) {
	dir := t.TempDir()
	blocked := func(_ context.Context, job Job, runID, stepDir string) (*Run, error) {
		return &Run{JobID: job.ID, RunID: runID, RunDir: stepDir,
			Outcome: OutcomeParked, ParkReason: ParkBlocked}, nil
	}
	wr, _ := (&Flow{Launch: blocked}).Execute(context.Background(), flowJob(t),
		flowDef(store.Step{ID: "author", Agent: "write"}), "", "run1", dir)

	if wr.ParkReason != ParkBlocked {
		t.Fatalf("park reason %s, want blocked", wr.ParkReason)
	}
	got := flowErrNote(t, dir)
	want := "the flow parked at step author: " + parkNote(ParkBlocked)
	if got != want {
		t.Errorf("the run directory says %q, want %q", got, want)
	}
}

// The two silent reasons, for the reason they are silent at run level: a step
// that wrote no result already says so in its ParkReason, and an unreadable
// result is a file any reader can open. Prose here would put words in the
// mouth of a run that observed nothing.
func TestAFlowWritesNothingForTheParksItOnlyInferred(t *testing.T) {
	dir := t.TempDir()
	// verify has no result: its agent died without writing one.
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t),
		flowDef(
			store.Step{ID: "author", Agent: "write"},
			store.Step{ID: "verify", Agent: "review"},
		), "", "run1", dir)

	if wr.ParkReason != ParkNoResult {
		t.Fatalf("park reason %s, want no_result", wr.ParkReason)
	}
	if got := flowErrNote(t, dir); got != "" {
		t.Errorf("the run directory says %q about a park bermuda only inferred", got)
	}
}

// A harness problem wins over the park that followed it, and it is written even
// when the flow never reached a step — the directory a step would have created
// does not exist yet, and that failure is exactly the one that must not be
// wordless.
func TestAFlowThatDiedOnItsOwnDefinitionSaysSo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run1")
	agent := &fakeAgent{}
	wr, err := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t),
		flowDef(store.Step{ID: "author", Agent: "write", Model: "haiku"}), "", "run1", dir)
	if err == nil {
		t.Fatal("a haiku step ran; the floor is sonnet")
	}
	if wr.Outcome != OutcomeParked {
		t.Fatalf("outcome %s, want parked", wr.Outcome)
	}
	if got := flowErrNote(t, dir); !strings.Contains(got, "haiku") {
		t.Errorf("the run directory says %q, which does not name what was wrong", got)
	}
}

// A flow that finished says nothing: a success with an error file beside it is
// a reader's problem, not a record.
func TestAFinishedFlowLeavesNoErrorNote(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	wr, err := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t),
		flowDef(store.Step{ID: "author", Agent: "write"}), "", "run1", dir)
	if err != nil || wr.Outcome != OutcomeDone {
		t.Fatalf("outcome %s: %v", wr.Outcome, err)
	}
	if got := flowErrNote(t, dir); got != "" {
		t.Errorf("a finished flow wrote %q", got)
	}
}
