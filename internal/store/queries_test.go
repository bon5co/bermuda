package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// The job and run queries are what every reader of this store actually calls:
// the board lists with them, the daemon decides with them, and the CLI reports
// with them. None of them had a test, and each one can be wrong quietly —
// a lost ordering, a filter that does not filter, a missing row reported as an
// empty result rather than an error.

func openStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, context.Background()
}

// putJob stores a job with an explicit creation time, because Jobs orders by it
// and PutJob would otherwise stamp every job in a test with the same second.
func putJob(t *testing.T, s *Store, ctx context.Context, j Job) {
	t.Helper()
	if j.CreatedAt.IsZero() {
		t.Fatalf("putJob(%q): give the job a CreatedAt, or ordering is undefined", j.ID)
	}
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("put job %q: %v", j.ID, err)
	}
}

func putRun(t *testing.T, s *Store, ctx context.Context, r Run) {
	t.Helper()
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatalf("put run %q: %v", r.ID, err)
	}
}

func ids(jobs []Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.ID
	}
	return out
}

func runIDs(runs []Run) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The board draws the job list in exactly the order this returns, so the order
// is the contract: favorites first, and oldest first within each group. A job
// starred to keep it at the top that sorts back into the middle is the whole
// point of the flag not working.
func TestJobsOrdersFavoritesFirstThenOldest(t *testing.T) {
	s, ctx := openStore(t)
	base := time.Unix(1_700_000_000, 0)
	putJob(t, s, ctx, Job{ID: "b", Name: "b", CreatedAt: base.Add(2 * time.Hour)})
	putJob(t, s, ctx, Job{ID: "a", Name: "a", CreatedAt: base.Add(1 * time.Hour)})
	putJob(t, s, ctx, Job{ID: "star-late", Name: "star late", Favorite: true, CreatedAt: base.Add(3 * time.Hour)})
	putJob(t, s, ctx, Job{ID: "star-early", Name: "star early", Favorite: true, CreatedAt: base})

	jobs, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	want := []string{"star-early", "star-late", "a", "b"}
	if got := ids(jobs); !equal(got, want) {
		t.Fatalf("Jobs order = %v, want %v", got, want)
	}
}

// An empty store is an empty list, not an error. The board calls this on first
// launch before any job exists.
func TestJobsEmptyStore(t *testing.T) {
	s, ctx := openStore(t)
	jobs, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("Jobs on an empty store = %v, want none", ids(jobs))
	}
}

// Jobs must round-trip the fields it reads back, not just the ids. scanJob
// reads twenty-eight columns positionally: swap two of the same type and every
// row still scans, silently reporting one field's value as another's.
func TestJobsRoundTripsFields(t *testing.T) {
	s, ctx := openStore(t)
	at := time.Unix(1_700_100_000, 0)
	putJob(t, s, ctx, Job{
		ID: "full", Name: "Full job", Description: "does the thing",
		Tags: []string{"daily", "brief"}, Prompt: "go", CWD: "/tmp/work",
		Kind: "claude", Model: "opus", PermissionMode: "acceptEdits",
		AllowedTools: "Bash", DisallowedTools: "Write",
		AddDirs: []string{"/one", "/two"}, ExtraArgs: "--verbose",
		SkipPermissions: true, MaxBudgetUSD: "5.00",
		Schedule: ScheduleOnce, IntervalSeconds: 90, CronExpr: "0 9 * * *",
		RunAt: &at, Catchup: CatchupAll, Timeout: 30 * time.Minute,
		Enabled: true, Favorite: true, Persistent: true, KeepContext: true,
		Flow: "nightly", Input: "x",
		CreatedAt: time.Unix(1_700_000_000, 0),
	})

	jobs, err := s.Jobs(ctx)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	g := jobs[0]
	checks := []struct {
		field     string
		got, want any
	}{
		{"Name", g.Name, "Full job"},
		{"Description", g.Description, "does the thing"},
		{"Prompt", g.Prompt, "go"},
		{"CWD", g.CWD, "/tmp/work"},
		{"Kind", g.Kind, "claude"},
		{"Model", g.Model, "opus"},
		{"PermissionMode", g.PermissionMode, "acceptEdits"},
		{"AllowedTools", g.AllowedTools, "Bash"},
		{"DisallowedTools", g.DisallowedTools, "Write"},
		{"ExtraArgs", g.ExtraArgs, "--verbose"},
		{"SkipPermissions", g.SkipPermissions, true},
		{"MaxBudgetUSD", g.MaxBudgetUSD, "5.00"},
		{"Schedule", g.Schedule, ScheduleOnce},
		{"IntervalSeconds", g.IntervalSeconds, 90},
		{"CronExpr", g.CronExpr, "0 9 * * *"},
		{"Catchup", g.Catchup, CatchupAll},
		{"Timeout", g.Timeout, 30 * time.Minute},
		{"Enabled", g.Enabled, true},
		{"Favorite", g.Favorite, true},
		{"Persistent", g.Persistent, true},
		{"KeepContext", g.KeepContext, true},
		{"Flow", g.Flow, "nightly"},
		{"Input", g.Input, "x"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
	if !equal(g.Tags, []string{"daily", "brief"}) {
		t.Errorf("Tags = %v, want [daily brief]", g.Tags)
	}
	if !equal(g.AddDirs, []string{"/one", "/two"}) {
		t.Errorf("AddDirs = %v, want [/one /two]", g.AddDirs)
	}
	if g.RunAt == nil || !g.RunAt.Equal(at) {
		t.Errorf("RunAt = %v, want %v", g.RunAt, at)
	}
}

func TestExists(t *testing.T) {
	s, ctx := openStore(t)
	putJob(t, s, ctx, Job{ID: "here", Name: "here", CreatedAt: time.Unix(1, 0)})

	for _, c := range []struct {
		name string
		id   string
		want bool
	}{
		{"stored id", "here", true},
		{"unknown id", "gone", false},
		{"empty id", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Exists(ctx, c.id)
			if err != nil {
				t.Fatalf("exists: %v", err)
			}
			if got != c.want {
				t.Fatalf("Exists(%q) = %v, want %v", c.id, got, c.want)
			}
		})
	}
}

// NameTaken guards the one thing a person uses to tell two jobs apart on the
// board. It has to be liberal about what counts as the same name and strict
// about excluding the job being edited — otherwise renaming a job to itself is
// refused, and every rename dialog is stuck.
func TestNameTaken(t *testing.T) {
	s, ctx := openStore(t)
	putJob(t, s, ctx, Job{ID: "brief", Name: "Daily brief", CreatedAt: time.Unix(1, 0)})
	putJob(t, s, ctx, Job{ID: "other", Name: "Weekly review", CreatedAt: time.Unix(2, 0)})

	cases := []struct {
		name     string
		ask      string
		exceptID string
		want     bool
	}{
		{"exact match against another job", "Daily brief", "other", true},
		{"different case", "DAILY BRIEF", "other", true},
		{"surrounding space", "  Daily brief  ", "other", true},
		{"the job's own name, editing itself", "Daily brief", "brief", false},
		{"a genuinely free name", "Nightly sweep", "brief", false},
		{"empty name is never taken", "", "", false},
		{"blank name is never taken", "   ", "", false},
		{"no exception given still sees every job", "Weekly review", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.NameTaken(ctx, c.ask, c.exceptID)
			if err != nil {
				t.Fatalf("name taken: %v", err)
			}
			if got != c.want {
				t.Fatalf("NameTaken(%q, except %q) = %v, want %v", c.ask, c.exceptID, got, c.want)
			}
		})
	}
}

// Pausing a job is how a person stops a schedule without losing it, so the flag
// has to survive the round trip, and asking to pause a job that is not there
// must say so rather than succeed silently.
func TestSetEnabled(t *testing.T) {
	s, ctx := openStore(t)
	putJob(t, s, ctx, Job{ID: "j", Name: "j", Enabled: true, CreatedAt: time.Unix(1, 0)})

	if err := s.SetEnabled(ctx, "j", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	j, err := s.Job(ctx, "j")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if j.Enabled {
		t.Fatal("job still enabled after SetEnabled(false)")
	}

	if err := s.SetEnabled(ctx, "j", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	j, err = s.Job(ctx, "j")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !j.Enabled {
		t.Fatal("job still disabled after SetEnabled(true)")
	}

	if err := s.SetEnabled(ctx, "nope", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetEnabled on a missing job = %v, want ErrNotFound", err)
	}
}

// SetEnabled leaves everything but the flag alone. It writes a bare UPDATE
// rather than going through PutJob, and the reason that matters is that PutJob
// would have to reconstruct every other column to do it.
func TestSetEnabledLeavesTheRestOfTheJobAlone(t *testing.T) {
	s, ctx := openStore(t)
	before := Job{
		ID: "j", Name: "keep me", Prompt: "p", Tags: []string{"daily"},
		Model: "opus", Schedule: ScheduleCron, CronExpr: "0 9 * * *",
		Enabled: true, Favorite: true, CreatedAt: time.Unix(1_700_000_000, 0),
	}
	putJob(t, s, ctx, before)

	if err := s.SetEnabled(ctx, "j", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	after, err := s.Job(ctx, "j")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Name != before.Name || after.Prompt != before.Prompt ||
		after.Model != before.Model || after.Schedule != before.Schedule ||
		after.CronExpr != before.CronExpr || !after.Favorite {
		t.Fatalf("SetEnabled changed more than the flag: %+v", *after)
	}
	if !equal(after.Tags, before.Tags) {
		t.Fatalf("tags = %v, want %v", after.Tags, before.Tags)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("CreatedAt moved: %v, want %v", after.CreatedAt, before.CreatedAt)
	}
}

// Deleting a job keeps its runs. That is documented on DeleteJob and it is what
// makes the run list a record rather than a view: a failure from a job someone
// has since removed is still the evidence of what went wrong.
func TestDeleteJobKeepsRunHistory(t *testing.T) {
	s, ctx := openStore(t)
	putJob(t, s, ctx, Job{ID: "j", Name: "j", CreatedAt: time.Unix(1, 0)})
	putRun(t, s, ctx, Run{ID: "r1", JobID: "j", Outcome: OutcomeDone, StartedAt: time.Unix(10, 0)})

	if err := s.DeleteJob(ctx, "j"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Job(ctx, "j"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("job after delete = %v, want ErrNotFound", err)
	}
	runs, err := s.JobRuns(ctx, "j", 0)
	if err != nil {
		t.Fatalf("job runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "r1" {
		t.Fatalf("run history after deleting the job = %v, want [r1]", runIDs(runs))
	}

	if err := s.DeleteJob(ctx, "j"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

// A job's creation time is set once. PutJob is an upsert and every edit goes
// through it, so if the conflict branch wrote created_at the way the insert
// does, saving a job from a form that does not carry the field would restamp it
// as new — and Jobs orders by exactly that column, so the job would jump to the
// bottom of the board on every edit.
func TestPutJobKeepsCreatedAtOnUpdate(t *testing.T) {
	s, ctx := openStore(t)
	born := time.Unix(1_600_000_000, 0)
	putJob(t, s, ctx, Job{ID: "j", Name: "j", Prompt: "one", CreatedAt: born})

	// The edit path: a job rebuilt from a form, carrying no creation time.
	if err := s.PutJob(ctx, Job{ID: "j", Name: "j", Prompt: "two"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.Job(ctx, "j")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.CreatedAt.Equal(born) {
		t.Fatalf("CreatedAt = %v after an update, want the original %v", got.CreatedAt, born)
	}
	if got.Prompt != "two" {
		t.Fatalf("Prompt = %q, want the updated %q", got.Prompt, "two")
	}
	if !got.UpdatedAt.After(born) {
		t.Fatalf("UpdatedAt = %v, want it moved past %v", got.UpdatedAt, born)
	}
}

// Reading a job and writing it straight back must change nothing. This is what
// every edit in the board does — read, change one field, save — so any field
// the upsert drops on the way through is lost by an edit that never touched it.
func TestPutJobRoundTripChangesNothing(t *testing.T) {
	s, ctx := openStore(t)
	at := time.Unix(1_700_100_000, 0)
	putJob(t, s, ctx, Job{
		ID: "j", Name: "Full", Description: "d", Tags: []string{"daily", "brief"},
		Prompt: "p", CWD: "/w", Kind: "claude", Model: "opus",
		PermissionMode: "acceptEdits", AllowedTools: "Bash", DisallowedTools: "Write",
		AddDirs: []string{"/one", "/two"}, ExtraArgs: "--v", SkipPermissions: true,
		MaxBudgetUSD: "5.00", Schedule: ScheduleOnce, IntervalSeconds: 90,
		CronExpr: "0 9 * * *", RunAt: &at, Catchup: CatchupAll,
		Timeout: 30 * time.Minute, Enabled: true, Favorite: true, Persistent: true,
		Flow: "nightly", Input: "x", CreatedAt: time.Unix(1_600_000_000, 0),
	})

	before, err := s.Job(ctx, "j")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := s.PutJob(ctx, *before); err != nil {
		t.Fatalf("write back: %v", err)
	}
	after, err := s.Job(ctx, "j")
	if err != nil {
		t.Fatalf("read again: %v", err)
	}

	// UpdatedAt is meant to move; nothing else is.
	before.UpdatedAt, after.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(*before, *after) {
		t.Fatalf("round trip changed the job:\n before %+v\n after  %+v", *before, *after)
	}
}

// A run is written twice: once when it starts and once when it ends. The second
// write is the whole Run struct again, so the update must not move the start
// time or reassign the job — the duration on every finished run, and the job a
// run is filed under, both depend on the first write surviving the second.
func TestPutRunUpdateKeepsStartAndJob(t *testing.T) {
	s, ctx := openStore(t)
	started := time.Unix(1_700_000_000, 0)
	putRun(t, s, ctx, Run{ID: "r", JobID: "j", Trigger: "scheduled",
		Outcome: "running", StartedAt: started})

	// The finishing write, from a caller that lost the start time and the job.
	ended := started.Add(2 * time.Minute)
	putRun(t, s, ctx, Run{ID: "r", Outcome: OutcomeDone, Note: "n", EndedAt: &ended})

	got, err := s.Run(ctx, "r")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.StartedAt.Equal(started) {
		t.Fatalf("StartedAt = %v after the finishing write, want %v", got.StartedAt, started)
	}
	if got.JobID != "j" {
		t.Fatalf("JobID = %q after the finishing write, want j", got.JobID)
	}
	if got.Outcome != OutcomeDone || got.Note != "n" {
		t.Fatalf("the finishing write did not land: %+v", *got)
	}
	if got.Duration() != 2*time.Minute {
		t.Fatalf("Duration = %v, want 2m", got.Duration())
	}
}

// The run list is read newest-first everywhere it is shown. Reversed, the board
// would report a stale run as the current state of a job.
func TestRunsNewestFirst(t *testing.T) {
	s, ctx := openStore(t)
	base := time.Unix(1_700_000_000, 0)
	putRun(t, s, ctx, Run{ID: "old", JobID: "j", Outcome: OutcomeDone, StartedAt: base})
	putRun(t, s, ctx, Run{ID: "new", JobID: "j", Outcome: OutcomeDone, StartedAt: base.Add(2 * time.Hour)})
	putRun(t, s, ctx, Run{ID: "mid", JobID: "j", Outcome: OutcomeDone, StartedAt: base.Add(time.Hour)})

	runs, err := s.Runs(ctx, "", 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	want := []string{"new", "mid", "old"}
	if got := runIDs(runs); !equal(got, want) {
		t.Fatalf("Runs order = %v, want %v", got, want)
	}
}

// The outcome filter is what "show me the failures" is built on. A filter that
// quietly matched everything would report a clean board as a broken one and,
// worse, the other way round.
func TestRunsFiltersByOutcome(t *testing.T) {
	s, ctx := openStore(t)
	base := time.Unix(1_700_000_000, 0)
	putRun(t, s, ctx, Run{ID: "d1", JobID: "a", Outcome: "done", StartedAt: base})
	putRun(t, s, ctx, Run{ID: "f1", JobID: "a", Outcome: "failed", StartedAt: base.Add(time.Minute)})
	putRun(t, s, ctx, Run{ID: "p1", JobID: "b", Outcome: "parked", StartedAt: base.Add(2 * time.Minute)})
	putRun(t, s, ctx, Run{ID: "f2", JobID: "b", Outcome: "failed", StartedAt: base.Add(3 * time.Minute)})

	for _, c := range []struct {
		name    string
		outcome string
		want    []string
	}{
		{"no filter returns every run", "", []string{"f2", "p1", "f1", "d1"}},
		{"failed only", "failed", []string{"f2", "f1"}},
		{"parked only", "parked", []string{"p1"}},
		{"an outcome nothing has", "running", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			runs, err := s.Runs(ctx, c.outcome, 0)
			if err != nil {
				t.Fatalf("runs: %v", err)
			}
			if got := runIDs(runs); !equal(got, c.want) {
				t.Fatalf("Runs(%q) = %v, want %v", c.outcome, got, c.want)
			}
		})
	}
}

// limit <= 0 means "the default", not "no rows" and not "all of them". The
// board and the CLI both pass 0 to mean "a sensible page", and a query that
// took it literally would show an empty list on a store full of runs.
func TestRunsLimit(t *testing.T) {
	s, ctx := openStore(t)
	base := time.Unix(1_700_000_000, 0)
	const total = 60
	for i := range total {
		putRun(t, s, ctx, Run{
			ID:    string(rune('a'+i/26)) + string(rune('a'+i%26)),
			JobID: "j", Outcome: OutcomeDone,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	for _, c := range []struct {
		name  string
		limit int
		want  int
	}{
		{"zero means the default page", 0, 50},
		{"negative means the default page", -1, 50},
		{"an explicit smaller limit", 3, 3},
		{"a limit beyond what exists", 500, total},
	} {
		t.Run(c.name, func(t *testing.T) {
			runs, err := s.Runs(ctx, "", c.limit)
			if err != nil {
				t.Fatalf("runs: %v", err)
			}
			if len(runs) != c.want {
				t.Fatalf("Runs(limit=%d) returned %d runs, want %d", c.limit, len(runs), c.want)
			}
		})
	}

	// And the default page is the newest 50, not an arbitrary 50.
	runs, err := s.Runs(ctx, "", 0)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	newest := base.Add((total - 1) * time.Minute)
	if !runs[0].StartedAt.Equal(newest) {
		t.Fatalf("first of the default page started %v, want the newest %v", runs[0].StartedAt, newest)
	}
}

// JobRuns must narrow to one job. Two jobs' histories interleaved on a job's
// own detail pane would attribute one job's failure to another.
func TestJobRunsFiltersByJob(t *testing.T) {
	s, ctx := openStore(t)
	base := time.Unix(1_700_000_000, 0)
	putRun(t, s, ctx, Run{ID: "a1", JobID: "a", Outcome: OutcomeDone, StartedAt: base})
	putRun(t, s, ctx, Run{ID: "b1", JobID: "b", Outcome: OutcomeDone, StartedAt: base.Add(time.Minute)})
	putRun(t, s, ctx, Run{ID: "a2", JobID: "a", Outcome: "failed", StartedAt: base.Add(2 * time.Minute)})

	runs, err := s.JobRuns(ctx, "a", 0)
	if err != nil {
		t.Fatalf("job runs: %v", err)
	}
	if got := runIDs(runs); !equal(got, []string{"a2", "a1"}) {
		t.Fatalf("JobRuns(a) = %v, want [a2 a1]", got)
	}

	none, err := s.JobRuns(ctx, "never-ran", 0)
	if err != nil {
		t.Fatalf("job runs: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("JobRuns for a job with no runs = %v, want none", runIDs(none))
	}
}

// LastRun is what the board shows as a job's current state and what Finished
// decides on. "Never run" has to be distinguishable from "ran and we could not
// read it", which is why it is an error and not a nil run.
func TestLastRun(t *testing.T) {
	s, ctx := openStore(t)
	base := time.Unix(1_700_000_000, 0)
	putRun(t, s, ctx, Run{ID: "first", JobID: "j", Outcome: OutcomeDone, StartedAt: base})
	putRun(t, s, ctx, Run{ID: "latest", JobID: "j", Outcome: "failed", StartedAt: base.Add(time.Hour)})
	putRun(t, s, ctx, Run{ID: "elsewhere", JobID: "other", Outcome: OutcomeDone, StartedAt: base.Add(2 * time.Hour)})

	last, err := s.LastRun(ctx, "j")
	if err != nil {
		t.Fatalf("last run: %v", err)
	}
	if last.ID != "latest" {
		t.Fatalf("LastRun(j) = %q, want latest", last.ID)
	}

	if _, err := s.LastRun(ctx, "never-ran"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LastRun for a job with no runs = %v, want ErrNotFound", err)
	}
}

// LastRuns answers the same question as LastRun for every job at once, and it
// has to keep answering it for a job whose last run is buried under a busier
// one — that is the whole reason it exists rather than the board taking the
// newest N runs and keeping the first sighting of each job.
func TestLastRuns(t *testing.T) {
	s, ctx := openStore(t)
	base := time.Unix(1_700_000_000, 0)
	putRun(t, s, ctx, Run{ID: "quiet", JobID: "quiet", Outcome: OutcomeDone, StartedAt: base})
	for i := 0; i < 20; i++ {
		putRun(t, s, ctx, Run{ID: "busy-" + string(rune('a'+i)), JobID: "busy",
			Outcome: "failed", StartedAt: base.Add(time.Duration(i+1) * time.Hour)})
	}
	// A flow called directly has no job, so there is nothing to key it under.
	putRun(t, s, ctx, Run{ID: "loose", Outcome: OutcomeDone, Flow: "adhoc",
		StartedAt: base.Add(100 * time.Hour)})

	last, err := s.LastRuns(ctx)
	if err != nil {
		t.Fatalf("last runs: %v", err)
	}
	if len(last) != 2 {
		t.Fatalf("LastRuns returned %d jobs, want 2: %v", len(last), last)
	}
	if got := last["quiet"].ID; got != "quiet" {
		t.Errorf("quiet job's last run is %q, want quiet", got)
	}
	if got := last["busy"].ID; got != "busy-t" {
		t.Errorf("busy job's last run is %q, want busy-t", got)
	}
	if _, ok := last[""]; ok {
		t.Error("a run with no job was keyed under the empty job id")
	}
}

// Runs carry the token counts the usage report bills from, and scanRun reads
// four same-typed counters in a row: swapped, every run still scans and the
// cheap column is billed as the expensive one.
func TestRunRoundTripsUsageAndFlow(t *testing.T) {
	s, ctx := openStore(t)
	started := time.Unix(1_700_000_000, 0)
	ended := started.Add(90 * time.Second)
	putRun(t, s, ctx, Run{
		ID: "r", JobID: "j", Trigger: "scheduled", Outcome: "parked",
		ParkReason: "needs a human", Status: "waiting", Note: "n",
		RunDir: "/runs/r", TabID: "t1", AgentName: "bermuda-r",
		StartedAt: started, EndedAt: &ended,
		InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheCreationTokens: 4,
		Model: "opus", Flow: "nightly", Input: "x",
		Space: "ws-7", Thread: "flow-nightly-101500z",
	})

	got, err := s.Run(ctx, "r")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	checks := []struct {
		field     string
		got, want any
	}{
		{"Trigger", got.Trigger, "scheduled"},
		{"Outcome", got.Outcome, "parked"},
		{"ParkReason", got.ParkReason, "needs a human"},
		{"Status", got.Status, "waiting"},
		{"Note", got.Note, "n"},
		{"RunDir", got.RunDir, "/runs/r"},
		{"TabID", got.TabID, "t1"},
		{"AgentName", got.AgentName, "bermuda-r"},
		{"InputTokens", got.InputTokens, int64(1)},
		{"OutputTokens", got.OutputTokens, int64(2)},
		{"CacheReadTokens", got.CacheReadTokens, int64(3)},
		{"CacheCreationTokens", got.CacheCreationTokens, int64(4)},
		{"Model", got.Model, "opus"},
		{"Flow", got.Flow, "nightly"},
		{"Input", got.Input, "x"},
		// A resume reads these back to land in the space and the thread the first
		// attempt used. Lost, the second half of one run holds its conversation
		// somewhere else, and nothing says so.
		{"Space", got.Space, "ws-7"},
		{"Thread", got.Thread, "flow-nightly-101500z"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("EndedAt = %v, want %v", got.EndedAt, ended)
	}
	if got.Duration() != 90*time.Second {
		t.Fatalf("Duration = %v, want 90s", got.Duration())
	}
}

// A run written with no trigger is a manual one. The daemon always names its
// trigger; a run created from the board or a flow does not, and an empty
// trigger in the column would make the usage split by trigger silently wrong.
func TestPutRunDefaultsTriggerToManual(t *testing.T) {
	s, ctx := openStore(t)
	putRun(t, s, ctx, Run{ID: "r", JobID: "j", Outcome: "running", StartedAt: time.Unix(1, 0)})
	got, err := s.Run(ctx, "r")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.Trigger != "manual" {
		t.Fatalf("Trigger = %q, want manual", got.Trigger)
	}
}

func TestRunNotFound(t *testing.T) {
	s, ctx := openStore(t)
	if _, err := s.Run(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Run on a missing id = %v, want ErrNotFound", err)
	}
}

// A run still going has no duration yet. Subtracting from a zero EndedAt would
// report a run that started this morning as having taken minus fifty years.
func TestRunDurationWhileRunning(t *testing.T) {
	r := Run{StartedAt: time.Unix(1_700_000_000, 0)}
	if d := r.Duration(); d != 0 {
		t.Fatalf("Duration of a running run = %v, want 0", d)
	}
}

func TestScheduleLabel(t *testing.T) {
	at := time.Date(2026, 3, 4, 9, 30, 0, 0, time.Local)
	cases := []struct {
		name string
		job  Job
		want string
	}{
		{"cron shows the expression", Job{Schedule: ScheduleCron, CronExpr: "0 9 * * *"}, "0 9 * * *"},
		{"interval shows the period", Job{Schedule: ScheduleInterval, IntervalSeconds: 5400}, "every 1h30m0s"},
		{"once shows the time", Job{Schedule: ScheduleOnce, RunAt: &at}, "once 2026-03-04 09:30"},
		{"once with no time", Job{Schedule: ScheduleOnce}, "once"},
		{"manual", Job{Schedule: ScheduleManual}, "manual"},
		{"unset schedule reads as manual", Job{}, "manual"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.job.ScheduleLabel(); got != c.want {
				t.Fatalf("ScheduleLabel() = %q, want %q", got, c.want)
			}
		})
	}
}

// Finished is what hides a one-shot from the board once it has served its
// purpose. Every wrong answer here loses something: too eager and a parked or
// failed one-shot disappears with work still owed, too shy and every one-shot
// ever run stays on screen forever.
func TestFinished(t *testing.T) {
	done := &Run{Outcome: OutcomeDone}
	cases := []struct {
		name string
		job  Job
		last *Run
		want bool
	}{
		{"one-shot that finished", Job{Schedule: ScheduleOnce}, done, true},
		{"one-shot that has never run", Job{Schedule: ScheduleOnce}, nil, false},
		{"one-shot still running", Job{Schedule: ScheduleOnce}, &Run{Outcome: "running"}, false},
		{"one-shot that failed", Job{Schedule: ScheduleOnce}, &Run{Outcome: "failed"}, false},
		{"one-shot parked for a human", Job{Schedule: ScheduleOnce}, &Run{Outcome: "parked"}, false},
		{"cron job whose last run finished", Job{Schedule: ScheduleCron}, done, false},
		{"interval job whose last run finished", Job{Schedule: ScheduleInterval}, done, false},
		{"manual job whose last run finished", Job{Schedule: ScheduleManual}, done, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.job.Finished(c.last); got != c.want {
				t.Fatalf("Finished() = %v, want %v", got, c.want)
			}
		})
	}
}

// A step's model is decided here and nowhere else, and the answer is what gets
// billed. Falling through to the job when the step names one would run a
// judgement step on whatever the job happened to be set to.
func TestEffectiveModel(t *testing.T) {
	cases := []struct {
		name string
		job  Job
		step Step
		want string
	}{
		{"the step's own model wins", Job{Model: "sonnet"}, Step{Model: "opus"}, "opus"},
		{"the job's model when the step names none", Job{Model: "opus"}, Step{}, "opus"},
		{"blank step model is not a model", Job{Model: "opus"}, Step{Model: "   "}, "opus"},
		{"the default when neither names one", Job{}, Step{}, DefaultModel},
		{"blank on both sides is still the default", Job{Model: " "}, Step{Model: " "}, DefaultModel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.job.EffectiveModel(c.step); got != c.want {
				t.Fatalf("EffectiveModel() = %q, want %q", got, c.want)
			}
		})
	}
}
