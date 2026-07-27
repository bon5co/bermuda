package usage

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTranscript lays out a fake ~/.claude/projects tree and returns the home
// it is rooted at.
func writeTranscript(t *testing.T, cwd, session, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := transcriptDir(home, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func userLine(runID string) string {
	return `{"type":"user","timestamp":"2026-07-25T06:13:05.532Z","message":{"role":"user","content":"Read the file /home/x/.bermuda/runs/` + runID + `/prompt.md and do exactly what it says."}}` + "\n"
}

func assistantLine(id, model string, in, out, read, create int) string {
	return `{"type":"assistant","timestamp":"2026-07-25T06:13:06Z","message":{"id":"` + id + `","model":"` + model +
		`","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) +
		`,"cache_read_input_tokens":` + itoa(read) + `,"cache_creation_input_tokens":` + itoa(create) + `}}}` + "\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCollectSumsOneRun(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	body := userLine("run-a") +
		assistantLine("m1", "claude-opus-5", 2, 85, 20628, 13877) +
		assistantLine("m2", "claude-opus-5", 3, 15, 100, 0)
	home := writeTranscript(t, cwd, "sess", body)

	u, err := Collect(home, cwd, "/home/x/.bermuda/runs/run-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 5 || u.OutputTokens != 100 ||
		u.CacheReadTokens != 20728 || u.CacheCreationTokens != 13877 {
		t.Fatalf("wrong totals: %+v", u)
	}
	if u.Model != "claude-opus-5" {
		t.Fatalf("model = %q", u.Model)
	}
}

// A persistent job reuses one session, so a run must stop at the next run's
// prompt instead of absorbing it.
func TestCollectStopsAtNextRun(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	body := userLine("run-a") +
		assistantLine("m1", "sonnet", 1, 10, 0, 0) +
		userLine("run-b") +
		assistantLine("m2", "sonnet", 999, 999, 999, 999)
	home := writeTranscript(t, cwd, "sess", body)

	u, err := Collect(home, cwd, "/home/x/.bermuda/runs/run-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 1 || u.OutputTokens != 10 {
		t.Fatalf("absorbed the next run: %+v", u)
	}
}

// Messages before the run's own prompt belong to whatever came before it.
func TestCollectIgnoresEarlierWork(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	body := assistantLine("m0", "sonnet", 500, 500, 0, 0) +
		userLine("run-a") +
		assistantLine("m1", "sonnet", 7, 8, 0, 0)
	home := writeTranscript(t, cwd, "sess", body)

	u, err := Collect(home, cwd, "/home/x/.bermuda/runs/run-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 7 || u.OutputTokens != 8 {
		t.Fatalf("picked up earlier work: %+v", u)
	}
}

// Two agents in the same directory each get their own numbers.
func TestCollectPicksTheRightSession(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	home := t.TempDir()
	dir := transcriptDir(home, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jsonl", userLine("run-a")+assistantLine("m1", "sonnet", 11, 0, 0, 0))
	write("b.jsonl", userLine("run-b")+assistantLine("m2", "sonnet", 22, 0, 0, 0))

	for runID, want := range map[string]int64{"run-a": 11, "run-b": 22} {
		u, err := Collect(home, cwd, "/home/x/.bermuda/runs/"+runID)
		if err != nil {
			t.Fatal(err)
		}
		if u.InputTokens != want {
			t.Fatalf("%s: input = %d, want %d", runID, u.InputTokens, want)
		}
	}
}

// A repeated message id is one message reported twice, not two messages.
func TestCollectDedupesByMessageID(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	body := userLine("run-a") +
		assistantLine("m1", "sonnet", 10, 5, 0, 0) +
		assistantLine("m1", "sonnet", 10, 5, 0, 0)
	home := writeTranscript(t, cwd, "sess", body)

	u, err := Collect(home, cwd, "/home/x/.bermuda/runs/run-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 10 || u.OutputTokens != 5 {
		t.Fatalf("counted a repeat twice: %+v", u)
	}
}

// A run whose transcript is missing records zero rather than someone else's
// usage, and does not fail.
func TestCollectMissingTranscript(t *testing.T) {
	home := t.TempDir()
	u, err := Collect(home, "/home/x/Projects/nowhere", "/home/x/.bermuda/runs/run-a")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Empty() {
		t.Fatalf("expected zero usage, got %+v", u)
	}
}

// A session that never mentions the run contributes nothing.
func TestCollectUnmentionedRun(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	home := writeTranscript(t, cwd, "sess",
		userLine("run-z")+assistantLine("m1", "sonnet", 99, 99, 0, 0))
	u, err := Collect(home, cwd, "/home/x/.bermuda/runs/run-a")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Empty() {
		t.Fatalf("attributed another run's usage: %+v", u)
	}
}

// A malformed line costs its own message, not the whole run.
func TestCollectSkipsBadLines(t *testing.T) {
	cwd := "/home/x/Projects/bermuda"
	body := userLine("run-a") + "{not json\n" + assistantLine("m1", "sonnet", 4, 4, 0, 0)
	home := writeTranscript(t, cwd, "sess", body)
	u, err := Collect(home, cwd, "/home/x/.bermuda/runs/run-a")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 4 {
		t.Fatalf("bad line broke the sum: %+v", u)
	}
}

func TestFormatCount(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1200: "1.2k", 8400: "8.4k",
		34000: "34k", 1_500_000: "1.5M"}
	for in, want := range cases {
		if got := FormatCount(in); got != want {
			t.Errorf("FormatCount(%d) = %q, want %q", in, got, want)
		}
	}
}
