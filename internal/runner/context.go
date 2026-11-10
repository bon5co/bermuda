package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v3/internal/herdrcli"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// SessionStore is the durable conversation bookkeeping used by the runner.
type SessionStore interface {
	JobSession(context.Context, string) (*store.JobSession, error)
	PutJobSession(context.Context, store.JobSession) error
	DeleteJobSession(context.Context, string) error
	HadPriorConversation(context.Context, string, string) (bool, error)
}

var errResumeMismatch = errors.New("harness did not reopen the recorded session")

// resumeArgs keeps harness resume spellings and argument ordering in one place.
// Flags verified with codex 0.159.2, claude 2.1.285 and pi 0.87.1.
func resumeArgs(kind string, session store.JobSession, flags []string) ([]string, error) {
	if harnessKind(kind) != harnessKind(session.Harness) || session.Value == "" {
		return nil, fmt.Errorf("stored session does not match harness %q", kind)
	}
	args := append([]string{}, flags...)
	switch kind {
	case "codex":
		if session.Kind != "id" {
			break
		}
		return append(append([]string{"resume"}, args...), session.Value), nil
	case "claude":
		if session.Kind != "id" {
			break
		}
		return append(args, "--resume", session.Value), nil
	case "pi", "omp":
		if session.Kind != "id" && session.Kind != "path" {
			break
		}
		if session.Kind == "path" {
			info, err := os.Stat(session.Value)
			if err != nil {
				return nil, fmt.Errorf("open recorded session file: %w", err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("recorded session path is not a file: %s", session.Value)
			}
		}
		return append(args, "--session", session.Value), nil
	}
	return nil, fmt.Errorf("cannot resume %s session of kind %q", kind, session.Kind)
}

func harnessKind(kind string) string {
	if kind == "omp" {
		return "pi"
	}
	return kind
}

func matchesSession(ag *herdrcli.Agent, session *store.JobSession) bool {
	if ag.AgentSession == nil || session == nil {
		return false
	}
	harness := ag.AgentSession.Agent
	if harness == "" {
		harness = ag.Agent
	}
	return harnessKind(harness) == harnessKind(session.Harness) &&
		ag.AgentSession.Kind == session.Kind && ag.AgentSession.Value == session.Value
}

func missingAgent(err error) bool {
	return herdrcli.Code(err, "agent_not_found") || herdrcli.Code(err, "agent_not_running") ||
		herdrcli.Code(err, "pane_not_found")
}

func (r *Runner) loadSession(ctx context.Context, run *Run, job Job) (*store.JobSession, error) {
	session, err := r.Store.JobSession(ctx, job.ID)
	if err == nil {
		return session, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	// No captured session after an earlier keep-context run is still loss,
	// including old Herdr versions that cannot report the conversation.
	had, err := r.Store.HadPriorConversation(ctx, job.ID, run.RunID)
	if err != nil {
		return nil, err
	}
	if had {
		run.Context, run.ContextNote = "lost", "no recorded harness session is available from the previous run"
	}
	return nil, nil
}

func (run *Run) loseContext(reason string) {
	run.Context = "lost"
	run.ContextNote = reason
}

func (run *Run) parkContext() {
	run.Outcome, run.ParkReason = OutcomeParked, ParkContextLost
	run.EndedAt = time.Now()
	run.recordPark()
}

// reuseContext never prompts over a question or an active restored turn.
func (r *Runner) reuseContext(ctx context.Context, run *Run, job Job, ag *herdrcli.Agent, how string) (*Run, error) {
	if job.KeepContext && !r.ownsContextAgent(job, ag) {
		run.Status = ag.AgentStatus
		run.loseContext("the recorded conversation is held outside this job's workspace; attend to it before retrying")
		run.parkContext()
		return run, nil
	}
	if job.KeepContext && harnessKind(ag.Agent) != harnessKind(job.Kind) {
		run.Status = ag.AgentStatus
		run.loseContext("the named agent is running a different harness; attend to it before retrying")
		run.parkContext()
		return run, nil
	}
	run.TabID, run.PaneID = ag.TabID, ag.PaneID
	if job.KeepContext {
		// A live named agent is direct evidence that its conversation survived.
		// Discard the provisional warning from a missing stored session.
		if run.Context == "lost" {
			run.ContextNote = ""
		}
		run.Context = how
		if ag.AgentSession != nil {
			run.ContextSession = ag.AgentSession.Value
		}
	}
	if ag.Name != "" {
		run.AgentName = ag.Name
	} else {
		run.AgentName = ag.PaneID
	}
	if ag.AgentStatus == herdrcli.StatusWorking {
		if err := r.Herdr.AgentWait(ctx, run.AgentName, job.Timeout); err != nil {
			run.Status = ag.AgentStatus
			run.ParkReason = ParkAgentLost
			if herdrcli.Code(err, "timeout") {
				run.ParkReason = ParkTimeout
			}
			run.ContextNote = strings.TrimSpace(run.ContextNote + "; existing conversation did not settle: " + err.Error())
			run.recordPark()
			return run, run.fail(fmt.Errorf("wait for existing agent: %w", err))
		}
		var err error
		ag, err = r.Herdr.AgentGet(ctx, run.AgentName)
		if err != nil {
			return run, run.fail(err)
		}
		if how == "resumed" {
			saved, err := r.Store.JobSession(ctx, job.ID)
			if err == nil {
				ag, err = r.resumedAgent(ctx, run, saved, r.contextStartTimeout())
			}
			if errors.Is(err, errResumeMismatch) {
				return run, err
			}
			if err != nil {
				run.loseContext("could not verify resumed session after the active turn: " + err.Error())
				run.parkContext()
				return run, nil
			}
		}
	}
	if ag.AgentStatus == herdrcli.StatusBlocked {
		run.Outcome, run.ParkReason, run.Status = OutcomeParked, ParkBlocked, ag.AgentStatus
		run.EndedAt = time.Now()
		run.recordPark()
		return run, nil
	}
	if !ag.InteractiveReady || (ag.AgentStatus != herdrcli.StatusIdle && ag.AgentStatus != herdrcli.StatusDone) {
		run.Outcome, run.ParkReason, run.Status = OutcomeParked, ParkBlocked, ag.AgentStatus
		run.ContextNote = strings.TrimSpace(run.ContextNote + "; existing conversation is not ready for a prompt")
		run.EndedAt = time.Now()
		run.recordPark()
		return run, nil
	}
	if !job.KeepContext {
		if err := r.clearAgent(ctx, run.AgentName); err != nil {
			return run, run.fail(fmt.Errorf("clear agent: %w", err))
		}
	}
	return r.promptAndClassify(ctx, run, job, run.RunDir)
}

func (r *Runner) ownsContextAgent(job Job, ag *herdrcli.Agent) bool {
	if job.WorkspaceID != "" {
		return ag.WorkspaceID == job.WorkspaceID
	}
	recorded, err := readWorkspaceRecord(r.StateDir)
	return err == nil && recorded.ID != "" && recorded.ID == ag.WorkspaceID
}

// A restored TUI may report its session after becoming interactive. Confirm
// reported identity before giving it work. Missing metadata remains unknown;
// an explicit mismatch means the harness started another conversation.
func (r *Runner) resumedAgent(ctx context.Context, run *Run, session *store.JobSession, timeout time.Duration) (*herdrcli.Agent, error) {
	// Give a delayed hook a moment to report, but missing optional metadata
	// must not turn a successful harness resume into a fresh conversation.
	if timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		ag, err := r.Herdr.AgentGet(ctx, run.AgentName)
		if err != nil {
			return nil, err
		}
		if ag.AgentStatus == herdrcli.StatusBlocked {
			return ag, nil
		}
		if ag.AgentStatus == herdrcli.StatusWorking {
			// A live turn is not a failed launch. Reuse waits with the job budget
			// and retains the pane if the wait fails.
			return ag, nil
		}
		if ag.AgentSession != nil {
			if !matchesSession(ag, session) {
				return nil, errResumeMismatch
			}
			return ag, nil
		}
		if time.Now().After(deadline) {
			run.ContextNote = strings.TrimSpace(run.ContextNote + "; resume requested, but Herdr has not confirmed the session")
			return ag, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *Runner) captureSession(ctx context.Context, run *Run, job Job) {
	ag, err := r.Herdr.AgentGet(ctx, run.AgentName)
	if err != nil {
		run.ContextNote = strings.TrimSpace(run.ContextNote + "; could not capture harness session: " + err.Error())
		return
	}
	if ag.PaneID != run.PaneID {
		run.ContextNote = strings.TrimSpace(run.ContextNote + "; agent was replaced before its session could be captured")
		return
	}
	session := ag.AgentSession
	if session == nil || session.Value == "" {
		run.ContextNote = strings.TrimSpace(run.ContextNote + "; Herdr did not report a harness session")
		return
	}
	harness := session.Agent
	if harness == "" {
		harness = ag.Agent
	}
	if harnessKind(harness) != harnessKind(job.Kind) {
		run.loseContext("live agent reports a different harness; session was not saved")
		return
	}
	previous, err := r.Store.JobSession(ctx, job.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		run.ContextNote = strings.TrimSpace(run.ContextNote + "; could not read job session: " + err.Error())
		return
	}
	if previous != nil && !matchesSession(ag, previous) {
		if run.Context == "resumed" {
			run.loseContext("harness opened a different session after resume")
		} else {
			run.ContextNote = strings.TrimSpace(run.ContextNote + "; harness session changed from " + previous.Value + " to " + session.Value)
		}
	}
	run.ContextSession = session.Value
	if err := r.Store.PutJobSession(ctx, store.JobSession{JobID: job.ID, Harness: job.Kind, Kind: session.Kind,
		Value: session.Value, RunID: run.RunID, CapturedAt: time.Now()}); err != nil {
		run.ContextNote = strings.TrimSpace(run.ContextNote + "; could not save job session: " + err.Error())
	}
}

func (r *Runner) contextStartTimeout() time.Duration {
	if r.StartTimeout > 0 {
		return r.StartTimeout
	}
	return 60 * time.Second
}

// ContextLabel is separate from the agent's account of the work.
func (run *Run) ContextLabel() string {
	if run.Context == "" {
		return ""
	}
	label := "context: " + run.Context
	if run.ContextSession != "" {
		label += " (" + run.ContextSession + ")"
	}
	if run.ContextNote != "" {
		label += "; " + strings.TrimPrefix(run.ContextNote, "; ")
	}
	return label
}

// FormatContextNote keeps the agent's result note and Bermuda's context
// observation together, including when a late result is reconciled later.
func FormatContextNote(resultNote, state, session, detail string) string {
	label := (&Run{Context: state, ContextSession: session, ContextNote: detail}).ContextLabel()
	if label == "" {
		return resultNote
	}
	if resultNote != "" {
		return resultNote + "; bermuda: " + label
	}
	return "bermuda: " + label
}
