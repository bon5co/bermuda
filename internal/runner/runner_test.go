package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
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

// Runs of the same job on different days must not collide either. A run id is
// "<timestamp>-<job id>", so truncating to the tail kept the clock time and cut
// the date: rt-template-daily's 19:00 run was named "t190002z-rt-template-daily"
// on two consecutive days, and herdr rejected the second with agent_name_taken
// before the run dir was even created.
func TestAgentNameDistinctAcrossDaysForLongJobID(t *testing.T) {
	const job = "rt-template-daily"
	a := agentName(job, "20260809T190002Z-"+job)
	b := agentName(job, "20260810T190002Z-"+job)
	if a == b {
		t.Errorf("agentName collided across days: %q", a)
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

// A run that dies before the agent starts has no result.json, and the row
// persisted for it took its note only from that file — so the failure was
// recorded as an outcome with no words at all. Twelve of the fleet's failures
// classify as `unknown` for want of exactly these words.
func TestLaunchFailureIsWhatTheRunSays(t *testing.T) {
	// A file where the run directory should go: MkdirAll fails, which is the
	// earliest launch-path error there is and needs no herdr server.
	base := t.TempDir()
	blocked := filepath.Join(base, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Runner{}
	run, err := r.ExecuteIn(context.Background(), Job{ID: "smoke"}, "run-1", filepath.Join(blocked, "run-1"))
	if err == nil {
		t.Fatal("expected the run dir creation to fail")
	}
	if run == nil {
		t.Fatal("a failed launch must still return a run to persist")
	}
	if run.Err == nil {
		t.Fatal("the launch error never reached the run")
	}
	note := run.Note()
	if !strings.HasPrefix(note, "bermuda: ") {
		t.Errorf("note %q must be marked as bermuda's own observation, not the agent's", note)
	}
	if !strings.Contains(note, "create run dir") {
		t.Errorf("note %q does not say what failed", note)
	}
}

// The agent's own account of the work wins whenever it wrote one: the runner
// trusts result.json for the outcome, and the note has to come from the same
// place or the two would describe different runs.
func TestResultNoteWinsOverBermudasOwn(t *testing.T) {
	run := &Run{
		Result: &Result{Status: "error", Note: "upload rejected: 400"},
		Err:    errors.New("close tab: tab_not_found"),
	}
	if got := run.Note(); got != "upload rejected: 400" {
		t.Errorf("Note() = %q, want the agent's own note", got)
	}
}

// A park bermuda observed nothing about stays silent. The reason is already a
// column of its own, and restating it as prose would put words in the mouth of
// a run that saw nothing — which is how an unclassified failure turns into a
// confident wrong cause.
func TestParkWithoutAnErrorSaysNothing(t *testing.T) {
	run := &Run{Outcome: OutcomeParked, ParkReason: ParkNoResult}
	if got := run.Note(); got != "" {
		t.Errorf("Note() = %q, want empty", got)
	}
}

// The first error is the cause; what happens afterwards is aftermath. A tab
// that would not close after the agent failed to start must not become the
// explanation for the run.
func TestFirstErrorWins(t *testing.T) {
	run := &Run{}
	first := errors.New("start agent: agent_name_taken")
	_ = run.fail(first)
	_ = run.fail(errors.New("close tab: tab_not_found"))
	if !errors.Is(run.Err, first) {
		t.Errorf("run.Err = %v, want the first error", run.Err)
	}
}

// Every instrument that asks why the fleet failed walks the run directories,
// not the database, so a launch failure that recorded itself only on the run row
// is still a directory holding a prompt and no explanation.
func TestLaunchFailureIsWrittenIntoTheRunDirectory(t *testing.T) {
	dir := t.TempDir()
	run := &Run{RunDir: dir}
	_ = run.fail(errors.New("start agent: agent_name_taken"))

	b, err := os.ReadFile(filepath.Join(dir, ErrFile))
	if err != nil {
		t.Fatalf("no %s written: %v", ErrFile, err)
	}
	if !strings.Contains(string(b), "agent_name_taken") {
		t.Errorf("%s = %q, want the launch error", ErrFile, b)
	}
	// result.json is the agent's own account and the sole authority on the
	// outcome. Writing one here would forge the signal the runner trusts.
	if _, err := os.Stat(filepath.Join(dir, "result.json")); !os.IsNotExist(err) {
		t.Error("a launch failure must not write a result.json on the agent's behalf")
	}
}

// A run with nowhere to write — the failure was the run directory itself — must
// not turn a failed launch into a second failure.
func TestErrFileIsSilentWithNoRunDirectory(t *testing.T) {
	run := &Run{}
	_ = run.fail(errors.New("create run dir: permission denied"))
	if run.Err == nil {
		t.Error("the error must still reach the run")
	}
}

// A run directory has no ParkReason column. Every instrument that asks why the
// fleet failed walks the directories, so a blocked run that recorded itself
// only on the row is still a directory holding a prompt and no explanation —
// which is what `unknown` has been made of.
func TestObservedParkIsWrittenIntoTheRunDirectory(t *testing.T) {
	for _, tc := range []struct {
		reason ParkReason
		want   string
	}{
		{ParkBlocked, "human input"},
		{ParkTimeout, "timeout"},
		{ParkAgentLost, "lost sight"},
	} {
		dir := t.TempDir()
		run := &Run{RunDir: dir, Outcome: OutcomeParked, ParkReason: tc.reason}
		run.recordPark()

		b, err := os.ReadFile(filepath.Join(dir, ErrFile))
		if err != nil {
			t.Fatalf("%s: no %s written: %v", tc.reason, ErrFile, err)
		}
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("%s: %s = %q, want it to mention %q", tc.reason, ErrFile, b, tc.want)
		}
		// The agent's own account stays the agent's to write.
		if _, err := os.Stat(filepath.Join(dir, "result.json")); !os.IsNotExist(err) {
			t.Errorf("%s: a park must not write a result.json on the agent's behalf", tc.reason)
		}
	}
}

// A park bermuda observed nothing about writes nothing. `no_result` is the
// absence its own column already names, and `bad_result` is the agent's file
// sitting there unreadable — restating either as bermuda's observation is how
// an unclassified failure becomes a confident wrong cause.
func TestUnobservedParkWritesNoFile(t *testing.T) {
	for _, reason := range []ParkReason{ParkNoResult, ParkBadResult} {
		dir := t.TempDir()
		run := &Run{RunDir: dir, Outcome: OutcomeParked, ParkReason: reason}
		run.recordPark()
		if _, err := os.Stat(filepath.Join(dir, ErrFile)); !os.IsNotExist(err) {
			t.Errorf("%s: wrote %s, want silence", reason, ErrFile)
		}
	}
}

// The launch error is the more specific fact: a run that died starting its
// agent parks as well, and the park must not overwrite what killed it.
func TestLaunchErrorSurvivesTheParkThatFollowsIt(t *testing.T) {
	dir := t.TempDir()
	run := &Run{RunDir: dir, Outcome: OutcomeParked, ParkReason: ParkBlocked}
	_ = run.fail(errors.New("start agent: agent_name_taken"))
	run.recordPark()

	b, err := os.ReadFile(filepath.Join(dir, ErrFile))
	if err != nil {
		t.Fatalf("no %s written: %v", ErrFile, err)
	}
	if !strings.Contains(string(b), "agent_name_taken") {
		t.Errorf("%s = %q, want the launch error to stand", ErrFile, b)
	}
}

// A run that finished has nothing to park about, whatever the pessimistic
// default left in the reason field.
func TestFinishedRunWritesNoParkNote(t *testing.T) {
	dir := t.TempDir()
	run := &Run{RunDir: dir, Outcome: OutcomeDone, ParkReason: ParkBlocked}
	run.recordPark()
	if _, err := os.Stat(filepath.Join(dir, ErrFile)); !os.IsNotExist(err) {
		t.Errorf("wrote %s for a finished run", ErrFile)
	}
}

// The classifier is where a live run learns it parked, so that is where the
// note has to be written — a unit test on recordPark alone would pass with
// nothing calling it.
func TestClassifyRecordsABlockedParkOnDisk(t *testing.T) {
	dir := t.TempDir()
	run := &Run{RunDir: dir}
	(&Runner{}).classify(run, herdrcli.StatusBlocked, nil)

	if run.ParkReason != ParkBlocked {
		t.Fatalf("ParkReason = %q, want %q", run.ParkReason, ParkBlocked)
	}
	b, err := os.ReadFile(filepath.Join(dir, ErrFile))
	if err != nil {
		t.Fatalf("no %s written: %v", ErrFile, err)
	}
	if !strings.Contains(string(b), "human input") {
		t.Errorf("%s = %q, want the blocked observation", ErrFile, b)
	}
}

// A run that wrote result.json says everything about itself already; bermuda
// must not add a second account beside it.
func TestClassifyWritesNothingWhenTheAgentReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.json"),
		[]byte(`{"status":"ok","note":"done"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &Run{RunDir: dir, Outcome: OutcomeParked, ParkReason: ParkNoResult}
	(&Runner{}).classify(run, herdrcli.StatusBlocked, nil)

	if run.Outcome != OutcomeDone {
		t.Fatalf("Outcome = %q, want %q", run.Outcome, OutcomeDone)
	}
	if _, err := os.Stat(filepath.Join(dir, ErrFile)); !os.IsNotExist(err) {
		t.Errorf("wrote %s beside the agent's own result", ErrFile)
	}
}
