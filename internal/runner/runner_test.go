package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
)

// herdr rejects agent names that are not 1-32 chars of lowercase letters,
// digits, '-' or '_', starting with a lowercase letter.
func TestAgentNameIsHerdrLegal(t *testing.T) {
	cases := []struct{ job, run string }{
		{"smoke", "20260725T055954Z-smoke"},
		{"rt-template-daily", "20260725T055954Z-rt-template-daily"},
		{"UPPER Case Job!", "20260725T055954Z-x"},
		{"", ""},
		{strings.Repeat("x", 100), strings.Repeat("y", 100)},
		{"9leading", "20260725T055954Z-9"},
	}
	for _, c := range cases {
		got := agentName(c.job, c.run)
		if len(got) < 1 || len(got) > 32 {
			t.Errorf("agentName(%q,%q) = %q: length %d out of range", c.job, c.run, got, len(got))
		}
		if got[0] < 'a' || got[0] > 'z' {
			t.Errorf("agentName(%q,%q) = %q: must start with lowercase letter", c.job, c.run, got)
		}
		for _, r := range got {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				t.Errorf("agentName(%q,%q) = %q: illegal rune %q", c.job, c.run, got, r)
			}
		}
	}
}

// Concurrent runs of the same job must not collide on agent name, since herdr
// addresses agents by name.
func TestAgentNameDistinctPerRun(t *testing.T) {
	a := agentName("daily", "20260725t055954z-daily")
	b := agentName("daily", "20260725t055955z-daily")
	if a == b {
		t.Errorf("agentName collided across runs: %q", a)
	}
}

// The incident: herdr reported the agent gone thirty seconds into a
// twenty-five-minute step, while the agent went on working for another ten
// minutes and then wrote its result file. Losing sight of a process is not
// evidence that it stopped, so the run must wait for the file it was promised.
func TestLostAgentWaitsForTheResultItStillWrites(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{ResultPoll: time.Millisecond}

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = writeResult(dir, Result{Status: "ok", Note: "published"})
	}()

	r.awaitResult(context.Background(), dir, time.Now().Add(2*time.Second))

	run := &Run{RunDir: dir}
	r.classify(run, herdrcli.StatusUnknown, agentGone())
	if run.Outcome != OutcomeDone {
		t.Fatalf("outcome %q (%q), want done", run.Outcome, run.ParkReason)
	}
	if run.Result == nil || run.Result.Note != "published" {
		t.Errorf("result not adopted: %+v", run.Result)
	}
}

// The wait is bounded: an agent that really did die must not hold the run open
// past the budget the job declared.
func TestLostAgentGivesUpAtTheDeadline(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{ResultPoll: time.Millisecond}

	start := time.Now()
	r.awaitResult(context.Background(), dir, start.Add(30*time.Millisecond))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s past a 30ms deadline", elapsed)
	}

	run := &Run{RunDir: dir}
	r.classify(run, herdrcli.StatusUnknown, agentGone())
	if run.Outcome != OutcomeParked {
		t.Fatalf("outcome %q, want parked", run.Outcome)
	}
	// "agent_lost", not "no_result": nobody watched this agent decline to write
	// a result — bermuda stopped being able to watch it at all.
	if run.ParkReason != ParkAgentLost {
		t.Errorf("park reason %q, want %q", run.ParkReason, ParkAgentLost)
	}
}

// A run whose agent settled normally and wrote nothing is still no_result, and
// must not pay the lost-agent wait.
func TestSettledAgentWithNoResultIsStillNoResult(t *testing.T) {
	run := &Run{RunDir: t.TempDir()}
	(&Runner{}).classify(run, herdrcli.StatusIdle, nil)
	if run.Outcome != OutcomeParked || run.ParkReason != ParkNoResult {
		t.Fatalf("got %q/%q, want parked/no_result", run.Outcome, run.ParkReason)
	}
}

// A timeout means the agent was watched for the whole budget and never
// settled. There is nothing left to wait for, so it is not a lost agent.
func TestTimeoutIsNotALostAgent(t *testing.T) {
	timeout := &herdrcli.Error{Code: "timeout", Message: "deadline"}
	if lostSight(timeout) {
		t.Error("a timeout must not be treated as losing sight of the agent")
	}
	if !lostSight(agentGone()) {
		t.Error("agent_not_running must be treated as losing sight of the agent")
	}
	if lostSight(nil) {
		t.Error("no error is not losing sight of the agent")
	}

	run := &Run{RunDir: t.TempDir()}
	(&Runner{}).classify(run, herdrcli.StatusUnknown, timeout)
	if run.ParkReason != ParkTimeout {
		t.Errorf("park reason %q, want timeout", run.ParkReason)
	}
}

// A result file that is present but unreadable is the agent's mistake, and says
// so whatever happened to the process afterwards.
func TestUnparseableResultOutranksALostAgent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &Run{RunDir: dir}
	(&Runner{}).classify(run, herdrcli.StatusUnknown, agentGone())
	if run.ParkReason != ParkBadResult {
		t.Errorf("park reason %q, want bad_result", run.ParkReason)
	}
}

// A job with no timeout of its own is bounded by the runner's grace rather than
// waiting forever on a slot.
func TestResultDeadlineFallsBackToGrace(t *testing.T) {
	r := &Runner{ResultGrace: time.Minute}
	if got := time.Until(r.resultDeadline(Job{})); got > time.Minute+time.Second {
		t.Errorf("deadline %s away, want about the 1m grace", got)
	}
	if got := time.Until(r.resultDeadline(Job{Timeout: 5 * time.Minute})); got < 4*time.Minute {
		t.Errorf("deadline %s away, want about the job's 5m timeout", got)
	}
}

func agentGone() error {
	return &herdrcli.Error{
		Code:    "agent_not_running",
		Message: "agent is no longer running in the target pane",
	}
}
