package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLine(t *testing.T) {
	got := Line(Usage{
		InputTokens:         5,
		OutputTokens:        1200,
		CacheReadTokens:     34000,
		CacheCreationTokens: 1_500_000,
	})
	want := "5 in · 1.2k out · 34k cache read · 1.5M cache write"
	if got != want {
		t.Errorf("Line = %q, want %q", got, want)
	}
}

// A run whose transcript is already on disk must not pay the settle wait: the
// common case is a run that finished long enough ago for Claude Code to have
// flushed, and holding the harness there would delay every run.
func TestCollectSettledReturnsWithoutWaiting(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	home := writeTranscript(t, cwd, "sess",
		userLine("run-a")+assistantLine("m1", "claude-opus-5", 2, 85, 20, 13))

	start := time.Now()
	u, err := CollectSettled(home, cwd, "/home/x/.bermuda/runs/run-a", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if u.OutputTokens != 85 {
		t.Fatalf("wrong totals: %+v", u)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waited %s for a transcript already on disk", elapsed)
	}
}

// Claude Code appends to the session file with a lag, so the counts for a
// quick run are often still missing at the moment bermuda persists it. The
// whole point of CollectSettled is to pick those up rather than record a zero.
func TestCollectSettledWaitsForALateTranscript(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	home := writeTranscript(t, cwd, "sess", userLine("run-a"))
	path := filepath.Join(transcriptDir(home, cwd), "sess.jsonl")

	go func() {
		time.Sleep(300 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString(assistantLine("m1", "claude-opus-5", 2, 85, 20, 13))
	}()

	u, err := CollectSettled(home, cwd, "/home/x/.bermuda/runs/run-a", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if u.OutputTokens != 85 || u.InputTokens != 2 {
		t.Fatalf("late transcript not picked up: %+v", u)
	}
}

// A missing count is a bookkeeping gap, not a reason to hold up the harness:
// the wait has to end, and it has to end with a zero Usage and no error.
func TestCollectSettledGivesUp(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	home := writeTranscript(t, cwd, "sess", userLine("other-run"))

	start := time.Now()
	u, err := CollectSettled(home, cwd, "/home/x/.bermuda/runs/run-a", 400*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Empty() {
		t.Fatalf("expected an empty Usage, got %+v", u)
	}
	if elapsed < 400*time.Millisecond {
		t.Errorf("gave up after %s, before the deadline", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("still waiting after %s", elapsed)
	}
}

// An unreadable transcript directory is a real error, and it must surface on
// the first attempt rather than being retried until the deadline.
func TestCollectSettledReturnsCollectError(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	home := t.TempDir()
	dir := transcriptDir(home, cwd)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file where the project directory belongs: ReadDir fails with
	// something other than "does not exist".
	if err := os.WriteFile(dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := CollectSettled(home, cwd, "/home/x/.bermuda/runs/run-a", 10*time.Second); err == nil {
		t.Fatal("expected an error for an unreadable transcript directory")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("retried an unrecoverable error for %s", elapsed)
	}
}
