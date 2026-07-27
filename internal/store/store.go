// Package store persists jobs and runs in SQLite.
//
// The store is the source of truth for what bermuda should run and what it has
// run. It is deliberately independent of Herdr: the scheduler must know its
// schedule even when no Herdr server exists.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so nix builds stay simple
)

// ErrNotFound is returned when a job or run does not exist.
var ErrNotFound = errors.New("not found")

// ScheduleType is how a job is triggered.
type ScheduleType string

const (
	// ScheduleManual runs only when asked.
	ScheduleManual ScheduleType = "manual"
	// ScheduleInterval runs every IntervalSeconds.
	ScheduleInterval ScheduleType = "interval"
	// ScheduleCron runs on a cron expression.
	ScheduleCron ScheduleType = "cron"
	// ScheduleOnce runs a single time at RunAt, then disables itself.
	ScheduleOnce ScheduleType = "once"
)

// DefaultModel is used when a job does not name one.
//
// A job always runs on a known model. Leaving the choice to whatever the agent
// happens to default to means a schedule's cost and capability can change
// underneath it without the job changing, so an unset model resolves to a
// stated one instead.
const DefaultModel = "sonnet"

// Catchup policies for fires missed while the daemon or Herdr was down.
const (
	CatchupLatest = "latest" // run once for the whole missed window
	CatchupAll    = "all"    // run once per missed fire
	CatchupSkip   = "skip"   // drop missed fires
)

// Store is a SQLite-backed job and run store.
type Store struct {
	db *sql.DB
	// TagsNormalized counts the rows the tag backfill rewrote on open.
	TagsNormalized int
}

// Job is a unit of work and everything needed to run it.
type Job struct {
	ID          string
	Name        string
	Description string
	Prompt      string
	CWD         string
	Kind        string // herdr agent kind
	// Steps is the declared sequence a flow job runs instead of Prompt.
	// Empty for the ordinary one-prompt job, which is most of them. See
	// steps.go.
	Steps []Step
	// Tags group related jobs. Stored comma-delimited.
	Tags []string

	// Agent invocation. These are modelled as fields rather than raw flags so
	// the board can show them and the daemon can validate them.
	Model           string
	PermissionMode  string
	AllowedTools    string
	DisallowedTools string
	AddDirs         []string
	ExtraArgs       string // shell-quoted or newline-separated passthrough
	SkipPermissions bool
	MaxBudgetUSD    string

	// Scheduling.
	Schedule        ScheduleType
	IntervalSeconds int
	CronExpr        string
	RunAt           *time.Time // for ScheduleOnce
	Catchup         string
	Timeout         time.Duration

	// Behaviour.
	Enabled bool
	// Favorite sorts a job to the top of the board.
	Favorite bool
	// Persistent keeps the job's agent alive between runs and prompts it
	// again, instead of starting a fresh agent. This is bermuda's equivalent
	// of resuming a conversation: Herdr exposes no agent session id, but it
	// can keep the agent itself.
	Persistent bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScheduleLabel renders the schedule for display.
func (j Job) ScheduleLabel() string {
	switch j.Schedule {
	case ScheduleCron:
		return j.CronExpr
	case ScheduleInterval:
		return "every " + (time.Duration(j.IntervalSeconds) * time.Second).String()
	case ScheduleOnce:
		if j.RunAt != nil {
			return "once " + j.RunAt.Format("2006-01-02 15:04")
		}
		return "once"
	default:
		return "manual"
	}
}

// OutcomeDone is the run outcome that means the work finished on its own.
const OutcomeDone = "done"

// Finished reports whether a one-shot job has nothing left to do, given its
// most recent run (nil when it has never run).
//
// The rule is deliberately narrow. A one-shot disables itself once it has run,
// so it sits in the list forever with no way to fire again — but only a run
// that ended in "done" says the work actually happened:
//
//   - parked still wants a human, which is the whole point of parking
//   - failed must stay in sight, because hiding a failure is how one goes
//     unnoticed
//   - never run is still pending, however old its --at time is
//
// A recurring job is never finished, whatever its last run says: it will fire
// again.
func (j Job) Finished(last *Run) bool {
	if j.Schedule != ScheduleOnce || last == nil {
		return false
	}
	return last.Outcome == OutcomeDone
}

// Run is one execution of a job.
type Run struct {
	ID         string
	JobID      string
	Trigger    string // manual | scheduled
	Outcome    string // running | done | failed | parked
	ParkReason string
	Status     string // last observed agent status
	Note       string
	RunDir     string
	TabID      string
	AgentName  string
	StartedAt  time.Time
	EndedAt    *time.Time

	// Token usage, read from the agent's session transcript after the run
	// settles. The four counts are billed differently, so they are kept apart,
	// and Model is kept because the same counts cost differently per model.
	// All zero means the usage could not be attributed, not that the run was
	// free.
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Model               string
}

// Duration returns how long the run took, or 0 while it is still going.
func (r Run) Duration() time.Duration {
	if r.EndedAt == nil {
		return 0
	}
	return r.EndedAt.Sub(r.StartedAt)
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
  id          TEXT PRIMARY KEY,
  prompt      TEXT NOT NULL,
  cwd         TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'claude',
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
  id          TEXT PRIMARY KEY,
  job_id      TEXT NOT NULL,
  outcome     TEXT NOT NULL,
  park_reason TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT '',
  note        TEXT NOT NULL DEFAULT '',
  run_dir     TEXT NOT NULL DEFAULT '',
  tab_id      TEXT NOT NULL DEFAULT '',
  agent_name  TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER
);

CREATE INDEX IF NOT EXISTS runs_job_started ON runs(job_id, started_at DESC);
CREATE INDEX IF NOT EXISTS runs_outcome ON runs(outcome);

-- One row per declared step of a flow run, written pending before the
-- flow starts so the board can say "2 of 4" rather than counting only the
-- steps that got far enough to report.
CREATE TABLE IF NOT EXISTS run_steps (
  run_id      TEXT NOT NULL,
  idx         INTEGER NOT NULL,
  step_id     TEXT NOT NULL,
  kind        TEXT NOT NULL DEFAULT '',
  outcome     TEXT NOT NULL DEFAULT 'pending',
  park_reason TEXT NOT NULL DEFAULT '',
  note        TEXT NOT NULL DEFAULT '',
  step_dir    TEXT NOT NULL DEFAULT '',
  agent_name  TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL DEFAULT 0,
  ended_at    INTEGER,
  PRIMARY KEY (run_id, step_id)
);
`

// addColumns are applied idempotently on every open, so an existing database
// picks up new fields without a migration tool.
var addColumns = []struct{ table, column, ddl string }{
	{"jobs", "name", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "description", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "tags", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "model", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "permission_mode", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "allowed_tools", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "disallowed_tools", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "add_dirs", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "extra_args", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "skip_permissions", "INTEGER NOT NULL DEFAULT 0"},
	{"jobs", "max_budget_usd", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "schedule_type", "TEXT NOT NULL DEFAULT 'manual'"},
	{"jobs", "interval_seconds", "INTEGER NOT NULL DEFAULT 0"},
	{"jobs", "cron_expr", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "run_at", "INTEGER"},
	{"jobs", "catchup", "TEXT NOT NULL DEFAULT 'latest'"},
	{"jobs", "timeout_ms", "INTEGER NOT NULL DEFAULT 0"},
	{"jobs", "enabled", "INTEGER NOT NULL DEFAULT 1"},
	{"jobs", "favorite", "INTEGER NOT NULL DEFAULT 0"},
	{"jobs", "persistent", "INTEGER NOT NULL DEFAULT 0"},
	{"jobs", "updated_at", "INTEGER NOT NULL DEFAULT 0"},
	{"jobs", "steps", "TEXT NOT NULL DEFAULT ''"},
	{"runs", "trigger", "TEXT NOT NULL DEFAULT 'manual'"},
	{"runs", "input_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"runs", "output_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"runs", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"runs", "cache_creation_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"runs", "model", "TEXT NOT NULL DEFAULT ''"},
}

// Open opens (and migrates) the store at dir/bermuda.db.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "bermuda.db")
	// busy_timeout keeps the board and the daemon from tripping over each
	// other on the same file; WAL lets them read and write concurrently.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateThreads(db); err != nil {
		db.Close()
		return nil, err
	}
	// Jobs written before the model became mandatory carry an empty one, which
	// would silently run on whatever the agent defaults to.
	if _, err := db.Exec(`UPDATE jobs SET model=? WHERE TRIM(model)=''`, DefaultModel); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill model: %w", err)
	}
	// Rows written before tags were sanitised can hold case-different or
	// oddly-spaced duplicates, which display and count as separate tags even
	// though filtering treats them as one.
	normalized, err := backfillTags(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill tags: %w", err)
	}
	return &Store{db: db, TagsNormalized: normalized}, nil
}

// backfillTags rewrites any job whose stored tag list is not already sanitised,
// and reports how many rows it changed. It is a no-op on a clean database.
func backfillTags(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT id, tags FROM jobs`)
	if err != nil {
		return 0, err
	}
	type fix struct{ id, tags string }
	var fixes []fix
	for rows.Next() {
		var id, tags string
		if err := rows.Scan(&id, &tags); err != nil {
			rows.Close()
			return 0, err
		}
		clean := strings.Join(SplitTags(tags), ",")
		if clean != tags {
			fixes = append(fixes, fix{id, clean})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, f := range fixes {
		if _, err := db.Exec(`UPDATE jobs SET tags=? WHERE id=?`, f.tags, f.id); err != nil {
			return 0, err
		}
	}
	return len(fixes), nil
}

// migrate adds any columns missing from an older database.
//
// Through addColumn, which holds one IMMEDIATE transaction across the check and
// the ALTER. This loop used to do both on a bare connection: the daemon, the
// board and any thread command all open this database, and two starting
// together would both see a column missing, both ALTER, and the loser would die
// on "duplicate column name" — refusing to open over a schema change that had
// already succeeded. The threads migration had this fixed, with a comment
// describing the bug that this copy still had.
func migrate(db *sql.DB) error {
	for _, c := range addColumns {
		if err := addColumn(db, c.table, c.column, c.ddl); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const jobColumns = `id, name, description, tags, prompt, cwd, kind, model, permission_mode,
	allowed_tools, disallowed_tools, add_dirs, extra_args, skip_permissions, max_budget_usd,
	schedule_type, interval_seconds, cron_expr, run_at, catchup, timeout_ms,
	enabled, favorite, persistent, created_at, updated_at, steps`

// PutJob inserts or replaces a job.
func (s *Store) PutJob(ctx context.Context, j Job) error {
	now := time.Now()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
	if j.Kind == "" {
		j.Kind = "claude"
	}
	if j.Catchup == "" {
		j.Catchup = CatchupLatest
	}
	if strings.TrimSpace(j.Model) == "" {
		j.Model = DefaultModel
	}
	if j.Schedule == "" {
		j.Schedule = ScheduleManual
	}
	// Tags can reach here without passing through SplitTags -- a caller that
	// builds a Job in code, or a migration -- so normalise on the way in.
	j.Tags = SanitizeTags(j.Tags)
	var runAt any
	if j.RunAt != nil {
		runAt = j.RunAt.Unix()
	}
	// A flow is checked on the way in, so an invalid one cannot be stored
	// and then discovered at 04:00 by the scheduler. The rules are in
	// ValidateSteps; the model is passed because a step that names none
	// inherits the job's.
	if err := ValidateSteps(j.Steps, j.Model); err != nil {
		return err
	}
	steps, err := encodeSteps(j.Steps)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jobs (`+jobColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  name=excluded.name, description=excluded.description, tags=excluded.tags,
		  prompt=excluded.prompt,
		  cwd=excluded.cwd, kind=excluded.kind, model=excluded.model,
		  permission_mode=excluded.permission_mode, allowed_tools=excluded.allowed_tools,
		  disallowed_tools=excluded.disallowed_tools, add_dirs=excluded.add_dirs,
		  extra_args=excluded.extra_args, skip_permissions=excluded.skip_permissions,
		  max_budget_usd=excluded.max_budget_usd, schedule_type=excluded.schedule_type,
		  interval_seconds=excluded.interval_seconds, cron_expr=excluded.cron_expr,
		  run_at=excluded.run_at, catchup=excluded.catchup, timeout_ms=excluded.timeout_ms,
		  enabled=excluded.enabled, favorite=excluded.favorite,
		  persistent=excluded.persistent, updated_at=excluded.updated_at,
		  steps=excluded.steps`,
		j.ID, j.Name, j.Description, strings.Join(j.Tags, ","),
		j.Prompt, j.CWD, j.Kind, j.Model, j.PermissionMode,
		j.AllowedTools, j.DisallowedTools, strings.Join(j.AddDirs, "\n"), j.ExtraArgs,
		boolToInt(j.SkipPermissions), j.MaxBudgetUSD,
		string(j.Schedule), j.IntervalSeconds, j.CronExpr, runAt, j.Catchup,
		j.Timeout.Milliseconds(), boolToInt(j.Enabled), boolToInt(j.Favorite),
		boolToInt(j.Persistent), j.CreatedAt.Unix(), j.UpdatedAt.Unix(), steps)
	return err
}

func scanJob(rows interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var addDirs, schedule, tags, steps string
	var runAt sql.NullInt64
	var timeoutMS, created, updated int64
	var skip, enabled, favorite, persistent int
	err := rows.Scan(&j.ID, &j.Name, &j.Description, &tags, &j.Prompt, &j.CWD, &j.Kind,
		&j.Model, &j.PermissionMode, &j.AllowedTools, &j.DisallowedTools, &addDirs,
		&j.ExtraArgs, &skip, &j.MaxBudgetUSD, &schedule, &j.IntervalSeconds,
		&j.CronExpr, &runAt, &j.Catchup, &timeoutMS, &enabled, &favorite,
		&persistent, &created, &updated, &steps)
	if err != nil {
		return j, err
	}
	if j.Steps, err = decodeSteps(steps); err != nil {
		return j, err
	}
	if addDirs != "" {
		j.AddDirs = strings.Split(addDirs, "\n")
	}
	j.Tags = SplitTags(tags)
	j.Schedule = ScheduleType(schedule)
	if runAt.Valid {
		t := time.Unix(runAt.Int64, 0)
		j.RunAt = &t
	}
	j.Timeout = time.Duration(timeoutMS) * time.Millisecond
	j.SkipPermissions = skip != 0
	j.Enabled = enabled != 0
	j.Favorite = favorite != 0
	j.Persistent = persistent != 0
	j.CreatedAt = time.Unix(created, 0)
	j.UpdatedAt = time.Unix(updated, 0)
	return j, nil
}

// Jobs returns all jobs, favorites first.
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobColumns+` FROM jobs ORDER BY favorite DESC, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Job returns one job by id.
func (s *Store) Job(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// Exists reports whether a job id is taken.
func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM jobs WHERE id=?`, id).Scan(&n)
	return n > 0, err
}

// NameTaken reports whether another job already uses this name.
//
// Names are compared case-insensitively and ignoring surrounding space, since
// the board identifies jobs by name: two jobs called "Daily brief" and "daily
// brief " would be indistinguishable on screen.
func (s *Store) NameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM jobs WHERE id != ?`, exceptID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, other string
		if err := rows.Scan(&id, &other); err != nil {
			return false, err
		}
		if strings.ToLower(strings.TrimSpace(other)) == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// SetEnabled pauses or resumes a job.
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET enabled=?, updated_at=? WHERE id=?`,
		boolToInt(enabled), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteJob removes a job. Its run history is kept.
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const runColumns = `id, job_id, trigger, outcome, park_reason, status, note,
	run_dir, tab_id, agent_name, started_at, ended_at,
	input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, model`

// PutRun inserts or updates a run.
func (s *Store) PutRun(ctx context.Context, r Run) error {
	var ended any
	if r.EndedAt != nil {
		ended = r.EndedAt.Unix()
	}
	if r.Trigger == "" {
		r.Trigger = "manual"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (`+runColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  trigger=excluded.trigger, outcome=excluded.outcome,
		  park_reason=excluded.park_reason, status=excluded.status,
		  note=excluded.note, run_dir=excluded.run_dir, tab_id=excluded.tab_id,
		  agent_name=excluded.agent_name, ended_at=excluded.ended_at,
		  input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
		  cache_read_tokens=excluded.cache_read_tokens,
		  cache_creation_tokens=excluded.cache_creation_tokens,
		  model=excluded.model`,
		r.ID, r.JobID, r.Trigger, r.Outcome, r.ParkReason, r.Status, r.Note,
		r.RunDir, r.TabID, r.AgentName, r.StartedAt.Unix(), ended,
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens,
		r.Model)
	return err
}

func scanRun(rows interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var started int64
	var ended sql.NullInt64
	err := rows.Scan(&r.ID, &r.JobID, &r.Trigger, &r.Outcome, &r.ParkReason,
		&r.Status, &r.Note, &r.RunDir, &r.TabID, &r.AgentName, &started, &ended,
		&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens,
		&r.CacheCreationTokens, &r.Model)
	if err != nil {
		return r, err
	}
	r.StartedAt = time.Unix(started, 0)
	if ended.Valid {
		t := time.Unix(ended.Int64, 0)
		r.EndedAt = &t
	}
	return r, nil
}

// Runs returns the most recent runs, newest first. outcome and jobID filter
// when set.
func (s *Store) Runs(ctx context.Context, outcome string, limit int) ([]Run, error) {
	return s.query(ctx, outcome, "", limit)
}

// JobRuns returns the most recent runs for one job.
func (s *Store) JobRuns(ctx context.Context, jobID string, limit int) ([]Run, error) {
	return s.query(ctx, "", jobID, limit)
}

func (s *Store) query(ctx context.Context, outcome, jobID string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + runColumns + ` FROM runs`
	var where []string
	var args []any
	if outcome != "" {
		where = append(where, "outcome=?")
		args = append(args, outcome)
	}
	if jobID != "" {
		where = append(where, "job_id=?")
		args = append(args, jobID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Run returns one run by id, or ErrNotFound.
func (s *Store) Run(ctx context.Context, id string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=?`, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("run %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LastRun returns the most recent run for a job, or ErrNotFound.
func (s *Store) LastRun(ctx context.Context, jobID string) (*Run, error) {
	runs, err := s.JobRuns(ctx, jobID, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, ErrNotFound
	}
	return &runs[0], nil
}

// SplitTags parses a comma-delimited tag list and sanitises it: blanks are
// trimmed so a trailing comma or stray spacing cannot produce an empty tag,
// and the result is normalised by SanitizeTags. Every write path goes through
// here, so none of them has to remember to ask.
func SplitTags(s string) []string {
	return SanitizeTags(strings.Split(s, ","))
}

// SanitizeTags normalises a tag list: each tag is trimmed, its internal runs of
// whitespace collapsed to a single space, and lower-cased; empties are dropped
// and duplicates removed, keeping first-seen order.
//
// Case carries no meaning -- tag filtering is case-insensitive -- so keeping it
// would only produce near-duplicates like "daily" and "DAILY" that display and
// count as two tags but filter as one. Order is how the author grouped them, so
// it is preserved rather than sorted.
func SanitizeTags(tags []string) []string {
	var out []string
	seen := make(map[string]bool, len(tags))
	for _, t := range tags {
		t = normalizeTag(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// HasTag reports whether the job carries a tag, case-insensitively.
//
// Both sides go through normalizeTag, so the question is asked in the same
// shape the tag was stored in. Trimming only the ends was not enough: a tag
// saved as "release notes" could not be matched by "release  notes", because
// SanitizeTags had collapsed the run of spaces and this had not.
func (j Job) HasTag(tag string) bool {
	tag = normalizeTag(tag)
	for _, t := range j.Tags {
		if normalizeTag(t) == tag {
			return true
		}
	}
	return false
}

// normalizeTag is the one definition of what makes two tags the same tag.
func normalizeTag(t string) string {
	return strings.ToLower(strings.Join(strings.Fields(t), " "))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
