package runner

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/internal/store"
)

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
