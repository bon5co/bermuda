package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// write puts a flow file in a temporary directory and returns the directory.
func write(t *testing.T, id, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, id+Ext), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The whole reason flows are files: a person or an agent writes this by hand,
// and what they wrote is what runs.
func TestAFlowIsReadFromTheFileAsWritten(t *testing.T) {
	dir := write(t, "triage", `
about: triage an incoming report
input: a report or a PR number
steps:
  - id: assess
    agent: Look at {{input}} and say whether it is real.
    model: opus
  - id: fix
    agent: "{{previous}} — now fix it."
  - id: verify
    run: go test ./...
`)
	f, err := Load(dir, "triage")
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != "triage" {
		t.Errorf("id is %q, want the filename", f.ID)
	}
	if !f.TakesInput() || f.Input != "a report or a PR number" {
		t.Errorf("input read back as %q", f.Input)
	}
	if len(f.Steps) != 3 {
		t.Fatalf("read %d steps, want 3", len(f.Steps))
	}
	// Order is the feature. A flow whose steps ran in any other order would be a
	// slower way of handing one agent a list and hoping.
	for i, want := range []string{"assess", "fix", "verify"} {
		if f.Steps[i].ID != want {
			t.Errorf("step %d is %q, want %q", i, f.Steps[i].ID, want)
		}
	}
	if f.Steps[0].Model != "opus" {
		t.Errorf("per-step model read back as %q", f.Steps[0].Model)
	}
	if f.Steps[2].IsAgent() {
		t.Error("the run step was read as an agent step")
	}
}

// A typo in a hand-written file is the ordinary case here, so an unknown key is
// refused rather than dropped. A silently ignored `agnet:` is a step that never
// runs, in a flow that then reports success.
func TestAnUnknownKeyIsRefusedRatherThanIgnored(t *testing.T) {
	dir := write(t, "typo", `
steps:
  - id: one
    agnet: this was meant to be a prompt
`)
	if _, err := Load(dir, "typo"); err == nil {
		t.Fatal("a misspelled key was accepted; the step would never run")
	}
}

// The same rules a job's steps were always held to. A flow must not be able to
// express something the runner would refuse halfway through.
func TestTheStepRulesStillApply(t *testing.T) {
	for name, body := range map[string]string{
		"both agent and run": "steps:\n  - id: one\n    agent: write\n    run: true\n",
		"neither":            "steps:\n  - id: one\n",
		"duplicate ids":      "steps:\n  - id: one\n    run: true\n  - id: one\n    run: true\n",
		"no id":              "steps:\n  - run: true\n",
		"model on a run":     "steps:\n  - id: one\n    run: true\n    model: opus\n",
	} {
		dir := write(t, "bad", body)
		if _, err := Load(dir, "bad"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// An empty flow is refused. A flow with no steps is a harness that forces an
// agent through nothing, and it would report success every time.
func TestAFlowWithNoStepsIsRefused(t *testing.T) {
	dir := write(t, "empty", "about: nothing\nsteps: []\n")
	if _, err := Load(dir, "empty"); err == nil {
		t.Fatal("a flow with no steps was accepted")
	}
}

// Checked when the file is read, not when it runs. A {{inpt}} discovered
// mid-flow has already spent an agent turn producing a prompt with literal
// braces in it, which the agent will then do its best to interpret.
func TestAnUnfillablePlaceholderIsRefusedAtReadTime(t *testing.T) {
	dir := write(t, "typo", "steps:\n  - id: one\n    agent: look at {{inpt}}\n")
	_, err := Load(dir, "typo")
	if err == nil {
		t.Fatal("a placeholder nothing can fill was accepted")
	}
	if !strings.Contains(err.Error(), "inpt") {
		t.Errorf("the refusal was %q, want it to name the placeholder", err)
	}
}

// {{previous}} in the first step has nothing to refer to, and would silently
// become a blank where the prompt's subject should be.
func TestThePreviousPlaceholderIsRefusedInTheFirstStep(t *testing.T) {
	dir := write(t, "first", "steps:\n  - id: one\n    agent: act on {{previous}}\n")
	if _, err := Load(dir, "first"); err == nil {
		t.Fatal("the first step was allowed to reference a result nothing produced")
	}
}

// One broken flow must not make the other nine unlistable — and the broken one
// is exactly what the reader needs to be told about, since it is invisible
// everywhere else.
func TestListReturnsTheGoodOnesAndNamesTheBad(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"good":   "steps:\n  - id: one\n    run: true\n",
		"alsook": "steps:\n  - id: one\n    run: true\n",
		"broken": "steps:\n  - id: one\n    agent: x\n    run: y\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name+Ext), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A file that is not a flow at all is not a flow that is broken.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	flows, bad := List(dir)
	if len(flows) != 2 {
		t.Errorf("listed %d flows, want the 2 that parse", len(flows))
	}
	if len(bad) != 1 {
		t.Errorf("reported %d broken flows, want 1", len(bad))
	}
	// Sorted, because this is what `flow list` prints and a report is read by
	// looking a name up in it.
	if len(flows) == 2 && flows[0].ID > flows[1].ID {
		t.Error("flows came back unsorted")
	}
}

func TestParseIDRefusesWhatIsNotOne(t *testing.T) {
	for _, bad := range []string{"", "Better Lingo", "UPPER", "trailing-", "-leading", "double--hyphen", "has/slash"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("%q was accepted as a flow id", bad)
		}
	}
	for _, good := range []string{"triage", "release-check", "a1", "one-two-three"} {
		if _, err := ParseID(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
}

func TestLoadingAFlowThatIsNotThereSaysSo(t *testing.T) {
	_, err := Load(t.TempDir(), "missing")
	if err == nil {
		t.Fatal("a flow that does not exist loaded")
	}
	if !strings.Contains(err.Error(), "flow list") {
		t.Errorf("the error was %q, want it to say how to see what does exist", err)
	}
}

// The chain. A(x), then B(A's result).
func TestExpandSubstitutesTheInputAndThePreviousResult(t *testing.T) {
	vals := Values{Input: "PR #431", Previous: "it is real"}
	got := Expand(store.Step{ID: "fix", Agent: "given {{input}}: {{previous}}"}, vals)
	if got.Agent != "given PR #431: it is real" {
		t.Errorf("expanded to %q", got.Agent)
	}
	// Whitespace inside the braces is tolerated: these files are hand-written,
	// and `{{ input }}` silently surviving into a prompt is a bug nobody looks
	// for.
	spaced := Expand(store.Step{ID: "fix", Agent: "{{ input }}"}, vals)
	if spaced.Agent != "PR #431" {
		t.Errorf("a spaced placeholder expanded to %q", spaced.Agent)
	}
}

// A run step's command is never rewritten. It reads the same values from the
// environment, and rewriting the text would corrupt any command that
// legitimately contains braces.
func TestExpandLeavesACommandAlone(t *testing.T) {
	cmd := `awk '{print $1}' && echo {{input}}`
	got := Expand(store.Step{ID: "verify", Run: cmd}, Values{Input: "x"})
	if got.Run != cmd {
		t.Errorf("a run step was rewritten to %q", got.Run)
	}
}

// A run step gets the pair where a shell already looks for things.
func TestValuesReachARunStepThroughTheEnvironment(t *testing.T) {
	env := Values{Input: "x", Previous: "y"}.Env()
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, EnvInput+"=x") || !strings.Contains(joined, EnvPrevious+"=y") {
		t.Errorf("env was %v", env)
	}
}

// Save refuses to overwrite, because `flow new` with a name somebody already
// used would otherwise replace a working sequence with a template, and the
// first anyone hears of it is a run that does nothing.
func TestSaveWillNotClobberAnExistingFlow(t *testing.T) {
	dir := t.TempDir()
	f := Flow{ID: "triage", Steps: []store.Step{{ID: "one", Run: "true"}}}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, f); err == nil {
		t.Fatal("saving over an existing flow was allowed")
	}
}

// The bypass is on unless the file says otherwise, because a flow step has
// nobody in its pane to answer a permission prompt — it waits out its grace and
// parks. That default is what makes flows work unattended at all.
func TestThePermissionBypassDefaultsOnAndTheFileCanTakeItBack(t *testing.T) {
	on := write(t, "loud", "steps:\n  - id: one\n    agent: do it\n")
	f, err := Load(on, "loud")
	if err != nil {
		t.Fatal(err)
	}
	if !f.BypassesPermissions() {
		t.Error("a flow that says nothing does not bypass; every agent step would park")
	}

	off := write(t, "careful", "skip_permissions: false\nsteps:\n  - id: one\n    agent: do it\n")
	g, err := Load(off, "careful")
	if err != nil {
		t.Fatal(err)
	}
	if g.BypassesPermissions() {
		t.Error("skip_permissions: false was ignored")
	}
}

// A step can take it back for itself, the same way it overrides model or effort.
func TestAStepCanOptOutOfTheBypass(t *testing.T) {
	dir := write(t, "mixed", "steps:\n  - id: fast\n    agent: go\n  - id: careful\n    agent: think\n    skip_permissions: false\n")
	f, err := Load(dir, "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if f.Steps[0].SkipPermissions != nil {
		t.Error("a step that said nothing carries an opinion")
	}
	if f.Steps[1].SkipPermissions == nil || *f.Steps[1].SkipPermissions {
		t.Error("the step's opt-out did not survive the read")
	}
}

// It means nothing on a run step, so it is refused rather than ignored.
func TestSkipPermissionsIsRefusedOnARunStep(t *testing.T) {
	dir := write(t, "bad", "steps:\n  - id: one\n    run: true\n    skip_permissions: false\n")
	if _, err := Load(dir, "bad"); err == nil {
		t.Fatal("skip_permissions was accepted on a run step")
	}
}
