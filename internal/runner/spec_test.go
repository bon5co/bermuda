package runner

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// FromStore is the only place a stored job becomes a runnable one, and every
// field it forgets or crosses fails silently: the run happens, in the wrong
// directory or on the wrong agent kind, and reports success. Distinct values per
// field so a swap cannot pass.
func TestFromStoreCarriesWhatTheRunNeeds(t *testing.T) {
	j := store.Job{
		ID:         "nightly-sweep",
		Name:       "Nightly Sweep",
		Prompt:     "sweep the coverage",
		CWD:        "/home/rafael/Projects/bermuda",
		Kind:       "codex",
		Timeout:    25 * time.Minute,
		Persistent: true,
		ExtraArgs:  "--sandbox danger-full-access",
	}

	got := FromStore(j)

	if got.ID != j.ID {
		t.Errorf("ID = %q, want %q", got.ID, j.ID)
	}
	if got.Prompt != j.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, j.Prompt)
	}
	if got.CWD != j.CWD {
		t.Errorf("CWD = %q, want %q", got.CWD, j.CWD)
	}
	if got.Kind != j.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, j.Kind)
	}
	if got.Timeout != j.Timeout {
		t.Errorf("Timeout = %s, want %s", got.Timeout, j.Timeout)
	}
	if !got.Persistent {
		t.Error("Persistent was dropped: the job's agent would be rebuilt every run")
	}
	// The args are the job's assembled invocation, not a second hand-rolled one.
	if want := BuildAgentArgs(j); !reflect.DeepEqual(got.AgentArgs, want) {
		t.Errorf("AgentArgs = %v, want %v", got.AgentArgs, want)
	}
}

// A non-persistent job must not come back persistent: a persistent run reuses a
// live agent and keeps its tab, so the mistake would leak one agent per job and
// hand the next run somebody else's context.
func TestFromStoreDoesNotInventPersistence(t *testing.T) {
	if FromStore(store.Job{ID: "one-off"}).Persistent {
		t.Error("a non-persistent stored job produced a persistent run")
	}
}

// The flags are typed by hand — in the board's editor as one line, in a flow
// file as a block — so both spellings have to reach argv. A newline-separated
// block parsed as a single argument is one unusable flag, and the agent rejects
// its own command line.
func TestExtraArgsAcceptBothTypedForms(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"single line", "--sandbox danger-full-access", []string{"--sandbox", "danger-full-access"}},
		{"newline block", "--sandbox\ndanger-full-access", []string{"--sandbox", "danger-full-access"}},
		{"newline block with blanks and indentation", "\n  --sandbox  \n\n\tdanger-full-access\n", []string{"--sandbox", "danger-full-access"}},
		{"empty", "", nil},
		{"whitespace only", "   \n\t\n", nil},
		{"repeated spaces on one line", "--a    --b", []string{"--a", "--b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A non-claude kind is pure passthrough, so this is the split alone.
			got := BuildAgentArgs(store.Job{Kind: "codex", ExtraArgs: c.raw})
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("args = %#v, want %#v", got, c.want)
			}
		})
	}
}

// A line kept whole is the case that matters: an argument with a space inside it
// (a prompt fragment, a path) survives only in the newline form, which is why
// both forms exist rather than one canonical one.
func TestNewlineFormKeepsSpacesInsideAnArgument(t *testing.T) {
	got := BuildAgentArgs(store.Job{Kind: "codex", ExtraArgs: "--append-prompt\nbe terse and exact"})
	want := []string{"--append-prompt", "be terse and exact"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

// Passthrough args come last so a job can override a flag bermuda assembled
// itself; claude takes the last spelling of a repeated flag. Emitting them first
// would make every hand-written override silently lose to bermuda's default.
func TestExtraArgsComeAfterTheModelledFlags(t *testing.T) {
	got := BuildAgentArgs(store.Job{Kind: "claude", Model: "sonnet", ExtraArgs: "--model opus"})
	if len(got) < 2 {
		t.Fatalf("args = %v", got)
	}
	if tail := got[len(got)-2:]; !reflect.DeepEqual(tail, []string{"--model", "opus"}) {
		t.Errorf("args = %v: the job's own --model must be last to win", got)
	}
}

// Every run states its model. A job that names none still has to run on a stated
// one rather than inheriting whatever the agent defaults to that week, or the
// same job changes model under the schedule with nothing recording it.
func TestModelIsAlwaysStatedForClaude(t *testing.T) {
	got := strings.Join(BuildAgentArgs(store.Job{Kind: "claude"}), " ")
	if !strings.Contains(got, "--model "+store.DefaultModel) {
		t.Errorf("args %q do not state the default model %q", got, store.DefaultModel)
	}
}

// The bypass has to reach the actual command line, and a step's opt-out has to
// reach it too. Asserting on the job struct alone is what let an unlaunchable
// agent step ship: the field looked right and the flag was never emitted.
func TestStepArgsCarryThePermissionDecision(t *testing.T) {
	job := store.Job{Model: "sonnet", Kind: "claude", SkipPermissions: true,
		PermissionMode: "acceptEdits"}

	got := strings.Join(BuildStepArgs(job, store.Step{ID: "a", Agent: "go"}), " ")
	if !strings.Contains(got, "--dangerously-skip-permissions") {
		t.Errorf("args %q lack the bypass; the step would park at the first prompt", got)
	}

	no := false
	got = strings.Join(BuildStepArgs(job, store.Step{ID: "b", Agent: "go", SkipPermissions: &no}), " ")
	if strings.Contains(got, "--dangerously-skip-permissions") {
		t.Errorf("args %q still bypass after the step opted out", got)
	}
	if !strings.Contains(got, "--permission-mode acceptEdits") {
		t.Errorf("args %q have no permission mode once the bypass is off", got)
	}
}

// A job that keeps context carries the flag onto the runnable job, and a flow
// step never does. A step that kept context would be handed the previous
// step's conversation, which is the thing StepJob exists to prevent -- the
// reviewer would inherit the writer's assumptions and review its own work.
func TestKeepContextTravelsToTheJobButNeverToAStep(t *testing.T) {
	job := store.Job{
		ID: "nightly", CWD: "/srv/work", Kind: "claude",
		Persistent: true, KeepContext: true,
	}

	if got := FromStore(job); !got.KeepContext {
		t.Error("KeepContext did not reach the runnable job")
	}
	if got := StepJob(job, store.Step{ID: "review", Agent: "review it"}); got.KeepContext {
		t.Error("a flow step kept context: it would inherit the previous step's conversation")
	}
}
