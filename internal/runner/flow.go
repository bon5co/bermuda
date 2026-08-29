package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v3/internal/flow"
	"github.com/bon5co/bermuda/v3/internal/statefs"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// A flow is a declared sequence of steps, run in series.
//
// What it buys over one prompt that says "then do B, then do C" is that the
// harness makes the call. B is launched by bermuda, not by A remembering to
// hand off, so A cannot skip B, inline B, or decide B was already covered — and
// A's report of its own work, the least reliable artifact in the system, is not
// an input to whether B runs.
//
// The other half is what happens when a step does not finish. A step that fails
// or that ends without writing result.json parks the flow *at that step*:
// the steps after it never start, so nothing downstream proceeds on a claim
// nobody checked. Everything a completed step wrote stays on disk, which is
// what makes resume cheap — and resume being cheap is what removes the pressure
// to wave a failed step through.
//
// A step may also declare `on_fail: {goto: <earlier step>}`, which turns the
// park into one more attempt. That is for the shape a park serves badly: the
// last step is a reviewer, it found something real, and the step that can fix it
// is the one three above that wrote the code. Parking there is correct and
// useless — it waits for a human to say "try again". The edge points backward
// because the step that fails is the checker and the step that must run again is
// the maker; a retry in place would re-read the same unchanged work.

// StepLauncher starts one agent step and runs it to completion, in its own
// process with its own directory.
//
// It is a field rather than a method call so tests can substitute a fake: a
// test that used the real launcher would start agents on this machine and talk
// to the live herdr socket.
type StepLauncher func(ctx context.Context, job Job, runID, stepDir string) (*Run, error)

// ShellRunner executes a `run:` step. It returns the combined output and
// whatever the command's exit status was.
type ShellRunner func(ctx context.Context, command, cwd string, env []string) ([]byte, error)

// Flow executes a job's declared steps in order.
type Flow struct {
	// Launch starts an agent step. Production passes Runner.ExecuteIn.
	Launch StepLauncher
	// Shell runs a command step, defaulting to `sh -c`.
	Shell ShellRunner
	// Space is the herdr workspace this run's steps share, and the thread that
	// space owns. Nil runs the steps where any other run's would go, which is what
	// happened before a flow had a space of its own: they still chain, they just
	// have nowhere to compare notes.
	Space *FlowSpace
	// Check is the checklist page this run's steps are told about, as
	// $BERMUDA_CHECK. Empty when the run is bound to none.
	//
	// The steps are told rather than left to work it out, because a step that
	// had to be handed a list id in its prompt is a step that eventually gets
	// the id wrong — and an item ticked on the wrong page is worse than one not
	// ticked at all. Ticking the item a step *declares* is the command layer's
	// job; this is only what a step needs to add an item nobody foresaw.
	Check string
	// ResetLoops gives a run's backward edges their full budget again.
	//
	// Off by default, because the budget surviving a resume is the point of
	// recording it: the ordinary resume is "I looked at why it parked and it is
	// worth continuing", not "spend the same allowance a second time". A human
	// who has fixed the thing the loop kept failing on asks for this explicitly.
	ResetLoops bool
	// Report is called when a step starts and again when it settles, so a
	// caller can persist progress while the flow is still going: a flow
	// that recorded itself only at the end would show nothing for the hour it
	// was running. Steps skipped because an earlier attempt already completed
	// them are deliberately not reported — the stored row already holds how
	// long that work really took, and overwriting it with this attempt's
	// zero-length skip would erase it.
	Report func(StepRun)
}

// OutcomeRunning marks a step that has started and not yet settled. Runs have
// no such outcome because the command layer records a run as running before it
// calls the runner at all; a step is started by the flow itself, so the
// flow is the only thing that can say so.
const OutcomeRunning Outcome = "running"

// ParkStepFailed is why a flow parked: one of its steps reported failure.
// The step keeps its own outcome — the flow parks, the step failed.
const ParkStepFailed ParkReason = "step_failed"

// ParkLoopExhausted is why a flow parked: a step with an on_fail edge kept
// failing and has used every loop it declared. It is a park like any other —
// the attempts are in the thread and the run resumes — rather than a silent
// give-up after an hour of rewriting.
const ParkLoopExhausted ParkReason = "loop_exhausted"

// ParkLoopStuck is why a flow parked: the step failed twice running with the
// same verdict, so the retry changed nothing it was checking. A loop that is
// not converging is worth stopping before the loop count runs out, because the
// remaining attempts are the same attempt.
const ParkLoopStuck ParkReason = "loop_stuck"

// StepRun is one step's execution.
type StepRun struct {
	ID    string
	Index int
	// Kind is "agent" or "run".
	Kind       string
	Outcome    Outcome
	ParkReason ParkReason
	Note       string
	Dir        string
	AgentName  string
	// Reused marks a step this attempt did not run because a previous attempt
	// already completed it.
	Reused bool
	// Attempt is which try at this step this run is on, counting from one. It is
	// above one only when a backward edge sent the flow through here again, and
	// it is on the record so a reader can tell a step that took forty minutes
	// from a step that took four attempts of ten.
	Attempt   int
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
}

// FlowRun is the record of one execution of a flow.
type FlowRun struct {
	JobID   string
	RunID   string
	RunDir  string
	Outcome Outcome
	// ParkReason is why the flow stopped, taken from the step that stopped
	// it.
	ParkReason ParkReason
	// StoppedAt is the id of the step the flow parked at, empty when every
	// step finished.
	StoppedAt string
	// Steps is what this attempt did, which on a park is only the steps up to
	// and including the one that stopped it. A step a backward edge sent the
	// flow through twice appears twice, in the order it ran: the rejected
	// attempt is part of what happened.
	Steps []StepRun
	// Loops is how many backward edges this run took, whether a declared
	// on_fail edge or one the overwatch chose.
	Loops int
	// Decisions is every answer the overwatch gave, in order. It is on the run
	// record rather than only in the run directory because "why did this flow
	// keep going after step two failed" is a question asked long after the
	// directory has been forgotten, and the answer is nobody's but the
	// overwatch's.
	Decisions []Decision
	// Total is how many steps were declared. It is not len(Steps): a flow
	// that parked at step two of four is 1/4, and saying 1/2 would report the
	// two steps it managed as the whole job.
	Total     int
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
}

// Done reports how many steps completed, of how many declared.
//
// Counted by step id and by the last thing each one did, not by rows: a step a
// backward edge ran twice has two records, and adding them up would report
// 4/3 steps ok — a number that says the flow did more work than it declared
// rather than that it did the same work twice.
func (w *FlowRun) Done() (done, total int) {
	last := map[string]Outcome{}
	for _, s := range w.Steps {
		last[s.ID] = s.Outcome
	}
	for _, outcome := range last {
		if outcome == OutcomeDone {
			done++
		}
	}
	return done, w.Total
}

// Note is the one-line summary of a flow run.
func (w *FlowRun) Note() string {
	done, total := w.Done()
	// Said out loud, because a run that healed itself looks exactly like a run
	// that worked first time, and the difference is forty minutes and a
	// rewritten diff.
	retried := ""
	if w.Loops == 1 {
		retried = ", 1 retry"
	} else if w.Loops > 1 {
		retried = fmt.Sprintf(", %d retries", w.Loops)
	}
	if w.StoppedAt == "" {
		return fmt.Sprintf("%d/%d steps ok%s", done, total, retried)
	}
	reason := string(w.ParkReason)
	if reason == "" {
		reason = "stopped"
	}
	line := fmt.Sprintf("%d/%d steps%s, parked at %s (%s)", done, total, retried, w.StoppedAt, reason)
	if why := w.stoppedStepNote(); why != "" {
		line += " — " + why
	}
	return line
}

// stoppedStepNote is what bermuda observed about the step that stopped the
// flow, when it observed anything.
//
// Only bermuda's own notes travel up. The step's ParkReason is already in the
// line above, so repeating an agent's account of its work would add nothing;
// what is worth carrying is the harness's account of a step that never got as
// far as the work — a quota refusal, an agent that would not start. Without it
// a flow row says only "parked at draft (no_result)", and the fact that the
// step never ran at all lives one directory deeper than anyone looks.
func (w *FlowRun) stoppedStepNote() string {
	const observed = "bermuda: "
	for i := len(w.Steps) - 1; i >= 0; i-- {
		if w.Steps[i].ID != w.StoppedAt {
			continue
		}
		if strings.HasPrefix(w.Steps[i].Note, observed) {
			return strings.TrimPrefix(w.Steps[i].Note, observed)
		}
		return ""
	}
	return ""
}

// flowParkNote is what bermuda observed about a flow's park, in its own words.
//
// The same discipline as parkNote, which this is the flow half of: only the
// parks bermuda actually watched happen get prose, and the step that stopped
// the flow is named because a flow's run directory is the one place that fact
// exists — the steps below it each know their own outcome and none of them
// knows it was the one that ended the run.
//
// `no_result` and `bad_result` stay silent for the reason they do at run level.
// A step that ended without a result already says so in its ParkReason, and a
// result that cannot be read is a file sitting in the step's directory for any
// reader to open; restating either as prose would put words in the mouth of a
// run that observed nothing.
func flowParkNote(reason ParkReason, stoppedAt string) string {
	var what string
	switch reason {
	case ParkStepFailed:
		what = "the step reported failure"
	case ParkLoopExhausted:
		what = "its on_fail edge used every loop it declared"
	case ParkLoopStuck:
		what = "it failed twice running with the same verdict"
	case ParkOverwatch:
		what = "the overwatch stopped it"
	case ParkOverwatchSpent:
		what = "the overwatch spent every decision it was given"
	default:
		// The agent-level parks travel up from the step that parked, and are
		// worded exactly as the run-level note words them: the instruments
		// match those sentences, and a flow's blocked agent is the same fact
		// about the same agent as a bare run's.
		what = parkNote(reason)
	}
	if what == "" {
		return ""
	}
	if stoppedAt == "" {
		// A flow that never reached a step — a definition that failed
		// validation under it — has no step to name.
		return "the flow parked before any step ran: " + what
	}
	return "the flow parked at step " + stoppedAt + ": " + what
}

// recordPark leaves a flow's park where the instruments read, beside the run's
// own artifacts rather than only in the database.
//
// The run half of this landed first (bermuda#103) and deliberately left the
// flow half alone: `Execute` sets a park reason on the FlowRun directly and
// never passes through `classify`, so a flow that parked at a step left its own
// directory wordless — a prompt, a `steps/` tree, and nothing saying which step
// ended it or why. Every instrument that asks why the fleet failed walks these
// directories, and a failure that left no words classifies as `unknown`.
//
// An error wins over the park, as it does for a run: an error describes a
// harness problem — a definition that would not validate, a span that could not
// be cleared — and the park that follows it is aftermath. Best-effort and
// silent, for the same reason: the flow has already stopped, and failing to
// note that must not become a second failure.
func (w *FlowRun) recordPark() {
	if w.RunDir == "" {
		return
	}
	note := ""
	switch {
	case w.Err != nil:
		note = w.Err.Error()
	case w.Outcome == OutcomeParked:
		note = flowParkNote(w.ParkReason, w.StoppedAt)
	}
	if note == "" {
		return
	}
	// The directory is made rather than assumed: a flow that parks on its own
	// definition parks before any step, and steps are what create the tree.
	// Skipping this would leave exactly the failure with no words at all.
	if err := os.MkdirAll(w.RunDir, statefs.Dir); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(w.RunDir, ErrFile), []byte(note+"\n"), statefs.File)
}

// StepDir is where one step's artifacts live: under the run, named by the step,
// so a flow's output is one directory tree and resume can find what an
// earlier attempt left behind.
func StepDir(runDir, stepID string) string {
	return filepath.Join(runDir, "steps", stepID)
}

// Execute runs the job's steps in series and returns what happened.
//
// It is the same call for a first run and for a resume: a step whose directory
// already holds a successful result.json is not run again. Completion therefore
// lives on disk rather than in the database, which is what lets a resume work
// after the process that recorded the first attempt is long gone.
func (w *Flow) Execute(ctx context.Context, job store.Job, def flow.Flow, input, runID, runDir string) (*FlowRun, error) {
	wr := &FlowRun{JobID: job.ID, RunID: runID, RunDir: runDir,
		Total: len(def.Steps), StartedAt: time.Now(), Outcome: OutcomeDone}
	defer func() {
		if wr.EndedAt.IsZero() {
			wr.EndedAt = time.Now()
		}
		wr.recordPark()
	}()

	// Validated again here and not only when the file was read: a flow is a file
	// on disk that anything can edit between the two, and running a sequence
	// nobody checked is how a half-saved edit becomes a half-run flow.
	if err := store.ValidateSteps(def.Steps, job.Model); err != nil {
		wr.Outcome, wr.ParkReason, wr.Err = OutcomeParked, ParkBadResult, err
		return wr, err
	}
	if len(def.Steps) == 0 {
		err := fmt.Errorf("flow %s has no steps", def.ID)
		wr.Outcome, wr.ParkReason, wr.Err = OutcomeParked, ParkBadResult, err
		return wr, err
	}

	// Precedence for the permission bypass: the step, then the flow file, then
	// whatever the caller's job already said. Only an explicit value in the file
	// overrides the job — defaulting it here unconditionally would silently undo
	// a job that deliberately asked for permission checks, which is the opposite
	// of what a default should do.
	//
	// The default itself lives with the caller, because a flow with no opinion
	// should behave like everything else that job started.
	if def.SkipPermissions != nil {
		job.SkipPermissions = *def.SkipPermissions
	}

	// previous is the note the last completed step published, and the only thing
	// besides the input that travels down the chain. It is carried across reused
	// steps too: a resume that skipped step two must still hand step two's result
	// to step three, or the resumed run is not the run that was interrupted.
	previous := ""

	// What the backward edges have spent so far, read from the run directory
	// rather than started fresh.
	//
	// This is the difference between a bound and a suggestion. The counters used
	// to live in this function, so `flow resume` began every attempt with a full
	// budget — and the thing that resumes a parked run is not always a human
	// deciding it is worth another go: a self-heal job that unparks whatever
	// broke overnight would refill an exhausted loop every morning, forever, at
	// the cost of a whole flow each time. On disk, next to the results, the
	// spend survives the process that spent it.
	ledger := loopLedger{}
	if !w.ResetLoops {
		ledger = readLoopLedger(runDir)
	}
	// Every flow has an overwatch, and a flow that declared none gets the
	// standard one. Resolved once here rather than per consult, so a run
	// cannot change its own supervision halfway through.
	watcher := def.Watcher()
	// Whether anyone is watching at all. A flow of shell steps that declared no
	// overwatch is not given one: see flow.Flow.Applies.
	overseen := def.Applies()

	// Which try at each step this run is on.
	attempts := map[string]int{}
	// The rejection waiting to be handed to the step a backward edge jumped to.
	var pending *retry

	for i := 0; i < len(def.Steps); i++ {
		step := def.Steps[i]
		sr := StepRun{ID: step.ID, Index: i, Kind: step.Label(),
			Dir: StepDir(runDir, step.ID)}

		if res, err := readResult(sr.Dir); err == nil && res.Status == "ok" {
			// An earlier attempt already did this one. Not re-running it is the
			// whole point of resume: the expensive steps are exactly the ones a
			// human would otherwise be tempted to wave through.
			sr.Outcome, sr.Reused, sr.Note = OutcomeDone, true, res.Note
			sr.Attempt = attempts[step.ID]
			wr.Steps = append(wr.Steps, sr)
			previous = res.Note
			continue
		}

		attempts[step.ID]++
		sr.Attempt = attempts[step.ID]
		sr.StartedAt = time.Now()
		sr.Outcome = OutcomeRunning
		w.report(sr)

		vals := flow.Values{Input: input, Previous: previous}
		// Expanded here rather than when the file was read, because {{previous}}
		// is not knowable until the step before it has finished.
		step = flow.Expand(step, vals)
		if pending != nil && pending.target == step.ID {
			// After the expansion, so a verdict that happens to contain braces is
			// text rather than a placeholder.
			step = pending.brief(step, sr.Attempt)
			pending = nil
		}

		if err := w.prepare(sr.Dir); err != nil {
			sr.Outcome, sr.ParkReason, sr.Err = OutcomeParked, ParkNoResult, err
		} else if step.IsAgent() {
			w.runAgentStep(ctx, job, step, vals, runID, &sr)
		} else {
			w.runCommandStep(ctx, job, step, vals, &sr)
		}
		previous = sr.Note
		sr.EndedAt = time.Now()
		w.report(sr)
		wr.Steps = append(wr.Steps, sr)

		if overseen && sr.Outcome == OutcomeDone && watcher.Watch == flow.WatchEveryStep {
			// The flow asked for a reader on every result, not only on the
			// ones that went wrong. `continue` is the expected answer and
			// costs one agent call per step, which is why this cadence is
			// something a flow declares rather than something it inherits.
			d := w.consult(ctx, job, def, watcher, wr, runID, runDir,
				overwatchTrigger{StepID: step.ID, Settled: true})
			wr.Decisions = append(wr.Decisions, d)
			act, next := w.actOn(d, def, wr, runDir, i, step.ID)
			switch act {
			case owStop:
				return wr, wr.Err
			case owJump:
				pending = nil
				i = next
				continue
			}
		}

		if sr.Outcome == OutcomeFailed && step.OnFail != nil {
			// A declared backward edge: this step judged the work and rejected
			// it, and the step that produced the work gets to try again.
			//
			// Only OutcomeFailed loops. A step that parked wrote no verdict —
			// it died, or it timed out, or it is waiting on a human — and
			// rewriting code on that signal is a heal loop against an
			// environment failure, which never converges because nothing was
			// wrong with the code.
			target, reason, err := w.loopBack(def.Steps, i, runDir, &ledger, sr.Note)
			switch {
			case err != nil:
				// The span could not be cleared, so a retry would hand the
				// verifier steps it thinks are already done. That is a harness
				// problem, not a verdict.
				wr.Outcome, wr.StoppedAt, wr.Err = OutcomeParked, step.ID, err
				wr.ParkReason = ParkNoResult
				return wr, err
			case target >= 0:
				wr.Loops++
				// previous is already this step's note, so the step being
				// re-run is handed the verdict that rejected it.
				pending = &retry{target: def.Steps[target].ID, from: step.ID, verdict: sr.Note}
				i = target - 1
				continue
			}
			// Out of loops, or looping without moving. The declared edge has
			// had its say and the run is about to stop, which is exactly the
			// moment the overwatch exists for: it is the only reader that can
			// tell "this loop is not converging" from "this loop was pointed
			// at the wrong step".
			//
			// The reason the edge ran out is kept as the default, so an
			// overwatch that adds nothing leaves the record saying what it
			// said before — "it failed" and "it failed the same way three
			// times" are different things to be told.
			wr.Outcome, wr.StoppedAt, wr.ParkReason = OutcomeParked, step.ID, reason
			if !overseen {
				return wr, nil
			}
			d := w.consult(ctx, job, def, watcher, wr, runID, runDir,
				overwatchTrigger{StepID: step.ID, Why: string(reason)})
			wr.Decisions = append(wr.Decisions, d)
			act, next := w.actOn(d, def, wr, runDir, i, step.ID)
			switch act {
			case owStop:
				return wr, wr.Err
			case owJump:
				pending = nil
				i = next
				continue
			}
			// carry on: the overwatch accepted the failed step
			previous = sr.Note
			continue
		}

		if sr.Outcome != OutcomeDone {
			// Park here. B must not run on A's unverified claim, and a dead A
			// must not be invisible.
			wr.Outcome, wr.StoppedAt = OutcomeParked, step.ID
			if sr.Outcome == OutcomeFailed {
				// A step reporting failure is an outcome, not an error: the
				// flow did exactly what it was built to do. Only a harness
				// problem — a launcher that could not start, a directory that
				// could not be written — is returned as an error.
				wr.ParkReason = ParkStepFailed
			} else {
				wr.Err = sr.Err
				wr.ParkReason = sr.ParkReason
				if wr.ParkReason == "" {
					wr.ParkReason = ParkNoResult
				}
			}
			// Everything above is what the harness decides on its own, and it
			// is what happens if the overwatch has nothing to add. Asking it
			// here rather than earlier keeps that true: the park is already
			// recorded, and a decision only ever moves the run off it.
			if !overseen {
				return wr, wr.Err
			}
			why := string(wr.ParkReason)
			if sr.Outcome == OutcomeFailed {
				why = "it reported failure"
			}
			d := w.consult(ctx, job, def, watcher, wr, runID, runDir,
				overwatchTrigger{StepID: step.ID, Why: why})
			wr.Decisions = append(wr.Decisions, d)
			act, next := w.actOn(d, def, wr, runDir, i, step.ID)
			switch act {
			case owStop:
				return wr, wr.Err
			case owJump:
				pending = nil
				i = next
				continue
			}
			previous = sr.Note
			continue
		}
	}
	return wr, nil
}

// loopBack decides what a failed step's on_fail edge does, and clears the way
// if the answer is "go back".
//
// It returns the index to resume at, or -1 and the reason the flow parks
// instead. The bookkeeping maps are the caller's, keyed by the step that owns
// the edge, and are updated here so the decision and its cost live together.
func (w *Flow) loopBack(steps []store.Step, at int, runDir string,
	ledger *loopLedger, verdict string) (int, ParkReason, error) {

	step := steps[at]
	of := step.OnFail
	if ledger.spent(step.ID) >= of.Loops() {
		return -1, ParkLoopExhausted, nil
	}
	// Compared to the previous rejection rather than to some notion of progress,
	// because the verdict is the only thing the harness can read. Two identical
	// rejections mean the retry changed nothing this step can see, and the loops
	// left would spend the same tokens to be told the same thing. An empty
	// verdict is not evidence of anything, so it does not park the run.
	if trimmed := strings.TrimSpace(verdict); trimmed != "" && trimmed == ledger.verdict(step.ID) {
		return -1, ParkLoopStuck, nil
	}

	target := store.StepIndex(steps, of.Goto)
	if target < 0 || target >= at {
		// ValidateSteps refuses both of these before a flow can run, so reaching
		// here means the definition changed under the run.
		return -1, "", fmt.Errorf("step %s sends on_fail to %q, which is not a step declared before it",
			step.ID, of.Goto)
	}
	// Every step from the target through this one, not just the target. The
	// reuse path skips any step whose result.json says ok, so leaving the steps
	// in between would re-run the maker and then hand this step the same
	// verdict it just rejected, produced by an attempt that never happened.
	//
	// Kept rather than deleted: the rejected attempt's verdict beside the
	// attempt that replaced it is what makes a loop that parked readable
	// afterwards, and the file it is renamed out of the way of is the only one
	// anything reads.
	attempt := ledger.spent(step.ID) + 1
	if err := clearSpan(steps, target, at, runDir, attempt); err != nil {
		return -1, "", err
	}
	ledger.record(step.ID, verdict)
	// Written before the retry runs, not after it settles. A crash between the
	// two would otherwise lose the attempt that was just paid for, and the loop
	// this bounds is one that costs an agent every time round.
	if err := writeLoopLedger(runDir, ledger); err != nil {
		// A budget nobody can persist is not a budget. Parking here is the same
		// treatment the un-clearable span gets above: a harness problem, not a
		// verdict about the work.
		return -1, "", err
	}
	return target, "", nil
}

// loopLedger is what one run's backward edges have spent, by the step that
// spent it.
//
// It lives in the run directory for the same reason completion does: a resume
// is a different process, often a different day, and anything held only in
// memory is a bound that quietly resets. Per step rather than per run because
// two checkers in one flow are two budgets — healing once at step two must not
// leave step five with nothing.
type loopLedger struct {
	// Loops is how many times each step has taken its edge.
	Loops map[string]int `json:"loops,omitempty"`
	// Verdicts is what each step last rejected on, which is how a loop that is
	// going nowhere is told from one that is converging. It is here rather than
	// in memory so a resume does not have to spend an attempt rediscovering that
	// the verdict has not moved.
	Verdicts map[string]string `json:"verdicts,omitempty"`
}

func (l *loopLedger) spent(stepID string) int      { return l.Loops[stepID] }
func (l *loopLedger) verdict(stepID string) string { return l.Verdicts[stepID] }

// record charges one loop to a step.
func (l *loopLedger) record(stepID, verdict string) {
	if l.Loops == nil {
		l.Loops = map[string]int{}
	}
	if l.Verdicts == nil {
		l.Verdicts = map[string]string{}
	}
	l.Loops[stepID]++
	l.Verdicts[stepID] = strings.TrimSpace(verdict)
}

// loopLedgerPath is where a run keeps it: beside the steps, inside the run
// directory, so it is copied, archived and deleted with everything else the run
// produced.
func loopLedgerPath(runDir string) string { return filepath.Join(runDir, "loops.json") }

// readLoopLedger reads what has been spent. A run that never looped has no
// file, and an unreadable one reads as empty — a corrupt ledger must not stop a
// flow, and the loop it fails to bound is still bounded by the step count and
// by the no-progress check.
func readLoopLedger(runDir string) loopLedger {
	var l loopLedger
	b, err := os.ReadFile(loopLedgerPath(runDir))
	if err != nil {
		return l
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return loopLedger{}
	}
	return l
}

func writeLoopLedger(runDir string, l *loopLedger) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runDir, statefs.Dir); err != nil {
		return err
	}
	return os.WriteFile(loopLedgerPath(runDir), b, statefs.File)
}

// retry is the rejection a backward edge carries to the step it jumped to.
//
// {{previous}} already holds the verdict, but a maker step usually reads
// {{input}} and never mentions {{previous}} — it is the first step of its span,
// and the value it would show is the note of whatever ran before it. Without
// this the agent is re-run with the prompt that produced the rejected work and
// no idea it was rejected, which reliably produces the same work again.
type retry struct {
	// target is the step being re-run, and from is the step that rejected it.
	target string
	from   string
	// verdict is the rejecting step's published note.
	verdict string
}

// brief prepends the rejection to a step's prompt. A run step is returned
// untouched: it reads $BERMUDA_PREVIOUS, and rewriting a shell command would
// corrupt any command that legitimately contains the text.
func (r *retry) brief(s store.Step, attempt int) store.Step {
	if !s.IsAgent() {
		return s
	}
	verdict := strings.TrimSpace(r.verdict)
	if verdict == "" {
		// A step that failed without saying why still has to be reported as a
		// rejection, or the retry reads as an unexplained re-run.
		verdict = "(it reported failure without saying why — the thread is the only record of what it saw)"
	}
	s.Agent = fmt.Sprintf(`This is attempt %d at this step. Attempt %d was rejected by step %q:

%s

Read this run's thread before changing anything — every attempt's findings are in it — and address that verdict. Producing the same work again produces the same rejection.

---

%s`, attempt, attempt-1, r.from, verdict, s.Agent)
	return s
}

func (w *Flow) report(sr StepRun) {
	if w.Report != nil {
		w.Report(sr)
	}
}

// prepare makes the step's directory and clears any verdict left by a previous
// attempt.
//
// Removing the old result.json is what keeps a retry honest: an agent that dies
// without writing one would otherwise inherit the last attempt's file and be
// classified by a result it never produced.
func (w *Flow) prepare(dir string) error {
	if err := os.MkdirAll(dir, statefs.Dir); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "result.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// runAgentStep launches one agent and takes its verdict from result.json.
func (w *Flow) runAgentStep(ctx context.Context, job store.Job, step store.Step, vals flow.Values, runID string, sr *StepRun) {
	if w.Launch == nil {
		sr.Outcome, sr.ParkReason = OutcomeParked, ParkNoResult
		sr.Err = fmt.Errorf("step %s: no agent launcher", step.ID)
		return
	}
	j := StepJob(job, step)
	if w.Space.Usable() {
		// Every step of one run in one space, so all of them are in the thread that
		// space owns. A step whose space has gone falls back inside the launcher
		// rather than failing here.
		j.WorkspaceID = w.Space.WorkspaceID
	}
	if w.Space.HasThread() {
		// Told on every step, because a step is a fresh agent that has read
		// nothing: a shared thread nobody mentions is a thread nobody writes to.
		j.Prompt += ThreadContract(w.Space.Thread)
	}
	// BERMUDA_STEP_DIR names the same directory as BERMUDA_RUN_DIR, which the
	// launcher injects. Both are given because a step is a run to the agent
	// writing result.json, and a step to the flow reading it.
	j.Env = map[string]string{
		"BERMUDA_STEP_DIR": sr.Dir,
		"BERMUDA_STEP_ID":  step.ID,
		"BERMUDA_RUN_ID":   runID,
		// The prompt has already been interpolated, so these are here for a step
		// that would rather read a long input from the environment than have it
		// pasted into its prompt — a stack trace, a diff, anything whose bulk
		// would drown the instruction it was meant to illustrate.
		flow.EnvInput:    vals.Input,
		flow.EnvPrevious: vals.Previous,
	}
	if w.Space.HasThread() {
		// So `bermuda thread post` inside the step needs no flag. The prompt above
		// says to post; a flag it has to remember on every call is a flag it
		// eventually forgets, and the message then lands in global where nobody in
		// this flow is reading.
		j.Env[EnvThread] = w.Space.Thread
	}
	if w.Check != "" {
		// Same rule for the checklist: `bermuda check add '<what I found>'` inside
		// a step lands on this run's page with no id to remember.
		j.Env[EnvCheck] = w.Check
	}
	// The step id is part of the agent's run id, so every step of a flow is
	// a differently named agent. That is what makes "a different subagent is a
	// different agent" true by construction rather than by remembering: there is
	// no path here that hands one step's live agent to the next.
	run, err := w.Launch(ctx, j, stepRunID(runID, step.ID, sr.Attempt), sr.Dir)
	if run != nil {
		sr.Outcome, sr.ParkReason, sr.AgentName = run.Outcome, run.ParkReason, run.AgentName
		// Run.Note falls back to what bermuda observed when the step wrote no
		// result.json, so a step that never got an agent started says why
		// instead of leaving the flow's record blank at exactly the point it
		// stopped.
		sr.Note = run.Note()
		if run.Err != nil {
			sr.Err = run.Err
		}
	}
	if err != nil {
		// A launcher that could not start the agent leaves nothing to judge, so
		// the step parks rather than being called a failure of the work.
		sr.Err = err
		if run == nil || sr.Outcome == "" {
			sr.Outcome, sr.ParkReason = OutcomeParked, ParkNoResult
		}
	}
	if sr.Outcome == "" {
		sr.Outcome, sr.ParkReason = OutcomeParked, ParkNoResult
	}
}

// runCommandStep executes a `run:` step and writes its own result.json.
//
// The harness writes the result file for a command step, so a step's completion
// is one predicate for both kinds: result.json says ok. Nothing has to know
// whether the work was done by an agent or by the shell, which is what lets
// resume skip either one.
func (w *Flow) runCommandStep(ctx context.Context, job store.Job, step store.Step, vals flow.Values, sr *StepRun) {
	shell := w.Shell
	if shell == nil {
		shell = sh
	}
	// A command has no prompt to interpolate, so the two values a flow carries
	// reach it where a shell already looks. Rewriting the command text instead
	// would corrupt any command that legitimately contains braces.
	env := append(os.Environ(),
		"BERMUDA_STEP_DIR="+sr.Dir,
		"BERMUDA_RUN_DIR="+sr.Dir,
		"BERMUDA_STEP_ID="+step.ID,
		"BERMUDA_JOB_ID="+job.ID)
	env = append(env, vals.Env()...)
	if w.Space.HasThread() {
		// A command step has no prompt to instruct, but a script that wants to say
		// something — a test runner reporting which suite is flaky — reaches the
		// same conversation the agent steps are in.
		env = append(env, EnvThread+"="+w.Space.Thread)
	}
	if w.Check != "" {
		env = append(env, EnvCheck+"="+w.Check)
	}

	out, err := shell(ctx, step.Run, job.CWD, env)
	// Kept for a human, never parsed: the exit status is the verdict.
	if len(out) > 0 {
		_ = os.WriteFile(filepath.Join(sr.Dir, "output.txt"), out, statefs.File)
	}

	res := Result{Status: "ok", Note: lastLine(out)}
	sr.Outcome = OutcomeDone
	if err != nil {
		// The exit status says it failed; the last line of output usually says
		// why, and a silent command leaves the status to speak alone.
		res.Status, res.Note = "error", err.Error()
		if l := lastLine(out); l != "" {
			res.Note += ": " + l
		}
		sr.Outcome, sr.Err = OutcomeFailed, err
	}
	sr.Note = res.Note
	if writeErr := writeResult(sr.Dir, res); writeErr != nil {
		// Without the file the step has no record of having completed, so a
		// resume would run it again — which for a command step is usually
		// harmless but is not something to do silently.
		sr.Outcome, sr.ParkReason = OutcomeParked, ParkNoResult
		sr.Err = writeErr
	}
}

// sh is the default command runner.
func sh(ctx context.Context, command, cwd string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd
	cmd.Env = env
	return cmd.CombinedOutput()
}

func writeResult(dir string, res Result) error {
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "result.json"), b, statefs.File)
}

// lastLine is the most useful single line of a command's output: the last
// non-empty one, which is where a failing command puts its complaint.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// stepRunID is the run id one step is launched under. It carries the step id so
// the agent name derived from it names the step, and so two steps of the same
// flow can never be the same agent.
//
// A retry carries its attempt too, and that is not cosmetic: a step that failed
// keeps its tab open for a human to look at, so the rejected attempt's agent is
// still there under the name the next attempt would ask for. Two agents cannot
// share a name — the retry would be refused, or worse, handed the agent that
// produced the work it is supposed to redo.
func stepRunID(runID, stepID string, attempt int) string {
	if attempt > 1 {
		return fmt.Sprintf("%s-%s-a%d", runID, stepID, attempt)
	}
	return runID + "-" + stepID
}
