package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/internal/store"
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

	job := store.Job{ID: "wf", Name: "Flow", CWD: work, Kind: "claude",
		Model: "sonnet", Enabled: true, Timeout: 0, Steps: []store.Step{
			{ID: "sync", Run: "echo ran >> " + counter},
			{ID: "gate", Run: "test -f " + gate},
			{ID: "ship", Run: "echo shipped >> " + counter},
		}}
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
	job := store.Job{ID: "wf", CWD: work, Kind: "claude", Model: "sonnet",
		Enabled: true, Steps: []store.Step{{ID: "only", Run: "true"}}}
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

// Steps are declared in JSON because whatever writes a flow is usually
// another agent. A file that is not a step list must be refused where it is
// read, not stored and discovered later.
func TestLoadStepsReadsAListAndRejectsTheRest(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "steps.json")
	steps := []store.Step{{ID: "sync", Run: "true"}, {ID: "author", Agent: "write"}}
	raw, _ := json.Marshal(steps)
	if err := os.WriteFile(good, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadSteps(good)
	if err != nil {
		t.Fatalf("a valid step file was refused: %v", err)
	}
	if len(got) != 2 || got[0].ID != "sync" || got[1].Agent != "write" {
		t.Errorf("read back %+v, want the two declared steps in order", got)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"id":"sync"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSteps(bad); err == nil {
		t.Error("a single object was accepted as a step list")
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSteps(empty); err == nil {
		t.Error("an empty list was accepted, which would store a flow with no steps")
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
