package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Workflows: a job whose steps are declared rather than remembered.
//
// A single-prompt job carries every stage of multi-step work inside one agent's
// head — pull first, verify after, hand the output on. It usually does four of
// the five. A workflow moves the sequence out of the prompt and into the
// harness, which does not forget: the steps are stored here, run in series by
// the runner, and a step that cannot show it finished stops the ones after it.
//
// The step list is stored as JSON in a single column rather than as its own
// table. Steps are only ever read and written as a whole ordered list — there
// is no query that wants one step of one job — and a column keeps the job a
// single row, so saving a job cannot half-succeed.

// Step is one declared unit of a workflow.
//
// Exactly one of Agent and Run is set. An Agent step is a prompt and gets its
// own process; a Run step is a shell command and has no agent at all, which is
// the single cheapest reliability win here: most "the agent forgot" incidents
// are a command a model was asked to remember instead of a step the harness
// executes.
type Step struct {
	// ID is unique within the workflow. It names the step's directory and its
	// agent, and it is how resume and the board refer to it.
	ID string `json:"id"`

	// Agent is the prompt for an agent step.
	Agent string `json:"agent,omitempty"`
	// Run is the shell command for a deterministic step.
	Run string `json:"run,omitempty"`

	// Per-step agent config. These default to the job's and override it, so a
	// two-line mechanical edit does not have to burn the model a judgement-heavy
	// step needs. All four are meaningless on a Run step and rejected there.
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Subagent string `json:"subagent,omitempty"`
}

// IsAgent reports whether this step runs an agent rather than a command.
func (s Step) IsAgent() bool { return strings.TrimSpace(s.Agent) != "" }

// Label is what the step is, for a column that has room for one word.
func (s Step) Label() string {
	if s.IsAgent() {
		return "agent"
	}
	return "run"
}

// IsWorkflow reports whether this job is a declared sequence rather than one
// prompt.
func (j Job) IsWorkflow() bool { return len(j.Steps) > 0 }

// EffectiveModel is the model a step actually runs on: its own if it names one,
// otherwise the job's.
func (j Job) EffectiveModel(s Step) string {
	if m := strings.TrimSpace(s.Model); m != "" {
		return m
	}
	if m := strings.TrimSpace(j.Model); m != "" {
		return m
	}
	return DefaultModel
}

// ValidateSteps checks a workflow before anything can be written or run.
//
// defaultModel is the job's model, because a step that names none inherits it —
// validating only what the step spells out would let a job-level haiku through
// the one rule that exists to stop it.
func ValidateSteps(steps []Step, defaultModel string) error {
	if len(steps) == 0 {
		return nil // not a workflow; a single-prompt job is the normal case
	}
	seen := map[string]bool{}
	for i, s := range steps {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			return fmt.Errorf("step %d has no id: a step is referred to by id on the board, in resume, and in its own directory", i+1)
		}
		if err := checkStepID(id); err != nil {
			return err
		}
		// Compared case-insensitively: the id becomes a directory name, and two
		// steps that differ only in case would share a directory on a
		// case-insensitive filesystem — so the second would resume as though
		// the first had already run it.
		key := strings.ToLower(id)
		if seen[key] {
			return fmt.Errorf("step id %q is used twice: ids must be unique within a workflow", id)
		}
		seen[key] = true

		agent, cmd := strings.TrimSpace(s.Agent), strings.TrimSpace(s.Run)
		switch {
		case agent == "" && cmd == "":
			return fmt.Errorf("step %q has neither agent nor run: a step is a prompt or a command", id)
		case agent != "" && cmd != "":
			return fmt.Errorf("step %q has both agent and run: a step is a prompt or a command, not both", id)
		}

		if cmd != "" {
			// A run step has no agent, which is the point of it. Accepting
			// agent config here would silently ignore it, and a step that looks
			// configured but is not is worse than one that refuses.
			for _, f := range []struct{ name, value string }{
				{"model", s.Model}, {"effort", s.Effort},
				{"kind", s.Kind}, {"subagent", s.Subagent},
			} {
				if strings.TrimSpace(f.value) != "" {
					return fmt.Errorf("step %q sets %s on a run step, which has no agent to configure", id, f.name)
				}
			}
			continue
		}

		model := strings.TrimSpace(s.Model)
		if model == "" {
			model = strings.TrimSpace(defaultModel)
		}
		if isHaiku(model) {
			return fmt.Errorf("step %q would run on %q: the floor is sonnet, and a workflow is exactly where a cheap model quietly does four of five steps", id, model)
		}
	}
	return nil
}

// isHaiku matches the model family rather than one spelling, since a model is
// named "haiku", "claude-3-5-haiku-latest", or whatever the next id looks like.
func isHaiku(model string) bool {
	return strings.Contains(strings.ToLower(model), "haiku")
}

// checkStepID keeps a step id usable as both a directory name and part of a
// herdr agent name. A step id with a slash in it would put the step's output
// somewhere other than under its run.
func checkStepID(id string) error {
	if len(id) > 32 {
		return fmt.Errorf("step id %q is longer than 32 characters", id)
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return fmt.Errorf("step id %q contains %q: use letters, digits, '-' or '_'", id, string(r))
		}
	}
	return nil
}

// encodeSteps renders the step list for storage. An empty list is stored as the
// empty string rather than "null", so a plain job stays visibly plain in the
// database.
func encodeSteps(steps []Step) (string, error) {
	if len(steps) == 0 {
		return "", nil
	}
	b, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("encode steps: %w", err)
	}
	return string(b), nil
}

func decodeSteps(raw string) ([]Step, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var steps []Step
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil, fmt.Errorf("decode steps: %w", err)
	}
	return steps, nil
}

// Step outcomes. They are the run outcomes plus "pending", because a workflow
// declares its steps before it runs any of them: a step nobody has started yet
// is a real state, and the board says how many there are.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
	StepParked  = "parked"
)

// RunStep is one step of one workflow run: what it was, how it went, and where
// its output is.
//
// The rows exist from the moment the run starts, all of them pending, so the
// board can say "2/4" rather than only counting the steps that happened to have
// finished.
type RunStep struct {
	RunID  string
	Index  int
	StepID string
	// Kind is "agent" or "run", so a reader can see which steps cost tokens.
	Kind       string
	Outcome    string
	ParkReason string
	Note       string
	StepDir    string
	AgentName  string
	StartedAt  time.Time
	EndedAt    *time.Time
}

// Duration is how long the step took, or 0 while it is pending or running.
func (s RunStep) Duration() time.Duration {
	if s.EndedAt == nil || s.StartedAt.IsZero() {
		return 0
	}
	return s.EndedAt.Sub(s.StartedAt)
}

const runStepColumns = `run_id, idx, step_id, kind, outcome, park_reason, note,
	step_dir, agent_name, started_at, ended_at`

// PutRunStep inserts or updates one step of a run.
func (s *Store) PutRunStep(ctx context.Context, rs RunStep) error {
	var started any
	if !rs.StartedAt.IsZero() {
		started = rs.StartedAt.Unix()
	} else {
		started = 0
	}
	var ended any
	if rs.EndedAt != nil {
		ended = rs.EndedAt.Unix()
	}
	if rs.Outcome == "" {
		rs.Outcome = StepPending
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO run_steps (`+runStepColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id, step_id) DO UPDATE SET
		  idx=excluded.idx, kind=excluded.kind, outcome=excluded.outcome,
		  park_reason=excluded.park_reason, note=excluded.note,
		  step_dir=excluded.step_dir, agent_name=excluded.agent_name,
		  started_at=excluded.started_at, ended_at=excluded.ended_at`,
		rs.RunID, rs.Index, rs.StepID, rs.Kind, rs.Outcome, rs.ParkReason,
		rs.Note, rs.StepDir, rs.AgentName, started, ended)
	return err
}

// SeedRunSteps records a workflow's steps as pending, without touching any that
// are already there.
//
// Resume depends on the "without touching" part: the completed steps of an
// earlier attempt keep their outcome and their duration, which is the record of
// the expensive work resume exists not to repeat.
func (s *Store) SeedRunSteps(ctx context.Context, runID string, steps []Step) error {
	for i, st := range steps {
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO run_steps (`+runStepColumns+`)
			VALUES (?,?,?,?,?,'','','','',0,NULL)
			ON CONFLICT(run_id, step_id) DO NOTHING`,
			runID, i, st.ID, st.Label(), StepPending)
		if err != nil {
			return err
		}
	}
	return nil
}

func scanRunStep(rows interface{ Scan(...any) error }) (RunStep, error) {
	var rs RunStep
	var started int64
	var ended sql.NullInt64
	err := rows.Scan(&rs.RunID, &rs.Index, &rs.StepID, &rs.Kind, &rs.Outcome,
		&rs.ParkReason, &rs.Note, &rs.StepDir, &rs.AgentName, &started, &ended)
	if err != nil {
		return rs, err
	}
	if started != 0 {
		rs.StartedAt = time.Unix(started, 0)
	}
	if ended.Valid {
		t := time.Unix(ended.Int64, 0)
		rs.EndedAt = &t
	}
	return rs, nil
}

// RunSteps returns one run's steps in declared order.
func (s *Store) RunSteps(ctx context.Context, runID string) ([]RunStep, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runStepColumns+` FROM run_steps WHERE run_id=? ORDER BY idx`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunStep
	for rows.Next() {
		rs, err := scanRunStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rs)
	}
	return out, rows.Err()
}

// RunStepsFor returns the steps of many runs at once, keyed by run id.
//
// The board refreshes every three seconds with a hundred runs on screen, so it
// asks once rather than once per row.
func (s *Store) RunStepsFor(ctx context.Context, runIDs []string) (map[string][]RunStep, error) {
	out := map[string][]RunStep{}
	if len(runIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(runIDs))
	for i, id := range runIDs {
		args[i] = id
	}
	q := `SELECT ` + runStepColumns + ` FROM run_steps WHERE run_id IN (?` +
		strings.Repeat(",?", len(runIDs)-1) + `) ORDER BY idx`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		rs, err := scanRunStep(rows)
		if err != nil {
			return nil, err
		}
		out[rs.RunID] = append(out[rs.RunID], rs)
	}
	return out, rows.Err()
}
