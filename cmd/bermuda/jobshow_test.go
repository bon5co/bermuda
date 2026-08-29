package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The read-only half of the job verbs: show, list, prune.
//
// None of them changes anything, which is why a wrong answer here is expensive
// rather than obvious. `job show` is what somebody reads before deciding a job
// is configured correctly, so a field it silently omits is a field nobody
// checks; the agent argv line is the only place the flags a run will actually
// carry are visible before it runs. `job list` and `job prune` already have
// their inner halves tested against a real database in prune_test.go -- what is
// tested here is the argv layer above them, where a flag that is parsed but
// never passed down turns --tag into "show everything" and --yes into a
// deletion nobody confirmed.

// showJob stores a job and returns what `job show` printed for it.
func showJob(t *testing.T, j store.Job) string {
	t.Helper()
	s := storeForEnv(t)
	if err := s.PutJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return jobShow([]string{j.ID}) })
	if err != nil {
		t.Fatalf("showing job %s failed: %v", j.ID, err)
	}
	return out
}

// Everything that decides what a run will do has to appear. A field that is
// stored but not shown is a field that cannot be checked without opening the
// database, and the agent flags in particular are why `job show` exists: a job
// carrying --dangerously-skip-permissions looks exactly like one that does not.
func TestJobShowPrintsWhatTheRunWillCarry(t *testing.T) {
	jobCmdEnv(t)
	dir := t.TempDir()
	out := showJob(t, store.Job{
		ID:              "brief",
		Name:            "Briefing",
		Description:     "the morning read",
		Prompt:          "write the briefing",
		Tags:            []string{"daily", "report"},
		CWD:             dir,
		Model:           "opus",
		AllowedTools:    "Read,Write",
		SkipPermissions: true,
		Schedule:        store.ScheduleCron,
		CronExpr:        "0 7 * * *",
		Catchup:         store.CatchupSkip,
		Enabled:         true,
	})

	for _, want := range []string{
		"brief", "Briefing", "the morning read",
		"daily, report", // tags are joined, not printed as a Go slice
		"0 7 * * *",     // the schedule label, not the enum
		store.CatchupSkip,
		dir,
		"opus", "Read,Write",
		"write the briefing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("job show omitted %q:\n%s", want, out)
		}
	}
	// The argv line is the assembled command, so the bypass has to reach it --
	// this is the one field where a display-only omission hides a real risk.
	if !strings.Contains(out, "--dangerously-skip-permissions") {
		t.Errorf("the agent argv does not show the permission bypass:\n%s", out)
	}
	// A job nobody has run yet must say so rather than print an empty table,
	// which reads as "the runs could not be loaded".
	if !strings.Contains(out, "no runs yet") {
		t.Errorf("a never-run job should say so:\n%s", out)
	}
}

// The recent-run table is the only place a park reason is visible next to the
// job that produced it. A parked run that shows up as merely "parked" sends the
// reader to the run log to find out what it is waiting for.
func TestJobShowListsRecentRunsWithTheirParkReason(t *testing.T) {
	jobCmdEnv(t)
	s := storeForEnv(t)
	ctx := context.Background()
	if err := s.PutJob(ctx, store.Job{ID: "brief", Prompt: "p", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	runs := []store.Run{
		{ID: "r1", JobID: "brief", Trigger: "schedule", Outcome: "done", StartedAt: at},
		{ID: "r2", JobID: "brief", Trigger: "manual", Outcome: "parked",
			ParkReason: "needs a human", Note: "waiting on Handler", StartedAt: at},
	}
	for _, r := range runs {
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error { return jobShow([]string{"brief"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"r1", "r2", "parked", "needs a human", "waiting on Handler"} {
		if !strings.Contains(out, want) {
			t.Errorf("the recent runs omitted %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no runs yet") {
		t.Errorf("a job with runs claims to have none:\n%s", out)
	}
}

// A flow job's steps live in a file, not in the job, so what `job show` prints
// has to be read from that file: printing the job's stored prompt for a flow
// job would describe a run that cannot happen.
func TestJobShowReadsAFlowJobsStepsFromTheFlowFile(t *testing.T) {
	jobCmdEnv(t)
	writeFlow(t, "triage", "input: which inbox\nsteps:\n"+
		"  - id: gather\n    agent: collect what came in\n"+
		"  - id: sort\n    run: true\n")
	out := showJob(t, store.Job{
		ID: "morning", Flow: "triage", Input: "yesterday",
		CWD: t.TempDir(), Enabled: true,
	})

	for _, want := range []string{"triage", "gather", "sort", "which inbox"} {
		if !strings.Contains(out, want) {
			t.Errorf("the flow listing omitted %q:\n%s", want, out)
		}
	}
}

// A job that names a flow which is missing or unparseable is still a valid
// job: it looks fine and will fail at its next fire. `job show` is where that
// is meant to become visible, so it has to name the flow and the problem, and
// it must not report the job itself as unreadable.
func TestJobShowReportsABrokenFlowWithoutFailing(t *testing.T) {
	jobCmdEnv(t)
	out := showJob(t, store.Job{
		ID: "morning", Flow: "gone", CWD: t.TempDir(), Enabled: true,
	})
	if !strings.Contains(out, "gone") {
		t.Errorf("the missing flow is not named:\n%s", out)
	}
	if !strings.Contains(out, "morning") {
		t.Errorf("the job's own configuration was not printed:\n%s", out)
	}
}

func TestJobShowNeedsAnIDThatExists(t *testing.T) {
	jobCmdEnv(t)
	if err := jobShow(nil); err == nil {
		t.Error("job show with no id must fail rather than pick a job")
	}
	if _, err := captureStdout(t, func() error { return jobShow([]string{"nope"}) }); err == nil {
		t.Error("job show on an unknown id must fail, not print an empty job")
	}
}

// --tag has to reach the filter. A parsed-but-ignored flag is invisible on a
// machine with few jobs and wrong on the one this runs on.
func TestJobListPassesItsTagAndAllFlagsDown(t *testing.T) {
	jobCmdEnv(t)
	s := storeForEnv(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	jobs := []store.Job{
		{ID: "brief", Tags: []string{"daily"}, Schedule: store.ScheduleCron, CronExpr: "0 7 * * *"},
		{ID: "sweep", Tags: []string{"weekly"}, Schedule: store.ScheduleCron, CronExpr: "0 7 * * 0"},
		{ID: "once-done", Schedule: store.ScheduleOnce, RunAt: &at, Tags: []string{"daily"}},
	}
	for _, j := range jobs {
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutRun(ctx, store.Run{ID: "r1", JobID: "once-done",
		Outcome: "done", StartedAt: at}); err != nil {
		t.Fatal(err)
	}

	tagged, err := captureStdout(t, func() error { return jobList([]string{"--tag", "daily"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tagged, "sweep") {
		t.Errorf("--tag daily listed a weekly job:\n%s", tagged)
	}
	if !strings.Contains(tagged, "brief") {
		t.Errorf("--tag daily hid a job that carries the tag:\n%s", tagged)
	}
	// The finished one-shot carries the tag too, so its absence here is the
	// hiding rule and not the filter.
	if strings.Contains(tagged, "once-done") {
		t.Errorf("a finished one-shot was listed without --all:\n%s", tagged)
	}

	all, err := captureStdout(t, func() error { return jobList([]string{"--all"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, "once-done") {
		t.Errorf("--all did not reach the listing:\n%s", all)
	}
}

// Prune is the only command that destroys configuration, and the dry run is the
// default. The argv layer is where that could be lost: a --yes that defaulted
// to true, or a missing one that was passed as true anyway, deletes jobs on a
// command somebody typed to find out what would happen.
func TestJobPruneDefaultsToADryRunAndDeletesOnlyWithYes(t *testing.T) {
	jobCmdEnv(t)
	s := storeForEnv(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	if err := s.PutJob(ctx, store.Job{ID: "once-done",
		Schedule: store.ScheduleOnce, RunAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(ctx, store.Run{ID: "r1", JobID: "once-done",
		Outcome: "done", StartedAt: at}); err != nil {
		t.Fatal(err)
	}

	dry, err := captureStdout(t, func() error { return jobPrune(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dry, "nothing removed") {
		t.Errorf("the dry run does not say it removed nothing:\n%s", dry)
	}
	if _, err := s.Job(ctx, "once-done"); err != nil {
		t.Fatalf("the default prune deleted the job: %v", err)
	}

	if _, err := captureStdout(t, func() error { return jobPrune([]string{"--yes"}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Job(ctx, "once-done"); err == nil {
		t.Error("--yes did not reach the deletion")
	}
	// Pruning loses the schedule, not the history.
	runs, err := s.JobRuns(ctx, "once-done", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Errorf("runs = %d, want the run to outlive the pruned job", len(runs))
	}
}
