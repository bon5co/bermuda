package main

import (
	"os"
	"strconv"
	"testing"
)

// Every one of these sets the whole environment it depends on before reading
// it. A test that inherited $CLAUDE_PID from the agent running it would pass on
// this machine and prove nothing about the rule.
func clearPIDEnv(t *testing.T) {
	t.Helper()
	for _, env := range pidSources {
		t.Setenv(env, "")
	}
}

// The pid has to be the agent's, not the command's. `bermuda thread claim` is a
// process that lives for milliseconds — two consecutive invocations from one
// agent session on this machine were pids 322534 and 322594 — so a claim and
// its release would never match and no agent could release its own lease.
func TestResolvePIDPrefersTheAgentOverTheProcess(t *testing.T) {
	clearPIDEnv(t)
	t.Setenv("CLAUDE_PID", "5095")

	pid, source := resolvePID()
	if pid != "5095" {
		t.Errorf("pid resolved to %q, want $CLAUDE_PID: the agent outlives this process", pid)
	}
	if pid == strconv.Itoa(os.Getpid()) {
		t.Error("the pid is this process's own, which differs on every invocation")
	}
	if source != "$CLAUDE_PID" {
		t.Errorf("pid source is %q, want $CLAUDE_PID: a wrong pid is undiagnosable "+
			"without knowing which rule fired", source)
	}
}

// The order below CLAUDE_PID matters too: a herdr pane is stable for the life
// of the agent in it, which is exactly the property a discriminator needs.
func TestResolvePIDFallsThroughInOrder(t *testing.T) {
	clearPIDEnv(t)
	t.Setenv("HERDR_PANE_ID", "pane-3")
	if pid, source := resolvePID(); pid != "pane-3" || source != "$HERDR_PANE_ID" {
		t.Errorf("with only a pane id the pid is %q from %q, want pane-3", pid, source)
	}

	t.Setenv("CLAUDE_PID", "5095")
	if pid, _ := resolvePID(); pid != "5095" {
		t.Errorf("with both set the pid is %q, want the agent's own pid", pid)
	}

	// The override is last to be set and first to win, because it is what an
	// agent reaches for when the rules above resolved to the wrong thing.
	t.Setenv("BERMUDA_PID", "forced")
	if pid, source := resolvePID(); pid != "forced" || source != "$BERMUDA_PID" {
		t.Errorf("$BERMUDA_PID resolved to %q from %q, want it to force the pid", pid, source)
	}
}

// With nothing exported there still has to be an answer. A blank pid is not a
// failure — it means "identified by name", which is the old bug — so the
// fallbacks must always produce something.
func TestResolvePIDAlwaysAnswers(t *testing.T) {
	clearPIDEnv(t)
	pid, source := resolvePID()
	if pid == "" || source == "" {
		t.Errorf("with nothing set the pid is %q from %q, want a fallback", pid, source)
	}
}

// An interactive identity carries a pid; a bermuda run does not, because its
// job and run ids already make two runs two holders. Stamping a pid on a run
// would make each of its commands a different holder from the last.
func TestResolveIdentityStampsOnlyInteractiveAgents(t *testing.T) {
	clearPIDEnv(t)
	t.Setenv("CLAUDE_PID", "5095")
	t.Setenv("BERMUDA_JOB_ID", "")
	t.Setenv("BERMUDA_RUN_DIR", "")
	t.Setenv("BERMUDA_THREAD_AGENT", "")
	t.Setenv("BERMUDA_ROOM_AGENT", "")

	id, err := resolveIdentity("ada")
	if err != nil {
		t.Fatal(err)
	}
	if id.PID != "5095" || id.String() != "ada#5095" {
		t.Errorf("--as ada resolved to %+v, want a pid on it", id)
	}

	t.Setenv("BERMUDA_THREAD_AGENT", "setup-agent")
	if id, err := resolveIdentity(""); err != nil || id.PID != "5095" {
		t.Errorf("$BERMUDA_THREAD_AGENT resolved to %+v, %v, want a pid on it", id, err)
	}

	t.Setenv("BERMUDA_JOB_ID", "scrape-daily")
	t.Setenv("BERMUDA_RUN_DIR", "/tmp/runs/run-1")
	id, err = resolveIdentity("")
	if err != nil {
		t.Fatal(err)
	}
	if id.PID != "" {
		t.Errorf("a bermuda run resolved to %+v, want no pid: the run id is what "+
			"tells two runs of one job apart", id)
	}
	if id.JobID != "scrape-daily" || id.RunID != "run-1" {
		t.Errorf("a bermuda run resolved to %+v, want its job and run unchanged", id)
	}
}
