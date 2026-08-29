package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// `bermuda run list`, `bermuda run show` and `bermuda usage` are how a person
// asks what the harness has been doing. The store queries underneath them have
// tests; the wiring between argv and those queries does not, and every mistake
// in it is silent. A --state that is not passed through lists failures among
// the successes, a --limit that is dropped answers a question nobody asked, and
// a --since applied with the wrong sign reports either nothing or everything
// while still looking like a plausible report.

// runCmdEnv points the commands at a throwaway state directory, so they read
// and write a temp database instead of the real ~/.bermuda one.
func runCmdEnv(t *testing.T) context.Context {
	t.Helper()
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	return context.Background()
}

// putRun stores one run, filling in the fields none of these tests care about.
func putRun(t *testing.T, s *store.Store, r store.Run) {
	t.Helper()
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	if err := s.PutRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

// decodeRuns reads what `run list --json` printed. That output is the
// machine-readable contract: the board and other agents parse it rather than
// the tab-separated table.
func decodeRuns(t *testing.T, out string) []store.Run {
	t.Helper()
	var runs []store.Run
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		t.Fatalf("run list --json emitted something that is not a run list: %v\n%s", err, out)
	}
	return runs
}

func TestRunCmdRejectsAMissingOrUnknownSubcommand(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"no subcommand", nil, "usage:"},
		{"unknown subcommand", []string{"delete"}, "unknown run subcommand"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runCmdEnv(t)
			err := runCmd(tc.argv)
			if err == nil {
				t.Fatal("expected an error, the command was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// --state is the flag that answers "what is broken" and "what is waiting for
// me". If it were not passed through to the query, the answer would be the
// whole list, which reads as a working command until somebody trusts it.
func TestRunListFiltersByState(t *testing.T) {
	runCmdEnv(t)
	s := storeForEnv(t)
	start := time.Now().Add(-time.Hour)
	for i, r := range []store.Run{
		{ID: "a", JobID: "brief", Outcome: "done"},
		{ID: "b", JobID: "brief", Outcome: "failed"},
		{ID: "c", JobID: "scrape", Outcome: "parked", ParkReason: "no result"},
		{ID: "d", JobID: "scrape", Outcome: "parked"},
	} {
		r.StartedAt = start.Add(time.Duration(i) * time.Minute)
		putRun(t, s, r)
	}

	out, err := captureStdout(t, func() error {
		return runList([]string{"--state", "parked", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	runs := decodeRuns(t, out)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want the 2 parked ones", len(runs))
	}
	for _, r := range runs {
		if r.Outcome != "parked" {
			t.Errorf("run %s has outcome %q, --state parked let it through", r.ID, r.Outcome)
		}
	}

	// A state nothing is in is an empty answer, not every run.
	out, err = captureStdout(t, func() error {
		return runList([]string{"--state", "running", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs := decodeRuns(t, out); len(runs) != 0 {
		t.Errorf("got %d runs for a state nothing is in, want none", len(runs))
	}
}

// The default limit is the command's, not the store's: the store falls back to
// 50 when it is given nothing, so a --limit that never reached it would still
// return a plausible list -- just not the one that was asked for.
func TestRunListHonoursTheLimitAndItsDefault(t *testing.T) {
	runCmdEnv(t)
	s := storeForEnv(t)
	start := time.Now().Add(-time.Hour)
	for i := 0; i < 25; i++ {
		putRun(t, s, store.Run{
			ID:        string(rune('a'+i/10)) + string(rune('0'+i%10)),
			JobID:     "brief",
			Outcome:   "done",
			StartedAt: start.Add(time.Duration(i) * time.Minute),
		})
	}

	out, err := captureStdout(t, func() error { return runList([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if runs := decodeRuns(t, out); len(runs) != 20 {
		t.Errorf("got %d runs with no --limit, want the documented default of 20", len(runs))
	}

	out, err = captureStdout(t, func() error {
		return runList([]string{"--limit", "3", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	runs := decodeRuns(t, out)
	if len(runs) != 3 {
		t.Fatalf("got %d runs for --limit 3, want 3", len(runs))
	}
	// Newest first: a limit that kept the oldest rows would quietly show
	// history instead of what just happened.
	for i := 1; i < len(runs); i++ {
		if runs[i].StartedAt.After(runs[i-1].StartedAt) {
			t.Errorf("run %s started after %s but is listed later; the list is not newest first",
				runs[i].ID, runs[i-1].ID)
		}
	}
}

// An empty list is a sentence, not a bare header. The table form is what a
// person reads, and "no runs" is the difference between "nothing has run" and
// "the command is broken".
func TestRunListSaysSoWhenThereAreNoRuns(t *testing.T) {
	runCmdEnv(t)
	out, err := captureStdout(t, func() error { return runList(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no runs") {
		t.Errorf("output = %q, want it to say there are no runs", out)
	}
}

// run show is the one command that says where a run's artifacts are. The paths
// it prints are the only pointer to the prompt, the transcript and the result:
// a run directory dropped or mistyped here leaves a person with nothing to
// open.
func TestRunShowPrintsTheRunAndWhereItsArtifactsAre(t *testing.T) {
	runCmdEnv(t)
	s := storeForEnv(t)
	ended := time.Now()
	started := ended.Add(-90 * time.Second)
	putRun(t, s, store.Run{
		ID:         "20260812T090000Z-brief",
		JobID:      "brief",
		Trigger:    "scheduled",
		Outcome:    "parked",
		ParkReason: "no result",
		Status:     "waiting",
		Note:       "asked a question",
		RunDir:     "/tmp/runs/20260812T090000Z-brief",
		TabID:      "w1:t2",
		AgentName:  "brief-1",
		StartedAt:  started,
		EndedAt:    &ended,
	})

	out, err := captureStdout(t, func() error {
		return runShow([]string{"20260812T090000Z-brief"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"20260812T090000Z-brief",
		"brief",
		"scheduled",
		"parked",
		"no result",
		"brief-1",
		"w1:t2",
		"asked a question",
		"/tmp/runs/20260812T090000Z-brief/prompt.md",
		"/tmp/runs/20260812T090000Z-brief/transcript.txt",
		"/tmp/runs/20260812T090000Z-brief/result.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("run show output is missing %q:\n%s", want, out)
		}
	}
	// A parked run without its reason is a run nobody can act on, so the
	// reason has a line of its own rather than living in the note.
	if !strings.Contains(out, "park reason") {
		t.Errorf("run show did not label the park reason:\n%s", out)
	}
}

// A flow run's steps are recorded separately and shown by another command.
// Saying how many there are is what tells a reader that the per-run view is
// not the whole story.
func TestRunShowPointsAtTheStepsOfAFlowRun(t *testing.T) {
	ctx := runCmdEnv(t)
	s := storeForEnv(t)
	putRun(t, s, store.Run{ID: "flowrun", JobID: "nightly", Outcome: "done", Flow: "nightly"})
	for i, id := range []string{"gather", "write"} {
		if err := s.PutRunStep(ctx, store.RunStep{
			RunID: "flowrun", Index: i, StepID: id, Kind: "agent", Outcome: "done",
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error { return runShow([]string{"flowrun"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "steps") || !strings.Contains(out, "2") {
		t.Errorf("run show did not report the 2 steps:\n%s", out)
	}
	if !strings.Contains(out, "flow status flowrun") {
		t.Errorf("run show did not name the command that shows the steps:\n%s", out)
	}
}

func TestRunShowNeedsARunThatExists(t *testing.T) {
	runCmdEnv(t)
	if err := runShow(nil); err == nil {
		t.Error("run show with no id must fail rather than pick a run")
	}
	err := runShow([]string{"nosuchrun"})
	if err == nil {
		t.Fatal("run show on an unknown id must fail")
	}
	if !strings.Contains(err.Error(), "nosuchrun") {
		t.Errorf("error = %v, want it to name the run that was not found", err)
	}
}

// --since is a window backwards from now. Applied with the wrong sign it would
// either total nothing or total everything, and both look like a working
// report -- the numbers would just be wrong forever.
func TestUsageTotalsOnlyRunsInsideTheSinceWindow(t *testing.T) {
	runCmdEnv(t)
	s := storeForEnv(t)
	now := time.Now()
	putRun(t, s, store.Run{
		ID: "recent", JobID: "brief", Outcome: "done",
		StartedAt: now.Add(-time.Hour), InputTokens: 100, OutputTokens: 10,
	})
	// Comfortably outside the 24h window and comfortably inside the 720h one.
	// Sitting it exactly 720h back put it on the boundary of the second query,
	// where whichever side the cutoff rounded to — plus however long the test
	// took to get there — decided the result.
	putRun(t, s, store.Run{
		ID: "ancient", JobID: "brief", Outcome: "done",
		StartedAt: now.Add(-20 * 24 * time.Hour), InputTokens: 999, OutputTokens: 999,
	})

	out, err := captureStdout(t, func() error {
		return usageCmd([]string{"--since", "24h", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.JobUsage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("usage --json emitted something that is not a usage report: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d jobs, want the one that ran inside the window", len(rows))
	}
	if rows[0].Runs != 1 || rows[0].InputTokens != 100 || rows[0].OutputTokens != 10 {
		t.Errorf("totals = %d runs / %d in / %d out, want 1/100/10: the run outside --since was counted",
			rows[0].Runs, rows[0].InputTokens, rows[0].OutputTokens)
	}

	// Widen the window and the older run joins the total.
	out, err = captureStdout(t, func() error {
		return usageCmd([]string{"--since", "720h", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Runs != 2 || rows[0].InputTokens != 1099 {
		t.Errorf("totals over a month = %+v, want both runs summed", rows)
	}
}

func TestUsageSaysSoWhenTheWindowIsEmpty(t *testing.T) {
	runCmdEnv(t)
	out, err := captureStdout(t, func() error { return usageCmd([]string{"--since", "1h"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no runs") {
		t.Errorf("output = %q, want it to say the window holds no runs", out)
	}
}
