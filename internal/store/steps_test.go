package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Validation is where a flow's two house rules live: no step may run on
// haiku, and no two steps may share an id. Both are cheap to state and
// expensive to discover at 04:00, which is when an unattended flow runs.

func newSteps(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The floor is sonnet. A flow is exactly where a cheap model quietly does
// four of five steps and reports success.
func TestValidationRejectsHaikuWhereverItIsNamed(t *testing.T) {
	cases := []struct {
		name     string
		steps    []Step
		jobModel string
	}{
		{"named on the step", []Step{{ID: "a", Agent: "go", Model: "haiku"}}, "sonnet"},
		{"a dated spelling", []Step{{ID: "a", Agent: "go", Model: "claude-3-5-haiku-latest"}}, "sonnet"},
		// Inherited counts: a step that names no model runs on the job's, so
		// validating only what the step spells out would wave this through.
		{"inherited from the job", []Step{{ID: "a", Agent: "go"}}, "haiku"},
	}
	for _, c := range cases {
		err := ValidateSteps(c.steps, c.jobModel)
		if err == nil {
			t.Errorf("%s: accepted, and the step would run on haiku", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "sonnet") {
			t.Errorf("%s: refusal %q does not say what the floor is", c.name, err)
		}
	}
	// A run step has no model to be wrong about.
	if err := ValidateSteps([]Step{{ID: "a", Run: "true"}}, "haiku"); err != nil {
		t.Errorf("a command step was refused for the job's model: %v", err)
	}
}

// Step ids name directories, agents, and what resume restarts at. Two steps
// sharing one would make a resume skip work that never ran.
func TestValidationRejectsADuplicateStepID(t *testing.T) {
	err := ValidateSteps([]Step{
		{ID: "verify", Agent: "check"},
		{ID: "verify", Agent: "check again"},
	}, "sonnet")
	if err == nil {
		t.Fatal("two steps called verify were accepted; the second would resume as though the first had run it")
	}
	// Case is not a difference either: the id is a directory name, and one
	// filesystem in the chain will not care about case.
	if err := ValidateSteps([]Step{
		{ID: "verify", Agent: "a"}, {ID: "Verify", Agent: "b"},
	}, "sonnet"); err == nil {
		t.Error("verify and Verify were accepted as two steps; they share a directory")
	}
}

// A step is a prompt or a command. Anything else is a step whose configuration
// is partly ignored, which looks configured and is not.
func TestValidationRejectsMalformedSteps(t *testing.T) {
	cases := []struct {
		name string
		step Step
	}{
		{"neither agent nor run", Step{ID: "a"}},
		{"both agent and run", Step{ID: "a", Agent: "go", Run: "true"}},
		{"no id", Step{Agent: "go"}},
		{"a path in the id", Step{ID: "../escape", Agent: "go"}},
		{"a model on a run step", Step{ID: "a", Run: "true", Model: "opus"}},
		{"a subagent on a run step", Step{ID: "a", Run: "true", Subagent: "cto"}},
		{"an effort on a run step", Step{ID: "a", Run: "true", Effort: "high"}},
	}
	for _, c := range cases {
		if err := ValidateSteps([]Step{c.step}, "sonnet"); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// A run step needs nothing but a command: no agent, no model, no kind.
func TestARunStepNeedsNothingElse(t *testing.T) {
	if err := ValidateSteps([]Step{{ID: "sync", Run: "wt.sh branch ."}}, "sonnet"); err != nil {
		t.Fatalf("a plain command step was refused: %v", err)
	}
}

// The store is the last gate. Anything can call PutJob — the board's editor,
// the CLI, a future importer — and none of them should be able to save a
// flow the runner would then refuse at 04:00.
func TestTheStoreRefusesToSaveAnInvalidFlow(t *testing.T) {
	s := newSteps(t)
	ctx := context.Background()
	j := Job{ID: "wf", Model: "sonnet", CWD: "/tmp", Enabled: true, Steps: []Step{
		{ID: "author", Agent: "write", Model: "haiku"},
	}}
	if err := s.PutJob(ctx, j); err == nil {
		t.Fatal("a haiku flow was stored; it would only be caught when it ran")
	}
	if _, err := s.Job(ctx, "wf"); err == nil {
		t.Error("the refused job was written anyway")
	}
}

// Steps have to survive the round trip intact, in order: the order is the
// feature.
func TestStepsSurviveTheRoundTrip(t *testing.T) {
	s := newSteps(t)
	ctx := context.Background()
	steps := []Step{
		{ID: "sync", Run: "git pull"},
		{ID: "author", Agent: "write five", Model: "opus", Effort: "high"},
		{ID: "verify", Agent: "review", Subagent: "cavecrew-reviewer", Kind: "claude"},
	}
	if err := s.PutJob(ctx, Job{ID: "wf", Model: "sonnet", CWD: "/tmp",
		Enabled: true, Steps: steps}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, "wf")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsFlow() || len(got.Steps) != 3 {
		t.Fatalf("read back %d steps, want 3", len(got.Steps))
	}
	for i, want := range steps {
		if got.Steps[i] != want {
			t.Errorf("step %d read back as %+v, want %+v", i, got.Steps[i], want)
		}
	}
	// A plain job stays plain: nothing about flows may turn an ordinary
	// one-prompt job into a zero-step flow.
	if err := s.PutJob(ctx, Job{ID: "plain", Prompt: "do it", Model: "sonnet",
		CWD: "/tmp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	plain, err := s.Job(ctx, "plain")
	if err != nil {
		t.Fatal(err)
	}
	if plain.IsFlow() {
		t.Errorf("a prompt-only job reads back as a flow with %d steps", len(plain.Steps))
	}
}

// The rows exist from the start of the run, so the board can say "2 of 4"
// rather than counting only the steps that got far enough to report — and a
// flow that died at step one still says it had four.
func TestSeedRunStepsDeclaresEveryStepAndKeepsWhatRan(t *testing.T) {
	s := newSteps(t)
	ctx := context.Background()
	steps := []Step{{ID: "sync", Run: "true"}, {ID: "author", Agent: "write"},
		{ID: "verify", Agent: "review"}}

	if err := s.SeedRunSteps(ctx, "run1", steps); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunSteps(ctx, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("seeded %d rows, want 3", len(got))
	}
	for i, want := range []string{"sync", "author", "verify"} {
		if got[i].StepID != want {
			t.Errorf("row %d is %q, want %q: the order is the flow", i, got[i].StepID, want)
		}
		if got[i].Outcome != StepPending {
			t.Errorf("%s starts as %q, want pending", got[i].StepID, got[i].Outcome)
		}
	}

	started := time.Now().Add(-90 * time.Second)
	ended := started.Add(80 * time.Second)
	if err := s.PutRunStep(ctx, RunStep{RunID: "run1", Index: 0, StepID: "sync",
		Kind: "run", Outcome: StepDone, Note: "pulled", StartedAt: started,
		EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	// Seeding again is what a resume does. It must not reset the step that
	// already ran: that row holds how long the expensive work took.
	if err := s.SeedRunSteps(ctx, "run1", steps); err != nil {
		t.Fatal(err)
	}
	got, err = s.RunSteps(ctx, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Outcome != StepDone {
		t.Fatalf("resume reset the completed step to %q", got[0].Outcome)
	}
	if d := got[0].Duration(); d < 79*time.Second || d > 81*time.Second {
		t.Errorf("the completed step's duration reads %s, want about 80s", d)
	}
}

// The board reads a hundred runs every three seconds, so it asks for their
// steps once rather than once per row.
func TestRunStepsForManyRuns(t *testing.T) {
	s := newSteps(t)
	ctx := context.Background()
	if err := s.SeedRunSteps(ctx, "run1", []Step{{ID: "a", Run: "true"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedRunSteps(ctx, "run2", []Step{{ID: "a", Run: "true"},
		{ID: "b", Run: "true"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunStepsFor(ctx, []string{"run1", "run2", "run3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["run1"]) != 1 || len(got["run2"]) != 2 {
		t.Errorf("read %d and %d steps, want 1 and 2", len(got["run1"]), len(got["run2"]))
	}
	if _, ok := got["run3"]; ok {
		// An ordinary run has no steps, and the absence is what tells the board
		// it is not a flow.
		t.Error("a run with no steps came back with an entry")
	}
	if _, err := s.RunStepsFor(ctx, nil); err != nil {
		t.Errorf("asking about no runs failed: %v", err)
	}
}
