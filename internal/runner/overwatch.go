package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bon5co/bermuda/v3/internal/flow"
	"github.com/bon5co/bermuda/v3/internal/statefs"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// The overwatch: the one reader that holds the whole run.
//
// Every step is a fresh agent by design, which is what stops step four
// inheriting step one's confusion — and it leaves nobody able to answer "given
// everything that has happened, what should this run do now". The harness could
// only ever park and wait for a person. That is the right answer when nobody
// knows why a step failed and a waste of a night when the answer is legible
// from two steps up.
//
// So at every point where a run would otherwise stop, an agent that has been
// handed the flow definition, every step's outcome, and the failure's own
// artifacts is asked what to do. Three things keep that from becoming a way to
// wave work through:
//
//  1. A declared `on_fail` edge wins. It is explicit, it is free, and an agent
//     overruling what the file says is the failure this tool exists to prevent.
//     Overwatch is consulted where the flow would park, including when an edge
//     has run out — not instead of the edge.
//  2. `skip` is not in the default allow-list. Accepting a failed step is the
//     one decision that breaks what a flow is for, so a flow that wants it has
//     to say so in the file where a reviewer sees it.
//  3. Anything unreadable parks. No decision file, unparseable JSON, a verb the
//     flow does not permit, a goto naming a step that does not exist or has not
//     run — every one of them parks with the reason named. Ambiguity resolves
//     towards stopping, never towards continuing.

// DecisionFile is what the overwatch agent writes, in its own directory.
//
// A separate file from result.json because the two say different things: the
// result says whether the overwatch agent itself did its job, and this says
// what the run should do. Collapsing them would make "the overwatch crashed"
// and "the overwatch says stop" the same record.
const DecisionFile = "decision.json"

// Decision is the overwatch's answer.
type Decision struct {
	// Decision is one of flow.Decide*.
	Decision string `json:"decision"`
	// Step is the step to go back to, for a goto.
	Step string `json:"step,omitempty"`
	// Why is the reasoning, in the overwatch's own words. It is recorded
	// whatever the decision, because the run's record is the only place a
	// person later reconstructs what the harness was thinking.
	Why string `json:"why,omitempty"`
}

// ParkOverwatch is why a flow parked: its overwatch said so, or could not say
// anything usable. The distinction lives in the note, not in the reason —
// both mean the run stopped with a reader having looked at it.
const ParkOverwatch ParkReason = "overwatch"

// ParkOverwatchSpent is why a flow parked: the overwatch used its whole budget
// of decisions and the run is still not finished. It is a park like an
// exhausted loop — the attempts are on disk and the run resumes — rather than
// an agent left deciding all night.
const ParkOverwatchSpent ParkReason = "overwatch_spent"

// overwatchLedger is what the overwatch has spent, kept on disk beside the loop
// ledger and for the same reason: a resume must not refill a budget.
type overwatchLedger struct {
	Consults int `json:"consults"`
}

func overwatchLedgerPath(runDir string) string {
	return filepath.Join(runDir, "overwatch.json")
}

func readOverwatchLedger(runDir string) overwatchLedger {
	var l overwatchLedger
	data, err := os.ReadFile(overwatchLedgerPath(runDir))
	if err != nil {
		return l
	}
	_ = json.Unmarshal(data, &l)
	return l
}

func writeOverwatchLedger(runDir string, l overwatchLedger) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(overwatchLedgerPath(runDir), data, statefs.File)
}

// overwatchDir is where one consult's artifacts live. Outside steps/, because
// the overwatch is not a step and a reader walking steps/ is asking what the
// flow declared.
func overwatchDir(runDir string, consult int) string {
	return filepath.Join(runDir, "overwatch", fmt.Sprintf("%d", consult))
}

// consult asks the overwatch what the run should do, and returns a decision it
// is allowed to act on.
//
// It never returns an error for a decision it did not like: an overwatch that
// cannot be understood is a park, with the reason on the record. The only
// errors here are the harness's own — a directory that could not be made.
func (w *Flow) consult(ctx context.Context, job store.Job, def flow.Flow, cfg flow.Overwatch,
	wr *FlowRun, runID, runDir string, trigger overwatchTrigger) Decision {

	ledger := readOverwatchLedger(runDir)
	if ledger.Consults >= cfg.Budget {
		return Decision{Decision: flow.DecidePark, Why: fmt.Sprintf(
			"the overwatch has spent its whole budget of %d decision(s) on this run", cfg.Budget)}
	}
	if w.Launch == nil {
		// A flow of `run:` steps in a harness with no launcher. Nothing to ask,
		// and pretending otherwise would park with a reason that reads like the
		// overwatch had an opinion.
		return Decision{Decision: flow.DecidePark, Why: "no agent launcher, so nothing could be asked"}
	}

	// The consult gets its own deadline. Without one, a job with no timeout --
	// which is the default -- let an overwatch that never answered hold a run
	// that would otherwise have parked in milliseconds.
	ctx, cancel := context.WithTimeout(ctx, cfg.Wait())
	defer cancel()

	consult := ledger.Consults + 1
	dir := overwatchDir(runDir, consult)
	if err := w.prepare(dir); err != nil {
		return Decision{Decision: flow.DecidePark, Why: "the overwatch's directory could not be written: " + err.Error()}
	}

	step := store.Step{
		ID:     flow.OverwatchStepID,
		Agent:  overwatchBrief(def, cfg, wr, trigger),
		Model:  cfg.Model,
		Effort: cfg.Effort,
		Kind:   cfg.Kind,
	}
	sr := StepRun{ID: step.ID, Index: -1, Kind: "overwatch", Dir: dir, Attempt: consult}
	w.runAgentStep(ctx, job, step, flow.Values{}, runID, &sr)

	ledger.Consults = consult
	if err := writeOverwatchLedger(runDir, ledger); err != nil {
		// The budget is a bound, and a bound that failed to record itself has
		// to stop the run rather than be silently reset by the next consult.
		return Decision{Decision: flow.DecidePark, Why: "the overwatch's budget could not be recorded: " + err.Error()}
	}

	d, err := readDecision(dir)
	if err != nil {
		return Decision{Decision: flow.DecidePark,
			Why: "the overwatch left no decision this run could act on: " + err.Error()}
	}
	return sanitise(d, cfg, def, wr)
}

// readDecision reads decision.json out of a consult's directory.
func readDecision(dir string) (Decision, error) {
	var d Decision
	data, err := os.ReadFile(filepath.Join(dir, DecisionFile))
	if err != nil {
		if os.IsNotExist(err) {
			return d, fmt.Errorf("no %s in %s", DecisionFile, dir)
		}
		return d, err
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return d, fmt.Errorf("%s is not readable JSON: %w", DecisionFile, err)
	}
	d.Decision = strings.ToLower(strings.TrimSpace(d.Decision))
	d.Step = strings.TrimSpace(d.Step)
	d.Why = strings.TrimSpace(d.Why)
	if d.Decision == "" {
		return d, fmt.Errorf("%s names no decision", DecisionFile)
	}
	return d, nil
}

// sanitise turns whatever the overwatch said into something the run may act on.
//
// Every rejection becomes a park that names what was wrong with the answer,
// because the alternative — treating an unusable decision as "carry on" — is
// how an agent's confusion becomes a flow that ran its remaining steps on a
// failure nobody read.
func sanitise(d Decision, cfg flow.Overwatch, def flow.Flow, wr *FlowRun) Decision {
	park := func(why string) Decision {
		return Decision{Decision: flow.DecidePark, Why: why + parenthetical(d.Why)}
	}
	switch d.Decision {
	case flow.DecidePark, flow.DecideContinue, flow.DecideAbort,
		flow.DecideRetry, flow.DecideGoto, flow.DecideSkip:
	default:
		return park(fmt.Sprintf("the overwatch answered %q, which is not a decision", d.Decision))
	}
	if !cfg.Permits(d.Decision) {
		return park(fmt.Sprintf("the overwatch chose %q, which this flow does not allow", d.Decision))
	}
	if d.Decision == flow.DecideGoto {
		if d.Step == "" {
			return park("the overwatch chose goto without naming a step")
		}
		if !declares(def, d.Step) {
			return park(fmt.Sprintf("the overwatch chose goto %q, which this flow has no step called", d.Step))
		}
		if !hasRun(wr, d.Step) {
			// Forward edges are branches, and a flow is a series. The same rule
			// a declared on_fail edge is held to.
			return park(fmt.Sprintf("the overwatch chose goto %q, which has not run yet — "+
				"a flow goes back, never forward", d.Step))
		}
	}
	return d
}

// parenthetical appends the overwatch's own reasoning to a rejection, so the
// record keeps what it thought even when the run could not do it.
func parenthetical(why string) string {
	if why == "" {
		return ""
	}
	return " (it said: " + why + ")"
}

// declares reports whether the flow has a step with this id.
func declares(def flow.Flow, id string) bool {
	for _, s := range def.Steps {
		if s.ID == id {
			return true
		}
	}
	return false
}

// hasRun reports whether this run has already executed a step.
func hasRun(wr *FlowRun, id string) bool {
	for _, s := range wr.Steps {
		if s.ID == id {
			return true
		}
	}
	return false
}

// overwatchTrigger says why the overwatch is being consulted, which is the
// first thing its brief has to answer.
type overwatchTrigger struct {
	// StepID is the step the run is sitting on.
	StepID string
	// Why is the harness's own account of what happened, in the words it uses
	// everywhere else: a park reason, an exhausted loop, a plain failure.
	Why string
	// Settled is true when the step finished normally and the overwatch is
	// being consulted only because the flow asked to see every step.
	Settled bool
}

// overwatchBrief writes the prompt.
//
// It is long on purpose. This agent is being asked to take a decision about
// work it did not do, and the failure mode of a short brief here is an
// overwatch that guesses -- so it gets the flow as declared, the run as it
// happened, the failure's own artifacts, and an explicit statement of which
// decisions it may return and what each one costs.
func overwatchBrief(def flow.Flow, cfg flow.Overwatch, wr *FlowRun, t overwatchTrigger) string {
	var b strings.Builder
	b.WriteString("You are the overwatch for a bermuda flow: the one agent that sees the whole run.\n")
	b.WriteString("Every step in this flow was a separate agent that read nothing but its own prompt.\n")
	b.WriteString("You have what none of them had, which is all of it.\n\n")

	b.WriteString("# The flow as declared\n\n")
	fmt.Fprintf(&b, "flow: %s\n", def.ID)
	if def.About != "" {
		fmt.Fprintf(&b, "about: %s\n", def.About)
	}
	b.WriteString("\nsteps, in order:\n")
	for i, s := range def.Steps {
		fmt.Fprintf(&b, "  %d. %s (%s)\n", i+1, s.ID, s.Label())
		if s.OnFail != nil {
			fmt.Fprintf(&b, "     on_fail: goto %s, max %d\n", s.OnFail.Goto, s.OnFail.Loops())
		}
		if line := firstLine(s.Agent, s.Run); line != "" {
			fmt.Fprintf(&b, "     %s\n", line)
		}
	}

	b.WriteString("\n# What has happened so far\n\n")
	if len(wr.Steps) == 0 {
		b.WriteString("nothing has run yet.\n")
	}
	for _, s := range wr.Steps {
		fmt.Fprintf(&b, "- %s: %s", s.ID, s.Outcome)
		if s.Attempt > 1 {
			fmt.Fprintf(&b, " (attempt %d)", s.Attempt)
		}
		if s.Reused {
			b.WriteString(" (reused from an earlier attempt)")
		}
		if s.ParkReason != "" {
			fmt.Fprintf(&b, " [%s]", s.ParkReason)
		}
		b.WriteString("\n")
		if s.Note != "" {
			fmt.Fprintf(&b, "    said: %s\n", oneLine(s.Note))
		}
		if s.Err != nil {
			fmt.Fprintf(&b, "    error: %s\n", oneLine(s.Err.Error()))
		}
		fmt.Fprintf(&b, "    artifacts: %s\n", s.Dir)
	}
	if wr.Loops > 0 {
		fmt.Fprintf(&b, "\nbackward edges taken so far: %d\n", wr.Loops)
	}

	b.WriteString("\n# Why you are being asked\n\n")
	if t.Settled {
		fmt.Fprintf(&b, "Step %s finished and this flow asks to see every step.\n", t.StepID)
		b.WriteString("If nothing is wrong, answer `continue`. That is the expected answer.\n")
	} else {
		fmt.Fprintf(&b, "Step %s did not complete: %s.\n", t.StepID, t.Why)
		b.WriteString("Without you this run parks here and waits for a person.\n")
	}

	b.WriteString("\n# Read before deciding\n\n")
	b.WriteString("The artifacts directory of each step above holds what it actually wrote — its\n")
	b.WriteString("result.json, its output. Read the ones that matter before answering. A decision\n")
	b.WriteString("taken from the summary above alone is a guess, and a guess here costs a whole\n")
	b.WriteString("run.\n")

	b.WriteString("\n# Your decision\n\n")
	b.WriteString("Write " + DecisionFile + " in your own run directory ($BERMUDA_STEP_DIR):\n\n")
	b.WriteString("    {\"decision\": \"<one of below>\", \"step\": \"<step id, goto only>\", \"why\": \"<one or two sentences>\"}\n\n")
	b.WriteString("Then write result.json as any step does, saying whether you managed to decide.\n\n")
	b.WriteString("Decisions this flow allows:\n\n")
	for _, d := range describeDecisions(cfg, t) {
		b.WriteString("  " + d + "\n")
	}
	b.WriteString("\nAnything else — no file, unreadable JSON, a decision this flow does not allow,\n")
	b.WriteString("a goto naming a step that has not run — parks the run. Ambiguity stops the run;\n")
	b.WriteString("it never carries it on.\n")

	b.WriteString("\n# You decide; you do not do the work\n\n")
	b.WriteString("You have the same tools the steps had, and you must not use them to do a\n")
	b.WriteString("step's work. Read anything; change nothing the steps own. A fix you apply\n")
	b.WriteString("yourself is a change no step made, no step reported, and no reviewer saw —\n")
	b.WriteString("the run's record would then describe work that did not happen where it says\n")
	b.WriteString("it happened. If something needs doing, send the run back to the step whose\n")
	b.WriteString("job it is.\n")

	b.WriteString("\n# What you are for\n\n")
	b.WriteString("Park is the right answer whenever the cause is not legible from what you can\n")
	b.WriteString("read. You exist to catch the cases where it is: a step that failed on something\n")
	b.WriteString("an earlier step got wrong, a transient that will pass, a verifier rejecting work\n")
	b.WriteString("the writer never saw the verdict on. You are not here to get the run finished.\n")
	b.WriteString("A run that stops with a clear reason is a good outcome; a run that finishes on\n")
	b.WriteString("work nobody checked is not.\n")

	if strings.TrimSpace(cfg.Brief) != "" {
		b.WriteString("\n# From the flow itself\n\n")
		b.WriteString(strings.TrimSpace(cfg.Brief) + "\n")
	}
	return b.String()
}

// describeDecisions lists the verbs this consult may return, each with what it
// costs, so the overwatch is choosing between known consequences.
func describeDecisions(cfg flow.Overwatch, t overwatchTrigger) []string {
	var out []string
	if t.Settled {
		out = append(out, "`continue` — nothing needs doing. The run carries on to the next step.")
	}
	if cfg.Permits(flow.DecideRetry) && !t.Settled {
		out = append(out, "`retry` — run this same step again, unchanged. For a transient: a "+
			"network blip, a tool that was busy. Pointless if the input was wrong.")
	}
	if cfg.Permits(flow.DecideGoto) {
		out = append(out, "`goto` with `step` — go back to an earlier step that has already run, "+
			"and re-run from there. For when the fault is upstream of where it surfaced.")
	}
	out = append(out, "`park` — stop here, resumable. Everything finished stays done and a person "+
		"picks it up. Choose this whenever you are not sure.")
	if cfg.Permits(flow.DecideAbort) {
		out = append(out, "`abort` — stop, and say this run is not worth resuming.")
	}
	if cfg.Permits(flow.DecideSkip) {
		out = append(out, "`skip` — accept the failed step and carry on to the next one. This flow "+
			"has explicitly allowed it. It means the steps after this one run on work that did "+
			"not pass; be certain.")
	}
	return out
}

// firstLine is the opening line of whichever of a step's bodies is set,
// shortened, so the brief describes a step without pasting a page of prompt.
func firstLine(agent, run string) string {
	body := strings.TrimSpace(agent)
	if body == "" {
		body = strings.TrimSpace(run)
	}
	if body == "" {
		return ""
	}
	line := oneLine(body)
	const max = 120
	if len(line) > max {
		line = line[:max] + "…"
	}
	return line
}

// oneLine flattens text so a summary stays a summary.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// overwatchAction is what the run does with a decision.
type overwatchAction int

const (
	// owCarryOn proceeds as if the overwatch had not been asked: the run
	// continues to the next step. It is what `continue` and `skip` mean.
	owCarryOn overwatchAction = iota
	// owJump sets the loop index; the caller continues from there.
	owJump
	// owStop returns the run. The FlowRun has already been given its outcome.
	owStop
)

// actOn turns a sanitised decision into what the run does next.
//
// The park it is handed is already recorded on the FlowRun before this is
// called, which is deliberate: a decision can only ever move a run *off* a
// park, never onto one it would not have taken anyway. If anything here fails,
// what the harness had already decided stands.
func (w *Flow) actOn(d Decision, def flow.Flow, wr *FlowRun, runDir string, at int, stepID string) (overwatchAction, int) {
	switch d.Decision {
	case flow.DecideContinue, flow.DecideSkip:
		if d.Decision == flow.DecideSkip {
			// Named on the record, because a run that carried on past a failed
			// step looks from the outside exactly like a run where nothing
			// failed, and the difference is a step nobody checked.
			wr.Outcome, wr.ParkReason, wr.StoppedAt = OutcomeDone, "", ""
		}
		return owCarryOn, 0

	case flow.DecideRetry:
		wr.Loops++
		wr.Outcome, wr.ParkReason, wr.StoppedAt = OutcomeDone, "", ""
		// One before, so the caller's i++ lands back on the same step. The
		// step's result.json is cleared by prepare on the way in, so the retry
		// is a real attempt rather than a reuse of the failure.
		return owJump, at - 1

	case flow.DecideGoto:
		target := store.StepIndex(def.Steps, d.Step)
		if target < 0 || target > at {
			// sanitise already refused both, so reaching here means the
			// definition moved under the run.
			return owStop, 0
		}
		// The same span clearing a declared edge does, and for the same reason:
		// the reuse path skips any step whose result.json says ok, so leaving
		// the steps in between would re-run the first one and then hand this
		// one work an attempt that never happened produced.
		if err := clearSpan(def.Steps, target, at, runDir, len(wr.Decisions)); err != nil {
			wr.Outcome, wr.StoppedAt, wr.ParkReason = OutcomeParked, stepID, ParkOverwatch
			wr.Err = err
			return owStop, 0
		}
		wr.Loops++
		wr.Outcome, wr.ParkReason, wr.StoppedAt = OutcomeDone, "", ""
		return owJump, target - 1

	case flow.DecideAbort:
		wr.Outcome, wr.StoppedAt, wr.ParkReason = OutcomeParked, stepID, ParkOverwatch
		return owStop, 0

	default: // park
		wr.Outcome, wr.StoppedAt = OutcomeParked, stepID
		if wr.ParkReason == "" {
			wr.ParkReason = ParkOverwatch
		}
		return owStop, 0
	}
}

// clearSpan renames the result files of every step from one index through
// another, so a run sent backwards re-runs them instead of reusing them.
//
// The files are kept rather than deleted: the rejected attempt beside the
// attempt that replaced it is what makes a loop readable afterwards, and only
// result.json is ever read.
func clearSpan(steps []store.Step, from, to int, runDir string, attempt int) error {
	if attempt < 1 {
		attempt = 1
	}
	for _, s := range steps[from : to+1] {
		dir := StepDir(runDir, s.ID)
		kept := filepath.Join(dir, fmt.Sprintf("result.attempt-%d.json", attempt))
		if err := os.Rename(filepath.Join(dir, "result.json"), kept); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
