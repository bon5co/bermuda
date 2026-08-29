package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// jobCmdEnv points the whole command surface at a temp state directory, so
// `job add` and friends write to a throwaway database instead of the real
// ~/.bermuda one. Everything below drives the argv-taking commands rather than
// their inner helpers: the guards that matter -- add refusing to overwrite,
// edit refusing a duplicate name -- live in those wrappers and nowhere else.
func jobCmdEnv(t *testing.T) context.Context {
	t.Helper()
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	return context.Background()
}

// storeForEnv opens the same database the commands just wrote to.
func storeForEnv(t *testing.T) *store.Store {
	t.Helper()
	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestJobAddStoresTheJobAndRefusesToOverwriteIt(t *testing.T) {
	ctx := jobCmdEnv(t)
	argv := []string{"--id", "brief", "--prompt", "write the briefing",
		"--cron", "0 7 * * *", "--cwd", t.TempDir()}
	if err := jobAdd(argv); err != nil {
		t.Fatal(err)
	}

	s := storeForEnv(t)
	j, err := s.Job(ctx, "brief")
	if err != nil {
		t.Fatal(err)
	}
	if j.Prompt != "write the briefing" {
		t.Errorf("prompt = %q, want the one that was passed", j.Prompt)
	}
	if j.Schedule != store.ScheduleCron || j.CronExpr != "0 7 * * *" {
		t.Errorf("schedule = %s/%q, want cron from --cron", j.Schedule, j.CronExpr)
	}
	if !j.Enabled {
		t.Error("a new job should be enabled")
	}

	// The one that cost a live job: the store upserts by id, so a reused id
	// must be refused here or it silently replaces a job's prompt and schedule.
	err = jobAdd([]string{"--id", "brief", "--prompt", "something else",
		"--cwd", t.TempDir()})
	if err == nil {
		t.Fatal("re-adding an existing id must fail, not overwrite")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to say the job already exists", err)
	}
	again, err := s.Job(ctx, "brief")
	if err != nil {
		t.Fatal(err)
	}
	if again.Prompt != "write the briefing" {
		t.Errorf("prompt = %q, the refused add still overwrote it", again.Prompt)
	}
}

func TestJobAddRejectsIncompleteJobs(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"no id", []string{"--prompt", "hi"}, "--id is required"},
		{"no prompt and no flow", []string{"--id", "x"}, "--prompt is required"},
		{"prompt and flow together", []string{"--id", "x", "--prompt", "hi", "--flow", "f"},
			"not both"},
		{"cron schedule without an expression",
			[]string{"--id", "x", "--prompt", "hi", "--schedule", "cron"}, "--cron"},
		{"interval schedule without an interval",
			[]string{"--id", "x", "--prompt", "hi", "--schedule", "interval"}, "--interval"},
		{"once schedule without a time",
			[]string{"--id", "x", "--prompt", "hi", "--schedule", "once"}, "--at"},
		{"unknown catchup policy",
			[]string{"--id", "x", "--prompt", "hi", "--catchup", "sometimes"}, "--catchup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobCmdEnv(t)
			err := jobAdd(tc.argv)
			if err == nil {
				t.Fatal("expected an error, the job was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestJobAddRefusesAReusedName(t *testing.T) {
	jobCmdEnv(t)
	dir := t.TempDir()
	if err := jobAdd([]string{"--id", "one", "--name", "Briefing",
		"--prompt", "a", "--cwd", dir}); err != nil {
		t.Fatal(err)
	}
	err := jobAdd([]string{"--id", "two", "--name", "Briefing",
		"--prompt", "b", "--cwd", dir})
	if err == nil {
		t.Fatal("two jobs must not share a name: the board and @mentions address jobs by it")
	}
	if !strings.Contains(err.Error(), "Briefing") {
		t.Errorf("error = %v, want it to name the clash", err)
	}
}

func TestJobEditChangesOnlyTheFlagsThatWerePassed(t *testing.T) {
	ctx := jobCmdEnv(t)
	dir := t.TempDir()
	if err := jobAdd([]string{"--id", "brief", "--name", "Briefing",
		"--prompt", "old prompt", "--cron", "0 7 * * *", "--tags", "daily",
		"--cwd", dir}); err != nil {
		t.Fatal(err)
	}
	if err := jobEdit([]string{"brief", "--prompt", "new prompt"}); err != nil {
		t.Fatal(err)
	}

	s := storeForEnv(t)
	j, err := s.Job(ctx, "brief")
	if err != nil {
		t.Fatal(err)
	}
	if j.Prompt != "new prompt" {
		t.Errorf("prompt = %q, want the edited value", j.Prompt)
	}
	// Everything not named on the command line must survive the edit -- an
	// edit that reset the schedule to its default would silently unschedule a
	// live job.
	if j.Name != "Briefing" {
		t.Errorf("name = %q, the edit reset it", j.Name)
	}
	if j.CronExpr != "0 7 * * *" || j.Schedule != store.ScheduleCron {
		t.Errorf("schedule = %s/%q, the edit reset it", j.Schedule, j.CronExpr)
	}
	if len(j.Tags) != 1 || j.Tags[0] != "daily" {
		t.Errorf("tags = %v, the edit reset them", j.Tags)
	}
	if j.CWD != dir {
		t.Errorf("cwd = %q, the edit reset it", j.CWD)
	}
}

func TestJobEditRefusesAnUnknownIDAndANameAlreadyTaken(t *testing.T) {
	ctx := jobCmdEnv(t)
	dir := t.TempDir()
	for _, id := range []string{"one", "two"} {
		if err := jobAdd([]string{"--id", id, "--name", id,
			"--prompt", "p", "--cwd", dir}); err != nil {
			t.Fatal(err)
		}
	}
	if err := jobEdit([]string{"nope", "--prompt", "p"}); err == nil {
		t.Error("editing a job that does not exist must fail")
	}
	if err := jobEdit([]string{"two", "--name", "one"}); err == nil {
		t.Error("renaming onto another job's name must fail")
	}
	s := storeForEnv(t)
	j, err := s.Job(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	if j.Name != "two" {
		t.Errorf("name = %q, the refused rename was written anyway", j.Name)
	}
}

func TestJobPauseAndResumeFlipOnlyTheEnabledFlag(t *testing.T) {
	ctx := jobCmdEnv(t)
	if err := jobAdd([]string{"--id", "brief", "--prompt", "p",
		"--cron", "0 7 * * *", "--cwd", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	s := storeForEnv(t)

	if err := jobEnable([]string{"brief"}, false); err != nil {
		t.Fatal(err)
	}
	j, err := s.Job(ctx, "brief")
	if err != nil {
		t.Fatal(err)
	}
	if j.Enabled {
		t.Error("pause left the job enabled")
	}
	if j.CronExpr != "0 7 * * *" {
		t.Errorf("cron = %q, pause disturbed the schedule", j.CronExpr)
	}

	if err := jobEnable([]string{"brief"}, true); err != nil {
		t.Fatal(err)
	}
	if j, err = s.Job(ctx, "brief"); err != nil {
		t.Fatal(err)
	}
	if !j.Enabled {
		t.Error("resume left the job paused")
	}
}

func TestJobEnableAndRemoveNeedAnID(t *testing.T) {
	jobCmdEnv(t)
	if err := jobEnable(nil, false); err == nil {
		t.Error("pause with no id must fail rather than touch every job")
	}
	if err := jobRemove(nil); err == nil {
		t.Error("remove with no id must fail rather than delete anything")
	}
}

func TestJobRemoveDeletesTheJobAndKeepsItsRuns(t *testing.T) {
	ctx := jobCmdEnv(t)
	if err := jobAdd([]string{"--id", "brief", "--prompt", "p",
		"--cwd", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	s := storeForEnv(t)
	if err := s.PutRun(ctx, store.Run{ID: "r1", JobID: "brief", Outcome: "done",
		StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := jobRemove([]string{"brief"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Job(ctx, "brief"); err == nil {
		t.Error("the job is still there after remove")
	}
	// Deleting the schedule must not delete the history: the run record is
	// what says what the job did, and nothing else keeps a copy.
	runs, err := s.JobRuns(ctx, "brief", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Errorf("runs = %d, want the run to outlive the job", len(runs))
	}
}
