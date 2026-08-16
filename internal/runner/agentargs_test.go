package runner

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// argValues returns every value given for a flag, in the order they appear.
//
// Assertions go through this rather than through a joined string: "--add-dir
// /a" appearing somewhere in a line does not prove the value is attached to the
// flag, and a pair that drifted apart is exactly the failure that produces an
// agent which will not launch.
func argValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// Every modelled field has to reach argv attached to its own flag. A field that
// is silently dropped does not fail the run: the agent starts with the wrong
// tool policy, the wrong directories, or no budget cap, finishes, and reports
// success. Distinct values per field so a crossed pair cannot pass.
func TestEveryModelledFieldReachesTheCommandLine(t *testing.T) {
	j := store.Job{
		Kind:            "claude",
		Model:           "opus",
		PermissionMode:  "acceptEdits",
		AllowedTools:    "Read,Grep",
		DisallowedTools: "Bash",
		AddDirs:         []string{"/srv/one", "/srv/two"},
		MaxBudgetUSD:    "2.50",
		AutoCompact:     "120000",
	}

	args := BuildAgentArgs(j)

	cases := []struct {
		flag string
		want []string
	}{
		{"--model", []string{"opus"}},
		{"--permission-mode", []string{"acceptEdits"}},
		{"--allowedTools", []string{"Read,Grep"}},
		{"--disallowedTools", []string{"Bash"}},
		{"--add-dir", []string{"/srv/one", "/srv/two"}},
		{"--max-budget-usd", []string{"2.50"}},
		{"--autocompact", []string{"120000"}},
	}
	for _, c := range cases {
		if got := argValues(args, c.flag); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v (args %v)", c.flag, got, c.want, args)
		}
	}
}

// The bypass and a permission mode are mutually exclusive, and the bypass wins.
// Emitting both is not a cosmetic duplicate: the two flags contradict each
// other, and which one the agent honours is not bermuda's to guess.
func TestTheBypassReplacesThePermissionModeRatherThanJoiningIt(t *testing.T) {
	args := BuildAgentArgs(store.Job{
		Kind: "claude", SkipPermissions: true, PermissionMode: "acceptEdits",
	})
	if !hasArg(args, "--dangerously-skip-permissions") {
		t.Errorf("args %v lack the bypass the job asked for", args)
	}
	if hasArg(args, "--permission-mode") {
		t.Errorf("args %v carry both the bypass and a permission mode", args)
	}
}

// A field nobody filled in must produce no flag at all. A flag emitted with an
// empty value is worse than a missing one — "--allowedTools ''" is a policy
// that allows nothing, and the run dies on its first tool call.
func TestUnsetAndBlankFieldsEmitNoFlag(t *testing.T) {
	blank := []string{
		"--permission-mode", "--allowedTools", "--disallowedTools",
		"--add-dir", "--max-budget-usd", "--autocompact",
		"--dangerously-skip-permissions",
	}
	for _, name := range []string{"unset", "whitespace"} {
		j := store.Job{Kind: "claude"}
		if name == "whitespace" {
			j.AllowedTools = "   "
			j.DisallowedTools = "\t"
			j.AutoCompact = "  "
			j.AddDirs = []string{"", "  "}
		}
		args := BuildAgentArgs(j)
		for _, flag := range blank {
			if hasArg(args, flag) {
				t.Errorf("%s job emitted %s: args %v", name, flag, args)
			}
		}
	}
}

// An add-dir list is trimmed, not passed through raw. A directory carrying its
// surrounding whitespace is a path the agent cannot open, and the run reports
// the failure as a missing directory the job clearly names.
func TestAddDirsAreTrimmedAndBlanksDropped(t *testing.T) {
	args := BuildAgentArgs(store.Job{
		Kind: "claude", AddDirs: []string{" /srv/one ", "", "  ", "/srv/two"},
	})
	if got, want := argValues(args, "--add-dir"), []string{"/srv/one", "/srv/two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--add-dir = %v, want %v", got, want)
	}
}

// --autocompact is modelled, so a job that also spells it into its passthrough
// has to win: claude takes the last occurrence. Emitting the modelled one last
// would make every hand-written override silently lose to the stored default,
// with the job's own configuration visible in the board and not in effect.
func TestAPassthroughAutoCompactOutranksTheStoredOne(t *testing.T) {
	args := BuildAgentArgs(store.Job{
		Kind: "claude", AutoCompact: "auto", ExtraArgs: "--autocompact 40000",
	})
	got := argValues(args, "--autocompact")
	if len(got) != 2 || got[len(got)-1] != "40000" {
		t.Errorf("--autocompact = %v, want the passthrough value last", got)
	}
}

// Only claude's flag spellings are modelled. Handing codex a --permission-mode
// or an --allowedTools it has never heard of is a command line it rejects
// outright, so another kind gets its passthrough and nothing else.
func TestAnotherAgentKindGetsItsPassthroughAndNoInventedFlags(t *testing.T) {
	args := BuildAgentArgs(store.Job{
		Kind:            "codex",
		Model:           "opus",
		PermissionMode:  "acceptEdits",
		AllowedTools:    "Read",
		DisallowedTools: "Bash",
		AddDirs:         []string{"/srv/one"},
		MaxBudgetUSD:    "2.50",
		AutoCompact:     "auto",
		SkipPermissions: true,
		ExtraArgs:       "--sandbox danger-full-access",
	})
	if want := []string{"--sandbox", "danger-full-access"}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want exactly %v", args, want)
	}
}

// An empty kind means claude, because that is what a job written before kinds
// existed is. Treating it as "some other agent" would drop the model and the
// permission flags from every such job at once.
func TestAnEmptyKindIsTreatedAsClaude(t *testing.T) {
	args := BuildAgentArgs(store.Job{Model: "opus", AllowedTools: "Read"})
	if got := argValues(args, "--model"); !reflect.DeepEqual(got, []string{"opus"}) {
		t.Errorf("--model = %v on a job with no kind: args %v", got, args)
	}
	if got := argValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Read"}) {
		t.Errorf("--allowedTools = %v on a job with no kind: args %v", got, args)
	}
}

// A step inherits the job's configuration and overrides only what it names.
// Inheritance that leaks the wrong way — a step's model becoming the job's, or
// a step losing the job's tool policy — is invisible until the bill or the
// permissions are wrong.
func TestAStepOverridesOnlyWhatItNames(t *testing.T) {
	job := store.Job{
		Kind: "claude", Model: "sonnet", AllowedTools: "Read,Grep",
		AddDirs: []string{"/srv/one"},
	}
	step := store.Step{ID: "review", Agent: "review it", Model: "opus"}

	args := BuildStepArgs(job, step)

	if got := argValues(args, "--model"); !reflect.DeepEqual(got, []string{"opus"}) {
		t.Errorf("--model = %v, want the step's override: args %v", got, args)
	}
	if got := argValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Read,Grep"}) {
		t.Errorf("--allowedTools = %v, want the job's inherited value: args %v", got, args)
	}
	if got := argValues(args, "--add-dir"); !reflect.DeepEqual(got, []string{"/srv/one"}) {
		t.Errorf("--add-dir = %v, want the job's inherited value: args %v", got, args)
	}
	// The override is for this step only; the job is a value and must not be
	// edited through, or every later step inherits the last one's model.
	if job.Model != "sonnet" {
		t.Errorf("the job's model became %q: a step wrote through to its flow", job.Model)
	}
}

// Effort and subagent are step-only axes and reach argv as their own flags. A
// step that names a reviewer subagent but launches the default agent runs the
// wrong charter over the same diff and passes.
func TestStepEffortAndSubagentReachTheCommandLine(t *testing.T) {
	args := BuildStepArgs(
		store.Job{Kind: "claude", Model: "sonnet"},
		store.Step{ID: "review", Agent: "review it", Effort: "high", Subagent: "code-reviewer"},
	)
	if got := argValues(args, "--effort"); !reflect.DeepEqual(got, []string{"high"}) {
		t.Errorf("--effort = %v: args %v", got, args)
	}
	if got := argValues(args, "--agent"); !reflect.DeepEqual(got, []string{"code-reviewer"}) {
		t.Errorf("--agent = %v: args %v", got, args)
	}
}

// A step that names neither must not emit the flags with empty values, for the
// same reason as the job's own blank fields: "--effort ''" is not a default.
func TestAStepThatNamesNoEffortOrSubagentEmitsNeitherFlag(t *testing.T) {
	args := BuildStepArgs(
		store.Job{Kind: "claude", Model: "sonnet"},
		store.Step{ID: "one", Agent: "go", Effort: "  ", Subagent: "\t"},
	)
	if hasArg(args, "--effort") || hasArg(args, "--agent") {
		t.Errorf("args %v carry an empty effort or subagent", args)
	}
}

// A step may switch agent kind, and the claude-only flags have to go with it —
// including --effort and --agent, which are appended after the kind check and
// would otherwise be the one pair that leaks onto a foreign command line.
func TestAStepThatSwitchesKindGetsNoClaudeFlags(t *testing.T) {
	args := BuildStepArgs(
		store.Job{Kind: "claude", Model: "sonnet", AllowedTools: "Read", ExtraArgs: "--sandbox ro"},
		store.Step{ID: "one", Agent: "go", Kind: "codex", Effort: "high", Subagent: "code-reviewer"},
	)
	if want := []string{"--sandbox", "ro"}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want exactly %v", args, want)
	}
}

// StepJob is what actually runs a step. Its id has to name both the flow and
// the step — the run directory and the agent are named from it — and it must
// never be persistent: a persistent step would reuse the previous step's live
// agent, handing the reviewer the writer's context under a different charter.
func TestStepJobNamesTheStepAndIsNeverPersistent(t *testing.T) {
	job := store.Job{
		ID: "nightly", CWD: "/srv/work", Kind: "claude", Model: "sonnet",
		Persistent: true,
	}
	step := store.Step{ID: "review", Agent: "review the diff"}

	got := StepJob(job, step)

	if got.ID != "nightly-review" {
		t.Errorf("ID = %q, want %q", got.ID, "nightly-review")
	}
	if got.Prompt != step.Agent {
		t.Errorf("Prompt = %q, want the step's agent prompt %q", got.Prompt, step.Agent)
	}
	if got.CWD != job.CWD {
		t.Errorf("CWD = %q, want the flow's %q", got.CWD, job.CWD)
	}
	if got.Persistent {
		t.Error("a step came back persistent: it would inherit the previous step's agent")
	}
	if want := BuildStepArgs(job, step); !reflect.DeepEqual(got.AgentArgs, want) {
		t.Errorf("AgentArgs = %v, want %v", got.AgentArgs, want)
	}
}

// The step's kind wins when it names one, and the flow's stands when it does
// not. A step silently falling back to claude on a codex flow builds a command
// line for the wrong binary.
func TestStepKindFallsBackToTheFlowsOwn(t *testing.T) {
	cases := []struct {
		name     string
		jobKind  string
		stepKind string
		want     string
	}{
		{"step names its own", "claude", "codex", "codex"},
		{"step names none", "codex", "", "codex"},
		{"step names whitespace", "codex", "  ", "codex"},
		{"neither names one", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StepJob(store.Job{ID: "f", Kind: c.jobKind}, store.Step{ID: "s", Kind: c.stepKind})
			if got.Kind != c.want {
				t.Errorf("Kind = %q, want %q", got.Kind, c.want)
			}
		})
	}
}

// The assembled line is the whole invocation, so a job with nothing set still
// has to be launchable: a model, and no stray empty argument that the agent
// would read as a prompt.
func TestAnEmptyJobStillProducesALaunchableLine(t *testing.T) {
	args := BuildAgentArgs(store.Job{})
	if got := argValues(args, "--model"); len(got) != 1 || got[0] != store.DefaultModel {
		t.Errorf("--model = %v, want the default %q", got, store.DefaultModel)
	}
	for i, a := range args {
		if strings.TrimSpace(a) == "" {
			t.Errorf("args[%d] is empty: args %v", i, args)
		}
	}
}
