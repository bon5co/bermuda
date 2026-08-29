// Package runner executes one job as an interactive herdr agent.
//
// The lifecycle proven by the spike:
//
//  1. ensure a dedicated bermuda workspace exists (never touch the user's own)
//  2. create a tab in it, injecting BERMUDA_RUN_DIR into the shell env
//  3. start an interactive agent in that tab's pane
//  4. submit the prompt and wait for the agent to settle
//  5. read result.json from the run dir — the sole authority on outcome
//  6. close the tab on success, keep it open when parked so a human can attend
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/statefs"
)

// WorkspaceLabel is the dedicated workspace bermuda owns. Runs only ever
// create, inspect, and close tabs inside it.
//
// Capitalised because Herdr prints it in the sidebar beside spaces people named
// themselves, and matched with IsWorkspaceLabel rather than by equality: a
// workspace called "bermuda" from an earlier version is the same workspace, and
// an exact match would quietly build a second one beside it and put the runs
// there.
const WorkspaceLabel = "Bermuda"

// IsWorkspaceLabel reports whether a workspace is the one bermuda owns.
func IsWorkspaceLabel(label string) bool {
	return strings.EqualFold(label, WorkspaceLabel)
}

// PluginSource identifies bermuda to Herdr for pane metadata and the agent
// view. Herdr scopes both by source, so this must match the plugin id.
const PluginSource = "bon5co.bermuda"

// TokenJob is the metadata token every bermuda pane carries. The agent view
// selects on its presence, so it is what makes a pane bermuda's.
const TokenJob = "bermuda_job"

// Outcome is the terminal state of a run.
type Outcome string

const (
	// OutcomeDone means result.json was written and reported success.
	OutcomeDone Outcome = "done"
	// OutcomeFailed means the agent finished but reported failure.
	OutcomeFailed Outcome = "failed"
	// OutcomeParked means the run needs a human: the agent blocked, the wait
	// timed out, or it exited without writing result.json. The tab is left
	// open in every parked case.
	OutcomeParked Outcome = "parked"
)

// ParkReason explains why a run parked.
type ParkReason string

const (
	ParkBlocked   ParkReason = "blocked"    // agent waiting on human input
	ParkTimeout   ParkReason = "timeout"    // exceeded the job's deadline
	ParkNoResult  ParkReason = "no_result"  // agent ended without result.json
	ParkBadResult ParkReason = "bad_result" // result.json present but unparseable
	// ParkAgentLost means bermuda stopped being able to observe the agent —
	// herdr reported it gone, or the control call failed — and no result.json
	// had appeared by the job's deadline. It is deliberately not no_result:
	// "the agent ended without writing" is a claim about the agent, and losing
	// sight of a process is not evidence that it stopped.
	ParkAgentLost ParkReason = "agent_lost"
)

// Job is one unit of scheduled work.
type Job struct {
	// ID is the stable job identifier, used to name agents and run dirs.
	ID string
	// Prompt is the instruction sent to the agent. ResultContract is appended
	// to it automatically.
	Prompt string
	// CWD is the working directory the agent starts in.
	CWD string
	// Kind is the herdr agent kind ("claude", "codex", ...).
	Kind string
	// AgentArgs are passed to the agent binary, e.g. permission flags.
	AgentArgs []string
	// Timeout bounds the prompt wait. Zero means wait indefinitely.
	Timeout time.Duration
	// Env is injected into the agent's shell alongside BERMUDA_RUN_DIR.
	Env map[string]string
	// WorkspaceID puts this run's tab in a named space instead of the one
	// bermuda owns. A flow sets it so every step of one run lands in the same
	// space and therefore in the same thread; a space is a membership list, and
	// steps that share one can read each other's findings.
	//
	// A space that has gone is not a reason to refuse the run: the tab falls back
	// to bermuda's own space, which is where it would have gone before this
	// existed.
	WorkspaceID string
	// Persistent keeps this job's agent alive between runs and reuses it,
	// skipping tab creation and agent startup. The agent's context is cleared
	// before each run, so runs stay independent: reuse is about avoiding
	// startup cost, not about carrying a conversation forward.
	Persistent bool
}

// persistentAgentName is the stable agent name for a persistent job. It does
// not vary per run, which is what makes the agent findable next time.
func persistentAgentName(jobID string) string {
	name := sanitizeAgentName("bmp-" + jobID)
	if len(name) > 32 {
		// herdr refuses a name longer than 32 characters outright
		// (invalid_agent_name), so an unbounded name means every run of a
		// persistent job fails to start — nothing about the job says why.
		//
		// The head is kept rather than the tail, because a persistent name has
		// no run id to make its tail distinctive and the readable part of a job
		// id is at the front. A digest of the whole id is appended so two long
		// ids sharing a prefix are not handed the same agent, which would let
		// one job inherit another's conversation.
		sum := fnv.New32a()
		_, _ = sum.Write([]byte(jobID))
		head := strings.TrimRight(name[:23], "-_")
		name = head + "-" + strconv.FormatUint(uint64(sum.Sum32()), 36)
	}
	return name
}

// Result is what a job writes to result.json.
type Result struct {
	Status string          `json:"status"` // "ok" or "error"
	Note   string          `json:"note"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Run is the record of a single execution.
type Run struct {
	JobID      string
	RunID      string
	RunDir     string
	Outcome    Outcome
	ParkReason ParkReason
	Status     herdrcli.AgentStatus
	Result     *Result
	AgentName  string
	TabID      string
	PaneID     string
	StartedAt  time.Time
	EndedAt    time.Time
	Err        error
	// MetaErr records a failure to label the pane. It is cosmetic and never
	// changes the run's outcome.
	MetaErr error
}

// fail records an error bermuda itself observed and returns it unchanged.
//
// The error was already returned to the caller; what was missing is that it
// never reached the run. A run that dies on the launch path — no workspace, no
// tab, an agent that would not start — writes no result.json, so the run row
// persisted for it carried an empty note and the failure explained itself
// nowhere. Twelve of the fleet's ninety-eight failures classify as `unknown`
// for exactly that reason (2026-08-17, Failure Causes on the Fleet): the words
// existed, on a process that had already exited by the time anybody asked.
//
// The first error wins. Later cleanup — a tab that would not close after the
// agent failed to start — describes the aftermath, not the cause.
func (r *Run) fail(err error) error {
	if r.Err == nil {
		r.Err = err
		r.writeErrFile()
	}
	return err
}

// ErrFile is where a run records what bermuda itself observed going wrong.
//
// The run row in the database is not the only reader. Every instrument that
// asks why the fleet failed — fleet-inventory, failure-cause, the freshness
// watchman — walks the run directories, because a directory outlives the
// database it was indexed in and is what a person finds first. Those tools see
// a launch failure as a directory holding a prompt and nothing else, and a
// failure that left no words classifies as `unknown`, which is a gap in the
// rules rather than a fact about the run.
//
// It is deliberately not result.json. That file is the agent's own account of
// the work and the sole authority on the outcome; writing one on the agent's
// behalf would forge the one signal the runner trusts. This file makes the
// harness's account separately readable, and a reader that wants the agent's
// answer still finds nothing — which is the truth.
const ErrFile = "bermuda-error.txt"

// writeErrFile records the error beside the run's other artifacts.
//
// Best-effort and silent: the run has already failed, and failing to write a
// note about a failure must not become a second failure. There is nothing to
// write into when the run directory is what could not be created.
func (r *Run) writeErrFile() {
	if r.RunDir == "" || r.Err == nil {
		return
	}
	_ = os.WriteFile(filepath.Join(r.RunDir, ErrFile), []byte(r.Err.Error()+"\n"), statefs.File)
}

// parkNote is what bermuda observed about a park, in its own words.
//
// Only the parks bermuda actually watched happen get one. `no_result` does not:
// what bermuda saw was an agent ending and no file appearing, which is the
// absence the reason already names, and prose restating it would put words in
// the mouth of a run that observed nothing. `bad_result` does not either — the
// file is there, it is the agent's own account, and its unreadability is
// visible to any reader that opens it.
func parkNote(reason ParkReason) string {
	switch reason {
	case ParkBlocked:
		return "the agent blocked waiting on human input; no result.json was written"
	case ParkTimeout:
		return "the agent did not finish within the job's timeout"
	case ParkAgentLost:
		return "bermuda lost sight of the agent and no result.json appeared before the deadline"
	}
	return ""
}

// recordPark leaves an observed park's reason where the instruments read.
//
// The run row has a ParkReason column and needs no prose; a run *directory* has
// no columns at all. Every instrument that asks why the fleet failed —
// fleet-inventory, failure-cause, the freshness watchman — walks the
// directories, because a directory outlives the database that indexed it. A
// blocked run leaves them a prompt and nothing else, and a failure that left no
// words classifies as `unknown`: two runs of this fleet's own night shift
// parked on an interactive menu in a headless session on 2026-08-26 and said so
// nowhere a reader could find.
//
// An error already recorded wins. `fail` describes a launch that died, which is
// a more specific fact than the park that followed it. Best-effort and silent,
// for the same reason as writeErrFile: the run has already stopped, and failing
// to write a note about it must not become a second failure.
func (r *Run) recordPark() {
	if r.RunDir == "" || r.Err != nil || r.Outcome != OutcomeParked {
		return
	}
	note := parkNote(r.ParkReason)
	if note == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(r.RunDir, ErrFile), []byte(note+"\n"), statefs.File)
}

// Note is what this run says about itself on the row that outlives it.
//
// result.json wins whenever the agent wrote one: it is the agent's own account
// of the work, and the runner trusts no other channel for the outcome. When
// there is none, the run says what bermuda observed instead, prefixed so the
// two are never confused downstream — an agent's note is a claim about the
// work, a bermuda note is a claim about the launch, and only one of them is
// evidence of a defect in the job.
//
// A park with no error stays silent on purpose. "The agent ended without
// writing result.json" is already the ParkReason, stored in its own column, and
// restating it as prose would put words in the mouth of a run that observed
// nothing.
func (r *Run) Note() string {
	if r.Result != nil {
		return r.Result.Note
	}
	if r.Err != nil {
		return "bermuda: " + r.Err.Error()
	}
	return ""
}

// Runner executes jobs against a herdr server.
type Runner struct {
	Herdr *herdrcli.Client
	// StateDir holds per-run directories. It is bermuda's own state directory,
	// not herdr's: the command layer resolves it and deliberately ignores
	// HERDR_PLUGIN_STATE_DIR, because the scheduler runs with no herdr server
	// at all and a plugin pane must not end up on a second database.
	StateDir string
	// StartTimeout bounds waiting for the agent to become interactive.
	StartTimeout time.Duration
	// ResultPoll is how often a lost agent's run directory is re-checked for
	// result.json. Zero means the default.
	ResultPoll time.Duration
	// ResultGrace bounds that wait for a job that declared no timeout of its
	// own. A job with a timeout is bounded by its timeout instead. Zero means
	// the default.
	ResultGrace time.Duration
}

const (
	defaultResultPoll  = 2 * time.Second
	defaultResultGrace = 15 * time.Minute
)

// resultContract is appended to every prompt. The result file is the only
// channel the runner trusts, so the contract has to be stated on every run.
// The contract names the file-writing tool deliberately. Left to itself an
// agent reaches for a shell redirect, which needs Bash permission and parks
// every run on a permission prompt; a plain file write is covered by
// acceptEdits, so unattended jobs need no permission bypass.
// It lives in prompt.md alongside the job's own instructions, so its length
// and line breaks are unconstrained.
const resultContract = `

---
When finished, use your file-writing tool (not a shell command or redirect) to write %s with this exact shape: {"status":"ok"|"error","note":"<one line summary>"}. Write that file even if the task failed; it is the only signal that this run completed.`

// Execute runs one job to completion and returns its record.
//
// A non-nil Run is returned even on failure so callers can persist and park
// partial runs; check Run.Outcome, not the error, to decide what happened.
func (r *Runner) Execute(ctx context.Context, job Job, runID string) (*Run, error) {
	return r.ExecuteIn(ctx, job, runID, filepath.Join(r.StateDir, "runs", runID))
}

// ExecuteIn runs one job with its artifacts in a caller-chosen directory.
//
// Flow steps use it: a step is a run in every respect except where its
// result.json lives, which is inside the flow's run directory rather than
// beside it. Everything else — the result contract, the parking rules, the
// transcript — is the same, so a step needs no result plumbing of its own.
func (r *Runner) ExecuteIn(ctx context.Context, job Job, runID, runDir string) (*Run, error) {
	run := &Run{
		JobID:     job.ID,
		RunID:     runID,
		AgentName: agentName(job.ID, runID),
		StartedAt: time.Now(),
	}
	// Every early return still needs a usable duration and outcome.
	run.Outcome = OutcomeParked
	run.ParkReason = ParkNoResult
	defer func() {
		if run.EndedAt.IsZero() {
			run.EndedAt = time.Now()
		}
	}()

	if err := os.MkdirAll(runDir, statefs.Dir); err != nil {
		return run, run.fail(fmt.Errorf("create run dir: %w", err))
	}
	run.RunDir = runDir

	if job.Persistent {
		run.AgentName = persistentAgentName(job.ID)
		// Reuse the live agent when it is still there and free. A blocked
		// agent is deliberately not reused: it is waiting on a human, and
		// prompting it would answer its question with the next job prompt.
		if ag, err := r.Herdr.AgentGet(ctx, run.AgentName); err == nil {
			switch ag.AgentStatus {
			case herdrcli.StatusBlocked:
				run.Outcome, run.ParkReason = OutcomeParked, ParkBlocked
				run.TabID, run.PaneID, run.Status = ag.TabID, ag.PaneID, ag.AgentStatus
				run.EndedAt = time.Now()
				run.recordPark()
				return run, nil
			default:
				run.TabID, run.PaneID = ag.TabID, ag.PaneID
				if err := r.clearAgent(ctx, run.AgentName); err != nil {
					return run, run.fail(fmt.Errorf("clear agent: %w", err))
				}
				return r.promptAndClassify(ctx, run, job, runDir)
			}
		}
	}

	spaceID, err := r.spaceFor(ctx, job)
	if err != nil {
		return run, run.fail(fmt.Errorf("ensure workspace: %w", err))
	}

	env := map[string]string{"BERMUDA_RUN_DIR": runDir, "BERMUDA_JOB_ID": job.ID}
	for k, v := range job.Env {
		env[k] = v
	}

	pane, err := r.Herdr.TabCreate(ctx, spaceID, job.ID, job.CWD, env)
	if err != nil {
		return run, run.fail(fmt.Errorf("create tab: %w", err))
	}
	run.TabID, run.PaneID = pane.TabID, pane.PaneID

	startTimeout := r.StartTimeout
	if startTimeout == 0 {
		startTimeout = 60 * time.Second
	}
	job.AgentArgs = withRunDirAccess(job.Kind, job.AgentArgs, runDir)
	if err := r.startAgent(ctx, run, job, pane.PaneID, startTimeout); err != nil {
		// The agent never ran, so there is nothing here for a human to attend
		// to. Reclaim the tab rather than leaking one per failed start.
		if closeErr := r.Herdr.TabClose(ctx, run.TabID); closeErr == nil {
			run.TabID = ""
		}
		return run, run.fail(fmt.Errorf("start agent: %w", err))
	}

	return r.promptAndClassify(ctx, run, job, runDir)
}

// tagPane makes a run legible in Herdr's own agents list.
//
// Without this every bermuda pane shows up as a generic "claude" with no
// indication of which job it is, which run, or why it is waiting. The tokens
// are also what the bermuda agent view filters and sorts on.
func (r *Runner) tagPane(ctx context.Context, run *Run, job Job, phase string) {
	if run.PaneID == "" {
		return
	}
	meta := herdrcli.PaneMetadata{
		DisplayAgent: "bermuda:" + job.ID,
		Title:        job.ID,
		Tokens: map[string]string{
			TokenJob:        job.ID,
			"bermuda_run":   run.RunID,
			"bermuda_phase": phase,
		},
		// Deliberately no state labels: overriding what "blocked" or "working"
		// read as would redefine Herdr's own status vocabulary, and bermuda
		// should describe its runs without changing what Herdr's words mean.
	}
	if err := r.Herdr.ReportPaneMetadata(ctx, run.PaneID, PluginSource, meta); err != nil {
		// Metadata is cosmetic: a run must not fail because its label did not
		// stick.
		run.MetaErr = err
	}
}

// promptAndClassify submits the prompt, archives the transcript, decides the
// outcome, and reclaims the tab when the run finished cleanly. It is shared by
// fresh runs and by persistent agents being prompted again.
func (r *Runner) promptAndClassify(ctx context.Context, run *Run, job Job, runDir string) (*Run, error) {
	// The submitted text is always one short line pointing at a file.
	// Submitting the job prompt directly does not scale: real prompts are long
	// and multi-line, and anything multi-line trips the agent's bracketed-paste
	// handling and is never submitted at all.
	//
	// Paths are resolved here rather than passed as $BERMUDA_RUN_DIR, since
	// expanding a variable would itself require shell permission, and a
	// persistent agent's shell has an older run's value anyway.
	// Label the pane before the work starts, so it is identifiable in Herdr's
	// agents list for the whole run rather than only once it finishes. Both
	// the fresh and the reused-agent paths arrive here.
	r.tagPane(ctx, run, job, "running")

	promptPath := filepath.Join(runDir, "prompt.md")
	resultPath := filepath.Join(runDir, "result.json")
	body := job.Prompt + "\n" + fmt.Sprintf(resultContract, resultPath) + "\n"
	if err := os.WriteFile(promptPath, []byte(body), statefs.File); err != nil {
		return run, run.fail(fmt.Errorf("write prompt: %w", err))
	}
	pointer := fmt.Sprintf("Read the file %s and do exactly what it says.", promptPath)

	deadline := r.resultDeadline(job)
	status, promptErr := r.prompt(ctx, run.AgentName, pointer, job.Timeout)
	run.Status = status

	// The wait ended without bermuda having watched the agent settle. Whatever
	// went wrong went wrong on our side of the glass — herdr lost the process,
	// a control call failed, the prompt was never seen to land — and none of
	// that is evidence the agent stopped working. It observably does not stop:
	// the incident this guards against had herdr report agent_not_running
	// thirty seconds in while the agent went on to finish the job ten minutes
	// later and write its result file.
	//
	// result.json is the only authority on the outcome, so wait for it for the
	// rest of the budget the job was given rather than declaring the run
	// resultless at the moment we went blind.
	if lostSight(promptErr) {
		r.awaitResult(ctx, runDir, deadline)
	}

	// Archive the transcript for humans regardless of outcome. Never parsed.
	if transcript, err := r.Herdr.AgentRead(ctx, run.AgentName); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "transcript.txt"), []byte(transcript), statefs.File)
	}

	r.classify(run, status, promptErr)
	run.EndedAt = time.Now()

	// Re-label with the outcome. A parked pane stays open, so this is what it
	// will read as until a human deals with it.
	phase := string(run.Outcome)
	if run.ParkReason != "" {
		phase = string(run.Outcome) + ": " + string(run.ParkReason)
	}
	r.tagPane(ctx, run, job, phase)

	// Only a clean, successful run reclaims its tab, and a persistent agent
	// keeps its tab by definition: closing it would destroy the conversation
	// the job exists to continue.
	if run.Outcome == OutcomeDone && !job.Persistent {
		if err := r.Herdr.TabClose(ctx, run.TabID); err != nil {
			run.Err = fmt.Errorf("close tab: %w", err)
		}
	}
	return run, run.Err
}

// withRunDirAccess grants the agent write access to its own run directory.
//
// The run dir lives outside the job's working directory, and permission modes
// such as claude's acceptEdits only cover the working directory. Without this
// the agent parks on an approval prompt for the one file bermuda requires it
// to write. This widens access by exactly one bermuda-owned directory, which
// is narrower than relaxing the permission mode itself.
func withRunDirAccess(kind string, args []string, runDir string) []string {
	if kind != "claude" {
		// Other agent kinds have their own flags; until they are modelled,
		// leave the caller's arguments untouched.
		return args
	}
	for _, a := range args {
		if a == "--add-dir" {
			return args // caller is managing directory access itself
		}
	}
	return append(append([]string{}, args...), "--add-dir", runDir)
}

// clearAgent wipes a reused agent's context before the next job.
//
// Without this, every run on a persistent agent inherits the previous run's
// conversation: the agent starts each job already primed with unrelated work,
// which both wastes context and lets one job's assumptions leak into another.
func (r *Runner) clearAgent(ctx context.Context, agent string) error {
	// /clear is submitted as an ordinary prompt because it is typed into the
	// same input. It is a local command, so the agent settles immediately
	// rather than working; a short timeout keeps a wedged agent from stalling
	// the run.
	if _, err := r.prompt(ctx, agent, "/clear", 30*time.Second); err != nil {
		if herdrcli.Code(err, "timeout") || herdrcli.Code(err, "agent_prompt_stalled") {
			// The agent did not report a state change for a command that does
			// no work. Treat the context as cleared rather than failing the
			// run over a detection artifact.
			return nil
		}
		return err
	}
	return nil
}

// promptAttempts bounds resubmission when an agent swallows the first prompt.
const promptAttempts = 3

// prompt submits the job prompt, resubmitting if the agent's TUI dropped it.
//
// An agent reports interactive_ready before its TUI reliably accepts input, so
// the first submission can vanish with no state change at all: herdr reports
// agent_prompt_stalled and the input box is left empty. Resubmitting is safe
// only when the agent is still not working — if it did start, the prompt did
// land and we must wait rather than send it twice.
func (r *Runner) prompt(ctx context.Context, agent, text string, timeout time.Duration) (herdrcli.AgentStatus, error) {
	var lastErr error
	for attempt := 0; attempt < promptAttempts; attempt++ {
		status, err := r.Herdr.AgentPrompt(ctx, agent, text, timeout)
		if err == nil || !herdrcli.Code(err, "agent_prompt_stalled") {
			return status, err
		}
		lastErr = err

		ag, getErr := r.Herdr.AgentGet(ctx, agent)
		if getErr == nil && ag.AgentStatus == herdrcli.StatusWorking {
			// The prompt did land; the stall was only a detection lag.
			if waitErr := r.Herdr.AgentWait(ctx, agent, timeout); waitErr != nil {
				return herdrcli.StatusUnknown, waitErr
			}
			ag, err = r.Herdr.AgentGet(ctx, agent)
			if err != nil {
				return herdrcli.StatusUnknown, err
			}
			return ag.AgentStatus, nil
		}

		select {
		case <-ctx.Done():
			return herdrcli.StatusUnknown, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return herdrcli.StatusUnknown, lastErr
}

// startAgent waits for the pane's shell prompt and starts the agent, retrying
// while herdr still considers the pane busy.
//
// Shell readiness and agent-start readiness are decided by different parts of
// herdr, so a pane can look idle to process-info and still be rejected as
// agent_pane_busy for a moment afterwards.
func (r *Runner) startAgent(ctx context.Context, run *Run, job Job, paneID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if err := r.Herdr.WaitShellPrompt(ctx, paneID, timeout); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("agent did not start within %s: %w", timeout, lastErr)
		}
		_, err := r.Herdr.AgentStart(ctx, run.AgentName, job.Kind, paneID, remaining, job.AgentArgs...)
		if err == nil {
			return nil
		}
		if !herdrcli.Code(err, "agent_pane_busy") {
			return err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// lostSight reports whether a prompt wait ended by bermuda losing track of the
// agent rather than by watching it settle.
//
// A timeout is excluded: there the agent was watched for the whole budget and
// never settled, so there is nothing left to wait for and nothing was lost.
// Every other error means the observation failed, not the work.
func lostSight(promptErr error) bool {
	return promptErr != nil && !herdrcli.Code(promptErr, "timeout")
}

// resultDeadline is how long a run may still produce result.json after bermuda
// loses sight of its agent.
//
// The job's own timeout is the budget it declared, so that is the bound. A job
// that declared none would otherwise be waited on forever, holding a scheduler
// slot for an agent that may genuinely be dead, so it gets a fixed grace
// instead.
func (r *Runner) resultDeadline(job Job) time.Time {
	if job.Timeout > 0 {
		return time.Now().Add(job.Timeout)
	}
	grace := r.ResultGrace
	if grace <= 0 {
		grace = defaultResultGrace
	}
	return time.Now().Add(grace)
}

// awaitResult polls the run directory until result.json appears or the deadline
// passes. The file is what the agent writes when it finishes; nothing else is
// consulted, precisely because the thing that was consulted — herdr's view of
// the process — is what turned out to be wrong.
func (r *Runner) awaitResult(ctx context.Context, runDir string, deadline time.Time) {
	poll := r.ResultPoll
	if poll <= 0 {
		poll = defaultResultPoll
	}
	for {
		if _, err := readResult(runDir); err == nil {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		wait := poll
		if left := time.Until(deadline); left < wait {
			wait = left
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// classify decides the run's outcome from result.json, falling back to the
// agent's last status. result.json wins whenever it is readable: "done" from
// herdr only means the process ended, not that the work succeeded.
func (r *Runner) classify(run *Run, status herdrcli.AgentStatus, promptErr error) {
	res, readErr := readResult(run.RunDir)
	if readErr == nil {
		run.Result = res
		// Clear the pessimistic default set before the run started.
		run.ParkReason = ""
		if res.Status == "ok" {
			run.Outcome = OutcomeDone
		} else {
			run.Outcome = OutcomeFailed
		}
		return
	}

	run.Outcome = OutcomeParked
	switch {
	case status == herdrcli.StatusBlocked:
		run.ParkReason = ParkBlocked
	case promptErr != nil && herdrcli.Code(promptErr, "timeout"):
		run.ParkReason = ParkTimeout
	case !os.IsNotExist(readErr):
		// The file is there and unreadable, which is the agent's mistake and
		// says so regardless of what happened to the process.
		run.ParkReason = ParkBadResult
	case lostSight(promptErr):
		// Waited out the budget above and still nothing. The honest reading is
		// that the agent was lost, not that it chose to write no result.
		run.ParkReason = ParkAgentLost
	default:
		run.ParkReason = ParkNoResult
	}
	if promptErr != nil && !herdrcli.Code(promptErr, "timeout") {
		_ = run.fail(promptErr)
	}
	run.recordPark()
}

// agentName builds a herdr-legal agent name: it must start with a lowercase
// letter and contain only lowercase letters, digits, '-' or '_', 1-32 chars.
// Job ids and run ids are user- and timestamp-derived, so both are sanitized
// and the result is truncated to fit.
func agentName(jobID, runID string) string {
	name := sanitizeAgentName("bm-" + jobID + "-" + runID)
	if len(name) > 32 {
		// Keeping the tail looks right -- run ids differ at the end -- but a run
		// id is "<timestamp>-<job id>", so for any job whose id is long enough
		// the tail is the job id plus the clock part of the timestamp and the
		// date is what gets cut. Two runs of rt-template-daily a day apart were
		// both named "t190002z-rt-template-daily", herdr refused the second with
		// agent_name_taken, and the run died before it wrote anything.
		//
		// So keep the readable head and append a digest of the whole name, the
		// same shape persistentAgentName uses: the digest covers the timestamp
		// whether or not it survived truncation.
		sum := fnv.New32a()
		_, _ = sum.Write([]byte(name))
		digest := strconv.FormatUint(uint64(sum.Sum32()), 36)
		head := strings.TrimRight(name[:31-len(digest)], "-_")
		name = head + "-" + digest
	}
	return name
}

func sanitizeAgentName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || out[0] < 'a' || out[0] > 'z' {
		out = "bm-" + out
	}
	return out
}

// ReadResult reads a run's result file.
//
// Exported because reconciliation needs it: a run's outcome lives in this file,
// not in the process that started it, so the outcome survives the death of
// whatever was supervising the run.
func ReadResult(runDir string) (*Result, error) { return readResult(runDir) }

func readResult(runDir string) (*Result, error) {
	b, err := os.ReadFile(filepath.Join(runDir, "result.json"))
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("parse result.json: %w", err)
	}
	return &res, nil
}

// ensureWorkspace returns the workspace bermuda owns, creating it when there is
// none. It is bermuda's own by having been created by bermuda and recorded —
// not by being called Bermuda, which is a name anybody may already have used.
func (r *Runner) ensureWorkspace(ctx context.Context, cwd string) (*herdrcli.Workspace, error) {
	return EnsureWorkspace(ctx, r.Herdr, r.StateDir, cwd)
}

// spaceFor is which workspace this run's tab goes in: the one the job named, or
// bermuda's own.
//
// A named space that herdr no longer has falls back rather than failing. The
// space is where the run is *legible* — it decides which thread the agents in it
// share — and a run refused because its window was closed would make a
// coordination convenience into the thing that stops the work.
func (r *Runner) spaceFor(ctx context.Context, job Job) (string, error) {
	if id := strings.TrimSpace(job.WorkspaceID); id != "" {
		if ws, err := r.Herdr.WorkspaceGet(ctx, id); err == nil && ws != nil {
			return ws.WorkspaceID, nil
		}
	}
	ws, err := r.ensureWorkspace(ctx, job.CWD)
	if err != nil {
		return "", err
	}
	return ws.WorkspaceID, nil
}
