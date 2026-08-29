package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/runner"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// persist is the only thing that turns a finished run into the durable record
// everything else reads: the board's outcome column, `bermuda run show`, the
// usage report, and the scheduler's idea of when a job last fired. A field it
// drops or crosses over is not a crash — it is a row that reads plausibly and
// says the wrong thing forever, which is exactly the failure nothing notices.

// transcriptHome writes a Claude Code session transcript for one run under a
// fake home, so persist's usage collection finds counts and settles at once
// rather than waiting out its ten-second grace.
func transcriptHome(t *testing.T, cwd, runDir string, u store.Run) string {
	t.Helper()
	home := t.TempDir()

	var b strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	dir := filepath.Join(home, ".claude", "projects", b.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The prompt line naming this run's directory is the anchor usage keys off;
	// the assistant line after it is what gets counted.
	prompt := fmt.Sprintf(`{"type":"user","message":{"content":"read %s/prompt.md"}}`, runDir)
	assistant, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"id":    "msg_1",
			"model": u.Model,
			"usage": map[string]any{
				"input_tokens":                u.InputTokens,
				"output_tokens":               u.OutputTokens,
				"cache_read_input_tokens":     u.CacheReadTokens,
				"cache_creation_input_tokens": u.CacheCreationTokens,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := prompt + "\n" + string(assistant) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// Every field a caller reads back has to survive the trip. The pairs that would
// go unnoticed if they crossed are the ones that look alike: outcome and status
// are both short lowercase words, and tab id and agent name are both opaque
// handles.
func TestPersistRecordsEveryFieldOfARun(t *testing.T) {
	s := newStore(t)
	cwd := t.TempDir()
	started := time.Now().Add(-90 * time.Second).Truncate(time.Second)
	ended := started.Add(75 * time.Second)

	want := store.Run{
		InputTokens: 11, OutputTokens: 22,
		CacheReadTokens: 33, CacheCreationTokens: 44,
		Model: "claude-opus-5",
	}
	t.Setenv("HOME", transcriptHome(t, cwd, "/state/runs/20260807T000000Z-nightly", want))

	run := &runner.Run{
		JobID:      "nightly",
		RunID:      "20260807T000000Z-nightly",
		RunDir:     "/state/runs/20260807T000000Z-nightly",
		Outcome:    runner.OutcomeParked,
		ParkReason: runner.ParkTimeout,
		Status:     herdrcli.StatusBlocked,
		Result:     &runner.Result{Status: "error", Note: "ran out of time"},
		AgentName:  "bermuda-nightly",
		TabID:      "w5K:tS",
		StartedAt:  started,
		EndedAt:    ended,
	}
	if err := persist(context.Background(), s, run, "scheduled", cwd); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got, err := s.Run(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, c := range []struct{ field, got, want string }{
		{"id", got.ID, run.RunID},
		{"job", got.JobID, run.JobID},
		{"trigger", got.Trigger, "scheduled"},
		{"outcome", got.Outcome, string(run.Outcome)},
		{"park reason", got.ParkReason, string(run.ParkReason)},
		{"status", got.Status, string(run.Status)},
		{"note", got.Note, run.Result.Note},
		{"run dir", got.RunDir, run.RunDir},
		{"tab", got.TabID, run.TabID},
		{"agent", got.AgentName, run.AgentName},
		{"model", got.Model, want.Model},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("started at %s, want %s", got.StartedAt, started)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Errorf("ended at %v, want %s", got.EndedAt, ended)
	}
	if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens ||
		got.CacheReadTokens != want.CacheReadTokens || got.CacheCreationTokens != want.CacheCreationTokens {
		t.Errorf("tokens %d/%d/%d/%d, want %d/%d/%d/%d",
			got.InputTokens, got.OutputTokens, got.CacheReadTokens, got.CacheCreationTokens,
			want.InputTokens, want.OutputTokens, want.CacheReadTokens, want.CacheCreationTokens)
	}
}

// The four counts are billed at different rates, so they must land in their own
// columns. A test that gives them all the same number cannot tell a swap from a
// correct write, which is why the case above uses four distinct values and this
// one states the rule directly.
func TestPersistKeepsTheFourTokenCountsApart(t *testing.T) {
	s := newStore(t)
	cwd := t.TempDir()
	runDir := "/state/runs/r-counts"
	want := store.Run{
		InputTokens: 1, OutputTokens: 2, CacheReadTokens: 4, CacheCreationTokens: 8,
		Model: "claude-sonnet-5",
	}
	t.Setenv("HOME", transcriptHome(t, cwd, runDir, want))

	run := &runner.Run{
		JobID: "j", RunID: "r-counts", RunDir: runDir,
		Outcome: runner.OutcomeDone, StartedAt: time.Now(), EndedAt: time.Now(),
	}
	if err := persist(context.Background(), s, run, "manual", cwd); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got, err := s.Run(context.Background(), "r-counts")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want int64
	}{
		{"input", got.InputTokens, want.InputTokens},
		{"output", got.OutputTokens, want.OutputTokens},
		{"cache read", got.CacheReadTokens, want.CacheReadTokens},
		{"cache creation", got.CacheCreationTokens, want.CacheCreationTokens},
	} {
		if c.got != c.want {
			t.Errorf("%s tokens = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// A run that reported no result has no note, and nothing may be invented for
// it: the board prints the note as the run's own words.
func TestPersistLeavesTheNoteEmptyWithoutAResult(t *testing.T) {
	s := newStore(t)
	cwd := t.TempDir()
	runDir := "/state/runs/r-nonote"
	t.Setenv("HOME", transcriptHome(t, cwd, runDir, store.Run{InputTokens: 1}))

	run := &runner.Run{
		JobID: "j", RunID: "r-nonote", RunDir: runDir,
		Outcome: runner.OutcomeParked, ParkReason: runner.ParkNoResult,
		StartedAt: time.Now(), EndedAt: time.Now(),
	}
	if err := persist(context.Background(), s, run, "manual", cwd); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got, err := s.Run(context.Background(), "r-nonote")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Note != "" {
		t.Errorf("note = %q, want empty", got.Note)
	}
	if got.ParkReason != string(runner.ParkNoResult) {
		t.Errorf("park reason = %q, want %q", got.ParkReason, runner.ParkNoResult)
	}
}

// A run with no end time is one still going. Recording an ended_at for it would
// make it look finished to everything that reads the row, and give it a
// duration measured from the zero time.
func TestPersistLeavesEndedAtNullForAnUnfinishedRun(t *testing.T) {
	s := newStore(t)
	cwd := t.TempDir()
	runDir := "/state/runs/r-open"
	t.Setenv("HOME", transcriptHome(t, cwd, runDir, store.Run{OutputTokens: 5}))

	run := &runner.Run{
		JobID: "j", RunID: "r-open", RunDir: runDir,
		Outcome: runner.OutcomeRunning, StartedAt: time.Now(),
	}
	if err := persist(context.Background(), s, run, "manual", cwd); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got, err := s.Run(context.Background(), "r-open")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.EndedAt != nil {
		t.Errorf("ended at = %v, want unset", got.EndedAt)
	}
}

// Usage is bookkeeping. A transcript that cannot be read leaves the counts at
// zero and must not cost the run its outcome — the alternative is a finished
// run that stays "running" on the board because its token file was unreadable.
func TestPersistRecordsTheRunEvenWhenUsageCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file, so there is no read failure to provoke")
	}
	s := newStore(t)
	cwd := t.TempDir()
	home := transcriptHome(t, cwd, "/state/runs/r-blind", store.Run{InputTokens: 7})
	t.Setenv("HOME", home)

	// Make the session unreadable, which is the failure that returns an error
	// rather than an empty count — an absent transcript is a legitimate gap and
	// is waited out instead.
	var sessions []string
	err := filepath.Walk(home, func(p string, _ os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(p, ".jsonl") {
			sessions = append(sessions, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("found %d session files, want 1", len(sessions))
	}
	if err := os.Chmod(sessions[0], 0o000); err != nil {
		t.Fatal(err)
	}

	run := &runner.Run{
		JobID: "j", RunID: "r-blind", RunDir: "/state/runs/r-blind",
		Outcome: runner.OutcomeDone, Result: &runner.Result{Status: "ok", Note: "done"},
		StartedAt: time.Now(), EndedAt: time.Now(),
	}
	if err := persist(context.Background(), s, run, "manual", cwd); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got, err := s.Run(context.Background(), "r-blind")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Outcome != string(runner.OutcomeDone) {
		t.Errorf("outcome = %q, want done", got.Outcome)
	}
	if got.Note != "done" {
		t.Errorf("note = %q, want %q", got.Note, "done")
	}
	if got.InputTokens|got.OutputTokens|got.CacheReadTokens|got.CacheCreationTokens != 0 {
		t.Errorf("tokens = %d/%d/%d/%d, want all zero",
			got.InputTokens, got.OutputTokens, got.CacheReadTokens, got.CacheCreationTokens)
	}
}

// A finished run replaces the "running" row executePrompt wrote at the start,
// rather than adding a second one: the board and the scheduler both key off the
// run id, and a duplicate would leave the job looking permanently in flight.
func TestPersistReplacesTheStartedRowRatherThanAddingOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	runDir := "/state/runs/r-upsert"
	t.Setenv("HOME", transcriptHome(t, cwd, runDir, store.Run{InputTokens: 3}))

	started := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := s.PutRun(ctx, store.Run{
		ID: "r-upsert", JobID: "j", Trigger: "scheduled",
		Outcome: "running", RunDir: runDir, StartedAt: started,
	}); err != nil {
		t.Fatalf("put start row: %v", err)
	}

	run := &runner.Run{
		JobID: "j", RunID: "r-upsert", RunDir: runDir,
		Outcome: runner.OutcomeFailed, Result: &runner.Result{Status: "error", Note: "exit 1"},
		StartedAt: started, EndedAt: time.Now(),
	}
	if err := persist(ctx, s, run, "scheduled", cwd); err != nil {
		t.Fatalf("persist: %v", err)
	}

	runs, err := s.Runs(ctx, "", 100)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("stored %d rows for one run, want 1", len(runs))
	}
	if runs[0].Outcome != string(runner.OutcomeFailed) {
		t.Errorf("outcome = %q, want failed", runs[0].Outcome)
	}
	// The start row owns the start time: overwriting it would report a duration
	// measured from whenever the run happened to finish being written.
	if !runs[0].StartedAt.Equal(started) {
		t.Errorf("started at %s, want the start row's %s", runs[0].StartedAt, started)
	}
}
