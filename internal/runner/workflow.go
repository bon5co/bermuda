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

	"github.com/bon5co/bermuda/internal/store"
)

// A workflow is a declared sequence of steps, run in series.
//
// What it buys over one prompt that says "then do B, then do C" is that the
// harness makes the call. B is launched by bermuda, not by A remembering to
// hand off, so A cannot skip B, inline B, or decide B was already covered — and
// A's report of its own work, the least reliable artifact in the system, is not
// an input to whether B runs.
//
// The other half is what happens when a step does not finish. A step that fails
// or that ends without writing result.json parks the workflow *at that step*:
// the steps after it never start, so nothing downstream proceeds on a claim
// nobody checked. Everything a completed step wrote stays on disk, which is
// what makes resume cheap — and resume being cheap is what removes the pressure
// to wave a failed step through.

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

// Workflow executes a job's declared steps in order.
type Workflow struct {
	// Launch starts an agent step. Production passes Runner.ExecuteIn.
	Launch StepLauncher
	// Shell runs a command step, defaulting to `sh -c`.
	Shell ShellRunner
	// Report is called when a step starts and again when it settles, so a
	// caller can persist progress while the workflow is still going: a workflow
	// that recorded itself only at the end would show nothing for the hour it
	// was running. Steps skipped because an earlier attempt already completed
	// them are deliberately not reported — the stored row already holds how
	// long that work really took, and overwriting it with this attempt's
	// zero-length skip would erase it.
	Report func(StepRun)
}

// OutcomeRunning marks a step that has started and not yet settled. Runs have
// no such outcome because the command layer records a run as running before it
// calls the runner at all; a step is started by the workflow itself, so the
// workflow is the only thing that can say so.
const OutcomeRunning Outcome = "running"

// ParkStepFailed is why a workflow parked: one of its steps reported failure.
// The step keeps its own outcome — the workflow parks, the step failed.
const ParkStepFailed ParkReason = "step_failed"

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
	Reused    bool
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
}

// WorkflowRun is the record of one execution of a workflow.
type WorkflowRun struct {
	JobID   string
	RunID   string
	RunDir  string
	Outcome Outcome
	// ParkReason is why the workflow stopped, taken from the step that stopped
	// it.
	ParkReason ParkReason
	// StoppedAt is the id of the step the workflow parked at, empty when every
	// step finished.
	StoppedAt string
	// Steps is what this attempt did, which on a park is only the steps up to
	// and including the one that stopped it.
	Steps []StepRun
	// Total is how many steps were declared. It is not len(Steps): a workflow
	// that parked at step two of four is 1/4, and saying 1/2 would report the
	// two steps it managed as the whole job.
	Total     int
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
}

// Done reports how many steps completed, of how many declared.
func (w *WorkflowRun) Done() (done, total int) {
	for _, s := range w.Steps {
		if s.Outcome == OutcomeDone {
			done++
		}
	}
	return done, w.Total
}

// Note is the one-line summary of a workflow run.
func (w *WorkflowRun) Note() string {
	done, total := w.Done()
	if w.StoppedAt == "" {
		return fmt.Sprintf("%d/%d steps ok", done, total)
	}
	reason := string(w.ParkReason)
	if reason == "" {
		reason = "stopped"
	}
	return fmt.Sprintf("%d/%d steps, parked at %s (%s)", done, total, w.StoppedAt, reason)
}

// StepDir is where one step's artifacts live: under the run, named by the step,
// so a workflow's output is one directory tree and resume can find what an
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
func (w *Workflow) Execute(ctx context.Context, job store.Job, runID, runDir string) (*WorkflowRun, error) {
	wr := &WorkflowRun{JobID: job.ID, RunID: runID, RunDir: runDir,
		Total: len(job.Steps), StartedAt: time.Now(), Outcome: OutcomeDone}
	defer func() {
		if wr.EndedAt.IsZero() {
			wr.EndedAt = time.Now()
		}
	}()

	// Validated again here and not only when the job was written: a job stored
	// before a rule existed must not run under the old rules.
	if err := store.ValidateSteps(job.Steps, job.Model); err != nil {
		wr.Outcome, wr.ParkReason, wr.Err = OutcomeParked, ParkBadResult, err
		return wr, err
	}
	if len(job.Steps) == 0 {
		err := fmt.Errorf("job %s has no steps", job.ID)
		wr.Outcome, wr.ParkReason, wr.Err = OutcomeParked, ParkBadResult, err
		return wr, err
	}

	for i, step := range job.Steps {
		sr := StepRun{ID: step.ID, Index: i, Kind: step.Label(),
			Dir: StepDir(runDir, step.ID)}

		if res, err := readResult(sr.Dir); err == nil && res.Status == "ok" {
			// An earlier attempt already did this one. Not re-running it is the
			// whole point of resume: the expensive steps are exactly the ones a
			// human would otherwise be tempted to wave through.
			sr.Outcome, sr.Reused, sr.Note = OutcomeDone, true, res.Note
			wr.Steps = append(wr.Steps, sr)
			continue
		}

		sr.StartedAt = time.Now()
		sr.Outcome = OutcomeRunning
		w.report(sr)

		if err := w.prepare(sr.Dir); err != nil {
			sr.Outcome, sr.ParkReason, sr.Err = OutcomeParked, ParkNoResult, err
		} else if step.IsAgent() {
			w.runAgentStep(ctx, job, step, runID, &sr)
		} else {
			w.runCommandStep(ctx, job, step, &sr)
		}
		sr.EndedAt = time.Now()
		w.report(sr)
		wr.Steps = append(wr.Steps, sr)

		if sr.Outcome != OutcomeDone {
			// Park here. B must not run on A's unverified claim, and a dead A
			// must not be invisible.
			wr.Outcome, wr.StoppedAt = OutcomeParked, step.ID
			if sr.Outcome == OutcomeFailed {
				// A step reporting failure is an outcome, not an error: the
				// workflow did exactly what it was built to do. Only a harness
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
			return wr, wr.Err
		}
	}
	return wr, nil
}

func (w *Workflow) report(sr StepRun) {
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
func (w *Workflow) prepare(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "result.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// runAgentStep launches one agent and takes its verdict from result.json.
func (w *Workflow) runAgentStep(ctx context.Context, job store.Job, step store.Step, runID string, sr *StepRun) {
	if w.Launch == nil {
		sr.Outcome, sr.ParkReason = OutcomeParked, ParkNoResult
		sr.Err = fmt.Errorf("step %s: no agent launcher", step.ID)
		return
	}
	j := StepJob(job, step)
	// BERMUDA_STEP_DIR names the same directory as BERMUDA_RUN_DIR, which the
	// launcher injects. Both are given because a step is a run to the agent
	// writing result.json, and a step to the workflow reading it.
	j.Env = map[string]string{
		"BERMUDA_STEP_DIR": sr.Dir,
		"BERMUDA_STEP_ID":  step.ID,
		"BERMUDA_RUN_ID":   runID,
	}
	// The step id is part of the agent's run id, so every step of a workflow is
	// a differently named agent. That is what makes "a different subagent is a
	// different agent" true by construction rather than by remembering: there is
	// no path here that hands one step's live agent to the next.
	run, err := w.Launch(ctx, j, stepRunID(runID, step.ID), sr.Dir)
	if run != nil {
		sr.Outcome, sr.ParkReason, sr.AgentName = run.Outcome, run.ParkReason, run.AgentName
		if run.Result != nil {
			sr.Note = run.Result.Note
		}
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
func (w *Workflow) runCommandStep(ctx context.Context, job store.Job, step store.Step, sr *StepRun) {
	shell := w.Shell
	if shell == nil {
		shell = sh
	}
	env := append(os.Environ(),
		"BERMUDA_STEP_DIR="+sr.Dir,
		"BERMUDA_RUN_DIR="+sr.Dir,
		"BERMUDA_STEP_ID="+step.ID,
		"BERMUDA_JOB_ID="+job.ID)

	out, err := shell(ctx, step.Run, job.CWD, env)
	// Kept for a human, never parsed: the exit status is the verdict.
	if len(out) > 0 {
		_ = os.WriteFile(filepath.Join(sr.Dir, "output.txt"), out, 0o644)
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
	return os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644)
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
// workflow can never be the same agent.
func stepRunID(runID, stepID string) string { return runID + "-" + stepID }
