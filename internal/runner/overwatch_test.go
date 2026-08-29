package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/flow"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// watcher is a fake agent that also answers as the overwatch: the decisions
// are consumed in order, so a test can say "retry, then park" and read what
// the run did with each.
type watcher struct {
	*fakeAgent
	decisions []Decision
	// raw, when set, is written instead of the decisions above -- for the
	// answers a real agent gives that a struct cannot express: no file at all,
	// or JSON that does not parse.
	raw []string
	// consults counts how many times the overwatch was asked.
	consults int
}

func newWatcher(results map[string]Result, decisions ...Decision) *watcher {
	return &watcher{fakeAgent: &fakeAgent{results: results}, decisions: decisions}
}

func (w *watcher) launch(ctx context.Context, job Job, runID, dir string) (*Run, error) {
	if job.Env["BERMUDA_STEP_ID"] != flow.OverwatchStepID {
		return w.fakeAgent.launch(ctx, job, runID, dir)
	}
	w.consults++
	// Recorded like any other launch, so a test can assert where in the
	// sequence the consult happened rather than only that it did.
	w.fakeAgent.calls = append(w.fakeAgent.calls,
		agentCall{stepID: flow.OverwatchStepID, runID: runID, dir: dir, job: job})
	switch {
	case len(w.raw) > 0:
		body := w.raw[0]
		w.raw = w.raw[1:]
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, DecisionFile), []byte(body), 0o600); err != nil {
				return nil, err
			}
		}
	case len(w.decisions) > 0:
		body, err := json.Marshal(w.decisions[0])
		if err != nil {
			return nil, err
		}
		w.decisions = w.decisions[1:]
		if err := os.WriteFile(filepath.Join(dir, DecisionFile), body, 0o600); err != nil {
			return nil, err
		}
	}
	if err := writeResult(dir, ok("decided")); err != nil {
		return nil, err
	}
	res, _ := readResult(dir)
	return &Run{JobID: job.ID, RunID: runID, RunDir: dir, Outcome: OutcomeDone, Result: res}, nil
}

// agentFlow is a flow of agent steps, so it is overseen without declaring
// anything -- which is the whole of "mandatory".
func agentFlow(steps ...store.Step) flow.Flow { return flowDef(steps...) }

// The case the feature exists for: a step failed, the harness would have parked
// and waited for a person, and the one reader that can see the whole run says
// what to do instead.
func TestTheOverwatchIsAskedWhereTheRunWouldHaveParked(t *testing.T) {
	dir := t.TempDir()
	agent := newWatcher(map[string]Result{
		"build": {Status: "error", Note: "the test binary was not there"},
	}, Decision{Decision: flow.DecidePark, Why: "the build never produced a binary; a person should look"})
	w := &Flow{Launch: agent.launch}

	wr, _ := w.Execute(context.Background(), flowJob(t),
		agentFlow(store.Step{ID: "build", Agent: "build it"}, store.Step{ID: "ship", Agent: "ship it"}),
		"", "run1", dir)

	if agent.consults != 1 {
		t.Fatalf("the overwatch was consulted %d time(s), want once", agent.consults)
	}
	if wr.Outcome != OutcomeParked || wr.StoppedAt != "build" {
		t.Errorf("run = %s at %q, want parked at build", wr.Outcome, wr.StoppedAt)
	}
	if len(wr.Decisions) != 1 || wr.Decisions[0].Decision != flow.DecidePark {
		t.Fatalf("decisions = %+v", wr.Decisions)
	}
	if !strings.Contains(wr.Decisions[0].Why, "never produced a binary") {
		t.Errorf("the overwatch's reasoning was not kept: %+v", wr.Decisions[0])
	}
	// And nothing downstream ran on the failure.
	if got := strings.Join(agent.started(), ","); strings.Contains(got, "ship") {
		t.Errorf("agents started for %q — ship ran on a failed build", got)
	}
}

// retry is for the transient: the same step, unchanged, one more time.
func TestRetryRunsTheSameStepAgain(t *testing.T) {
	dir := t.TempDir()
	agent := &watcher{
		fakeAgent: &fakeAgent{scripted: map[string][]Result{
			"fetch": {{Status: "error", Note: "connection reset"}, ok("got it")},
		}, results: map[string]Result{"use": ok("used it")}},
		decisions: []Decision{{Decision: flow.DecideRetry, Why: "a reset connection is transient"}},
	}
	w := &Flow{Launch: agent.launch}

	wr, err := w.Execute(context.Background(), flowJob(t),
		agentFlow(store.Step{ID: "fetch", Agent: "fetch"}, store.Step{ID: "use", Agent: "use"}),
		"", "run1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("run = %s/%s, want done after the retry succeeded", wr.Outcome, wr.ParkReason)
	}
	if got := strings.Join(agent.started(), ","); got != "fetch,overwatch,fetch,use" {
		t.Errorf("agents started for %q, want the step retried after the consult", got)
	}
	if wr.Loops != 1 {
		t.Errorf("Loops = %d, want the retry counted — a run that healed itself must not look like one that worked first time", wr.Loops)
	}
}

// goto is the declared backward edge, chosen at run time by the only reader
// that can see the fault is upstream of where it surfaced.
func TestGotoSendsTheRunBackAndReRunsTheStepsBetween(t *testing.T) {
	dir := t.TempDir()
	agent := &watcher{
		fakeAgent: &fakeAgent{scripted: map[string][]Result{
			"write":  {ok("v1"), ok("v2")},
			"verify": {{Status: "error", Note: "the fixture is wrong, not the code"}, ok("passes")},
		}},
		decisions: []Decision{{Decision: flow.DecideGoto, Step: "write", Why: "the fault is in what write produced"}},
	}
	w := &Flow{Launch: agent.launch}

	wr, err := w.Execute(context.Background(), flowJob(t),
		agentFlow(store.Step{ID: "write", Agent: "write"}, store.Step{ID: "verify", Agent: "verify"}),
		"", "run1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("run = %s/%s, want done", wr.Outcome, wr.ParkReason)
	}
	if got := strings.Join(agent.started(), ","); got != "write,verify,overwatch,write,verify" {
		t.Errorf("agents started for %q, want the run sent back to write", got)
	}
	// The rejected attempt is kept beside the one that replaced it, which is
	// what makes the loop readable afterwards.
	kept := filepath.Join(StepDir(dir, "verify"), "result.attempt-1.json")
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the rejected attempt was not kept: %v", err)
	}
}

// The whole guarantee of a flow is that B does not run on A's unverified
// claim. An agent able to wave a step through would end it, so a flow has to
// say so in the file before that decision is even offered.
func TestSkipIsRefusedUnlessTheFlowAllowsIt(t *testing.T) {
	def := agentFlow(
		store.Step{ID: "check", Agent: "check"},
		store.Step{ID: "ship", Agent: "ship"},
	)
	results := map[string]Result{"check": {Status: "error", Note: "two tests fail"}, "ship": ok("shipped")}

	dir := t.TempDir()
	agent := newWatcher(results, Decision{Decision: flow.DecideSkip, Why: "they are flaky"})
	wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)
	if wr.Outcome != OutcomeParked {
		t.Fatalf("run = %s, want parked: skip is not allowed by default", wr.Outcome)
	}
	if got := strings.Join(agent.started(), ","); strings.Contains(got, "ship") {
		t.Fatalf("agents started for %q — a step was waved through", got)
	}
	if !strings.Contains(wr.Decisions[0].Why, "does not allow") {
		t.Errorf("the record does not say why the decision was refused: %+v", wr.Decisions[0])
	}

	// The same decision, in a flow that declared it.
	dir = t.TempDir()
	allowed := def
	allowed.Overwatch = &flow.Overwatch{Allow: []string{flow.DecideSkip}}
	agent = newWatcher(results, Decision{Decision: flow.DecideSkip, Why: "they are flaky and tracked"})
	wr, _ = (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), allowed, "", "run2", dir)
	if wr.Outcome != OutcomeDone {
		t.Fatalf("run = %s/%s, want done: the flow allowed skip", wr.Outcome, wr.ParkReason)
	}
	if got := strings.Join(agent.started(), ","); got != "check,overwatch,ship" {
		t.Errorf("agents started for %q, want the run carried past the failed check", got)
	}
}

// Everything unreadable parks. Ambiguity stops a run; it never carries it on.
func TestAnUnusableAnswerParksAndSaysWhy(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		expect string
	}{
		{"no decision file at all", "", "left no decision"},
		{"not JSON", "the build looks fine to me", "not readable JSON"},
		{"a verb that does not exist", `{"decision":"fix-it-yourself"}`, "not a decision"},
		{"an empty decision", `{"decision":"","why":"unsure"}`, "no decision"},
		{"goto with no step", `{"decision":"goto"}`, "without naming a step"},
		{"goto to a step that does not exist", `{"decision":"goto","step":"nowhere"}`, "no step called"},
		{"goto forward", `{"decision":"goto","step":"ship"}`, "has not run yet"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			agent := &watcher{
				fakeAgent: &fakeAgent{results: map[string]Result{"check": {Status: "error", Note: "no"}}},
				raw:       []string{c.raw},
			}
			wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t),
				agentFlow(store.Step{ID: "check", Agent: "check"}, store.Step{ID: "ship", Agent: "ship"}),
				"", "run1", dir)

			if wr.Outcome != OutcomeParked {
				t.Fatalf("run = %s, want parked on an answer it could not use", wr.Outcome)
			}
			if got := strings.Join(agent.started(), ","); strings.Contains(got, "ship") {
				t.Fatalf("agents started for %q — the run carried on past an unusable answer", got)
			}
			if len(wr.Decisions) != 1 || !strings.Contains(wr.Decisions[0].Why, c.expect) {
				t.Errorf("the record does not say what was wrong with the answer: %+v", wr.Decisions)
			}
		})
	}
}

// A budget is what stops a confused overwatch spending the night deciding.
func TestTheOverwatchBudgetBoundsOneRun(t *testing.T) {
	dir := t.TempDir()
	agent := &watcher{
		fakeAgent: &fakeAgent{results: map[string]Result{"flaky": {Status: "error", Note: "still failing"}}},
		decisions: []Decision{
			{Decision: flow.DecideRetry}, {Decision: flow.DecideRetry},
			{Decision: flow.DecideRetry}, {Decision: flow.DecideRetry},
		},
	}
	def := agentFlow(store.Step{ID: "flaky", Agent: "try"})
	def.Overwatch = &flow.Overwatch{Budget: 2}

	wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)
	if wr.Outcome != OutcomeParked {
		t.Fatalf("run = %s, want parked once the budget ran out", wr.Outcome)
	}
	if agent.consults != 2 {
		t.Errorf("the overwatch ran %d times, want the declared budget of 2", agent.consults)
	}
	last := wr.Decisions[len(wr.Decisions)-1]
	if !strings.Contains(last.Why, "budget") {
		t.Errorf("the park does not say the budget ran out: %+v", last)
	}
}

// A resume must not hand back a budget the run already spent -- the thing that
// resumes a parked run is not always a human deciding it is worth another go.
func TestTheBudgetSurvivesAResume(t *testing.T) {
	dir := t.TempDir()
	def := agentFlow(store.Step{ID: "flaky", Agent: "try"})
	def.Overwatch = &flow.Overwatch{Budget: 1}
	fail := map[string]Result{"flaky": {Status: "error", Note: "still failing"}}

	first := &watcher{fakeAgent: &fakeAgent{results: fail},
		decisions: []Decision{{Decision: flow.DecideRetry}}}
	(&Flow{Launch: first.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)
	if first.consults != 1 {
		t.Fatalf("the first attempt consulted %d times, want 1", first.consults)
	}

	second := &watcher{fakeAgent: &fakeAgent{results: fail},
		decisions: []Decision{{Decision: flow.DecideRetry}}}
	wr, _ := (&Flow{Launch: second.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)
	if second.consults != 0 {
		t.Errorf("the resume consulted the overwatch %d time(s) on a spent budget", second.consults)
	}
	if wr.Outcome != OutcomeParked {
		t.Errorf("run = %s, want parked", wr.Outcome)
	}
}

// A declared on_fail edge is explicit, free, and the author's. An agent
// overruling it is the failure this tool exists to prevent -- so the edge runs
// first, and the overwatch is asked only once the edge has had its say.
func TestADeclaredEdgeIsTakenBeforeTheOverwatchIsAsked(t *testing.T) {
	dir := t.TempDir()
	agent := &watcher{
		fakeAgent: &fakeAgent{scripted: map[string][]Result{
			"write":  {ok("v1"), ok("v2")},
			"verify": {{Status: "error", Note: "wrong"}, ok("right")},
		}},
		decisions: []Decision{{Decision: flow.DecidePark}},
	}
	def := agentFlow(
		store.Step{ID: "write", Agent: "write"},
		store.Step{ID: "verify", Agent: "verify", OnFail: &store.OnFail{Goto: "write", MaxLoops: 2}},
	)
	wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)

	if agent.consults != 0 {
		t.Errorf("the overwatch was consulted %d time(s) while a declared edge still had loops", agent.consults)
	}
	if wr.Outcome != OutcomeDone {
		t.Errorf("run = %s/%s, want done via the declared edge", wr.Outcome, wr.ParkReason)
	}
}

// ...and once the edge has run out, that is exactly the moment worth a reader:
// "this loop is not converging" and "this loop was pointed at the wrong step"
// look identical from inside it.
func TestTheOverwatchIsAskedWhenADeclaredEdgeRunsOut(t *testing.T) {
	dir := t.TempDir()
	agent := &watcher{
		fakeAgent: &fakeAgent{scripted: map[string][]Result{
			"write":  {ok("v1"), ok("v2")},
			"verify": {{Status: "error", Note: "wrong"}, {Status: "error", Note: "still wrong"}},
		}},
		decisions: []Decision{{Decision: flow.DecidePark, Why: "the verifier is checking the wrong artifact"}},
	}
	def := agentFlow(
		store.Step{ID: "write", Agent: "write"},
		store.Step{ID: "verify", Agent: "verify", OnFail: &store.OnFail{Goto: "write", MaxLoops: 1}},
	)
	wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)

	if agent.consults != 1 {
		t.Fatalf("the overwatch was consulted %d time(s) when the edge ran out, want once", agent.consults)
	}
	if wr.ParkReason != ParkLoopExhausted {
		t.Errorf("ParkReason = %q, want the reason the edge ran out to survive the consult", wr.ParkReason)
	}
	if len(wr.Decisions) != 1 || !strings.Contains(wr.Decisions[0].Why, "wrong artifact") {
		t.Errorf("decisions = %+v", wr.Decisions)
	}
}

// every_step buys a reader on every result and costs an agent call per step,
// which is why a flow declares it rather than inheriting it.
func TestEveryStepConsultsOnSuccessAndCarriesOn(t *testing.T) {
	dir := t.TempDir()
	agent := newWatcher(map[string]Result{"one": ok("did one"), "two": ok("did two")},
		Decision{Decision: flow.DecideContinue}, Decision{Decision: flow.DecideContinue})
	def := agentFlow(store.Step{ID: "one", Agent: "a"}, store.Step{ID: "two", Agent: "b"})
	def.Overwatch = &flow.Overwatch{Watch: flow.WatchEveryStep}

	wr, err := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("run = %s/%s, want done", wr.Outcome, wr.ParkReason)
	}
	if agent.consults != 2 {
		t.Errorf("the overwatch was consulted %d time(s), want one per step", agent.consults)
	}
}

// The default cadence costs nothing on a run where nothing goes wrong, which
// is most runs.
func TestTheDefaultCadenceCostsNothingOnACleanRun(t *testing.T) {
	dir := t.TempDir()
	agent := newWatcher(map[string]Result{"one": ok("did one"), "two": ok("did two")})
	wr, err := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t),
		agentFlow(store.Step{ID: "one", Agent: "a"}, store.Step{ID: "two", Agent: "b"}),
		"", "run1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("run = %s", wr.Outcome)
	}
	if agent.consults != 0 {
		t.Errorf("the overwatch was consulted %d time(s) on a run where nothing went wrong", agent.consults)
	}
}

// A flow of shell steps has exit codes, not prose, and summoning a model to
// read `test -f gate` spends an agent on a boolean -- while making a
// deliberately deterministic flow depend on one. Before this rule, a run that
// used to park in milliseconds sat for ten minutes waiting on an agent.
func TestAShellOnlyFlowIsNotGivenAnAgentItNeverAskedFor(t *testing.T) {
	dir := t.TempDir()
	agent := newWatcher(nil, Decision{Decision: flow.DecideRetry})
	def := flowDef(store.Step{ID: "gate", Run: "exit 3"})

	wr, _ := (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)
	if agent.consults != 0 {
		t.Errorf("a shell-only flow started %d overwatch agent(s)", agent.consults)
	}
	if wr.Outcome != OutcomeParked || wr.ParkReason != ParkStepFailed {
		t.Errorf("run = %s/%s, want the park it would have taken anyway", wr.Outcome, wr.ParkReason)
	}

	// Declaring one puts it back: a shell flow that wants a reader has said so.
	dir = t.TempDir()
	agent = newWatcher(nil, Decision{Decision: flow.DecidePark, Why: "the gate file is genuinely missing"})
	def.Overwatch = &flow.Overwatch{}
	if _, _ = (&Flow{Launch: agent.launch}).Execute(context.Background(), flowJob(t), def, "", "run2", dir); agent.consults != 1 {
		t.Errorf("a shell flow that declared an overwatch was consulted %d time(s), want once", agent.consults)
	}
}

// A supervisor that can hang the run it supervises is worse than no
// supervisor, and a job's timeout defaults to waiting for ever.
func TestAConsultIsBoundedEvenWhenTheJobIsNot(t *testing.T) {
	if got := (flow.Overwatch{}).Wait(); got != flow.DefaultTimeout {
		t.Errorf("Wait = %s, want the default %s", got, flow.DefaultTimeout)
	}
	if got := (flow.Overwatch{Timeout: "90s"}).Wait(); got != 90*time.Second {
		t.Errorf("Wait = %s, want 90s", got)
	}

	dir := t.TempDir()
	blocked := func(ctx context.Context, job Job, runID, d string) (*Run, error) {
		if job.Env["BERMUDA_STEP_ID"] != flow.OverwatchStepID {
			if err := writeResult(d, Result{Status: "error", Note: "nope"}); err != nil {
				return nil, err
			}
			res, _ := readResult(d)
			return &Run{Outcome: OutcomeFailed, Result: res}, nil
		}
		<-ctx.Done() // an overwatch that never answers
		return nil, ctx.Err()
	}
	def := agentFlow(store.Step{ID: "check", Agent: "check"})
	def.Overwatch = &flow.Overwatch{Timeout: "50ms"}

	done := make(chan *FlowRun, 1)
	go func() {
		wr, _ := (&Flow{Launch: blocked}).Execute(context.Background(), flowJob(t), def, "", "run1", dir)
		done <- wr
	}()
	select {
	case wr := <-done:
		if wr.Outcome != OutcomeParked {
			t.Errorf("run = %s, want parked when the overwatch never answered", wr.Outcome)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the overwatch hung the run it was supervising")
	}
}
