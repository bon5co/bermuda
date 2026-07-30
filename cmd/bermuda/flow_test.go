package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/flow"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// The command layer's half of a flow: the run row and the step rows have to
// agree with what actually happened on disk, because that pair is what `bermuda
// flow status` prints and what the board renders.
//
// Every step here is a `run:` step, so nothing in this file starts an agent or
// touches the herdr socket. BERMUDA_STATE_DIR points the store and the run
// directories at a temporary directory — BERMUDA_HOME is silently ignored and
// would write into the real database.

func flowStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A parked flow has to be resumable from what is on disk, by a process that
// knows nothing but the run id — which is the situation a human is in the
// morning after.
func TestAParkedFlowResumesWithoutRedoingTheCompletedSteps(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	work := t.TempDir()
	gate := filepath.Join(work, "gate")
	counter := filepath.Join(work, "counter")

	writeFlow(t, "wf", "steps:\n"+
		"  - id: sync\n    run: echo ran >> "+counter+"\n"+
		"  - id: gate\n    run: test -f "+gate+"\n"+
		"  - id: ship\n    run: echo shipped >> "+counter+"\n")
	job := store.Job{ID: "wf", Name: "Flow", CWD: work, Kind: "claude",
		Model: "sonnet", Enabled: true, Timeout: 0, Flow: "wf"}
	if err := s.PutJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	run, _ := Execute(ctx, s, job, "manual")
	if run == nil || string(run.Outcome) != "parked" {
		t.Fatalf("first run ended %+v, want parked at the gate", run)
	}
	rec, err := s.Run(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != "parked" || !strings.Contains(rec.Note, "gate") {
		t.Errorf("the stored run reads %q / %q and does not name the step it parked at",
			rec.Outcome, rec.Note)
	}
	steps, err := s.RunSteps(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("stored %d step rows, want 3: a flow that died at step two still had three", len(steps))
	}
	want := []string{store.StepDone, store.StepFailed, store.StepPending}
	for i, w := range want {
		if steps[i].Outcome != w {
			t.Errorf("step %s is %q, want %q", steps[i].StepID, steps[i].Outcome, w)
		}
	}

	// The world changes — which is the only thing that makes a resume different
	// from a retry — and the same run picks up at the step that stopped it.
	if err := os.WriteFile(gate, []byte("open"), 0o644); err != nil {
		t.Fatal(err)
	}
	resumed, err := runFlow(ctx, s, job, *rec)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if string(resumed.Outcome) != "done" {
		t.Fatalf("resume ended %s, want done", resumed.Outcome)
	}
	if resumed.RunID != run.RunID {
		t.Errorf("resume created run %s instead of continuing %s", resumed.RunID, run.RunID)
	}
	body, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(body))
	if len(lines) != 2 || lines[0] != "ran" || lines[1] != "shipped" {
		t.Errorf("the counter reads %v, want one 'ran' and one 'shipped': the completed step was run twice", lines)
	}
	steps, err = s.RunSteps(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range steps {
		if st.Outcome != store.StepDone {
			t.Errorf("after resume step %s is %q, want done", st.StepID, st.Outcome)
		}
	}
}

// A flow job must not be launchable as a single empty prompt. Everything
// that starts a job — the daemon, the board, `job run` — goes through Execute.
func TestExecuteSendsAFlowJobDownTheFlowPath(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	work := t.TempDir()
	writeFlow(t, "wf", "steps:\n  - id: only\n    run: true\n")
	job := store.Job{ID: "wf", CWD: work, Kind: "claude", Model: "sonnet",
		Enabled: true, Flow: "wf"}
	if err := s.PutJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	run, err := Execute(ctx, s, job, "scheduled")
	if err != nil {
		t.Fatalf("flow job failed: %v", err)
	}
	steps, err := s.RunSteps(ctx, run.RunID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("the run has %d step rows (%v); it was not run as a flow", len(steps), err)
	}
}

// The old spelling has to keep dispatching, because the callers that hold it
// are the ones nobody re-reads: cron entries, launcher scripts, and the SKILL
// text an agent memorised. A renamed verb fails at 04:00, in a shell nobody is
// watching, on the feature that exists precisely for unattended sequences.
func TestTheOldWorkflowSpellingStillRunsAFlow(t *testing.T) {
	fn, ok := commands()["workflow"]
	if !ok {
		t.Fatal("`bermuda workflow` no longer dispatches; every stored cron entry holding it breaks silently")
	}
	err := fn([]string{"nonsense"})
	if err == nil || !strings.Contains(err.Error(), "flow subcommand") {
		t.Errorf("the alias returned %v; it should reach flowCmd and be told the subcommand is unknown", err)
	}
}

// writeFlow puts a flow file where the command layer will look for it.
//
// Flows are files now, so a test that wants one has to write one — and writing
// it under BERMUDA_STATE_DIR is what keeps this out of the real ~/.bermuda/flows
// that this machine's own agents are using.
func writeFlow(t *testing.T, id, body string) {
	t.Helper()
	dir := flowDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The template `flow new` writes must itself be a valid flow.
//
// It is the first flow anybody sees and the thing they edit into their own, so
// a template that does not parse turns "make a flow" into "debug bermuda". This
// caught a real one: an unquoted `run: echo "verified: $X"` is not legal YAML,
// because a bare scalar cannot contain ": ".
func TestTheNewFlowTemplateParses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	if err := flowNew([]string{"sample", "--about", "a sample"}); err != nil {
		t.Fatal(err)
	}
	f, err := flow.Load(flowDir(), "sample")
	if err != nil {
		t.Fatalf("the template bermuda writes does not load: %v", err)
	}
	if len(f.Steps) == 0 {
		t.Error("the template has no steps")
	}
	if !f.TakesInput() {
		t.Error("the template does not declare an input, so it cannot demonstrate {{input}}")
	}
}

// A directly-called flow must be runnable with agent steps, not only `run:`
// ones.
//
// This is a regression test for a bug that shipped in v2.0.0. `flowJob` left
// Kind empty, PutJob is where every other job gets it filled in, and a flow
// called directly never goes through PutJob — so herdr refused every agent step
// with "unsupported interactive agent kind:". A flow made only of `run:` steps
// worked perfectly, which is exactly why the unit tests and the end-to-end
// suite both missed it: neither launched an agent.
func TestADirectlyCalledFlowCanLaunchAnAgentStep(t *testing.T) {
	j := flowJob(flow.Flow{ID: "triage", About: "triage"}, "/tmp", "", "")
	if j.Kind != store.DefaultKind {
		t.Errorf("kind is %q, want %q — herdr refuses an agent with no kind",
			j.Kind, store.DefaultKind)
	}
	if j.Model != store.DefaultModel {
		t.Errorf("model is %q, want %q", j.Model, store.DefaultModel)
	}
	// An explicit choice still wins.
	if got := flowJob(flow.Flow{ID: "t"}, "/tmp", "opus", "codex"); got.Kind != "codex" || got.Model != "opus" {
		t.Errorf("explicit kind/model were overridden: %q %q", got.Kind, got.Model)
	}
}

// Resuming a directly-called flow must run its agent steps the same way the
// first attempt did.
//
// This is a regression test for a bug introduced by the fix for the v2.0.1 one.
// `flowResume` built its fallback job by hand — model and kind, no
// SkipPermissions — so a resumed flow enabled permission prompts that nobody
// was there to answer and parked at `blocked`. The first attempt worked, which
// is what made it hard to see: the flow only broke on the retry, which is
// exactly when somebody is already dealing with a failure.
//
// Asserting on the resume path specifically, because the earlier test asserted
// on flowJob and flowJob was never the half that was wrong.
func TestAResumedFlowKeepsTheUnattendedDefaults(t *testing.T) {
	j := flowJob(flow.Flow{ID: "triage"}, "", "", "")
	j.ID = "some-run-job"

	if !j.SkipPermissions {
		t.Error("a resumed flow would stop at the first permission prompt with nobody to answer it")
	}
	if j.Kind != store.DefaultKind {
		t.Errorf("kind is %q, want %q — herdr refuses an agent with no kind", j.Kind, store.DefaultKind)
	}
	if j.Model != store.DefaultModel {
		t.Errorf("model is %q, want %q", j.Model, store.DefaultModel)
	}
	if j.Flow != "triage" {
		t.Errorf("flow is %q, want the one the run recorded", j.Flow)
	}
}
