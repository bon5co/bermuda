package main

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// shellQuote's only job is that `flow edit` opens the file it was asked to open
// and runs nothing else. It is handed a path built from a flow id and the state
// directory, both of which can hold anything a filesystem allows, and its result
// is concatenated into an `sh -c` string — so the test runs the real shell
// rather than asserting on the quoting.
//
// Asserting the returned string would only restate the implementation. What a
// caller depends on is what sh does with it: one argument, byte for byte the
// path, and no substitution.
func TestShellQuoteSurvivesTheRealShell(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"plain", "/home/rafael/.bermuda/flows/nightly.yaml"},
		{"spaces", "/home/rafael/My Flows/nightly run.yaml"},
		{"single quote", "/home/rafael/rafael's flows/nightly.yaml"},
		{"double quote", `/home/rafael/"quoted"/nightly.yaml`},
		{"command substitution", "/home/rafael/$(touch pwned)/nightly.yaml"},
		{"backticks", "/home/rafael/`touch pwned`/nightly.yaml"},
		{"semicolon", "/home/rafael/x; touch pwned; :/nightly.yaml"},
		{"variable", "/home/rafael/$HOME/nightly.yaml"},
		{"glob", "/home/rafael/*/nightly.yaml"},
		{"newline", "/home/rafael/two\nlines/nightly.yaml"},
		{"backslash", `/home/rafael/back\slash/nightly.yaml`},
		{"quote then escape", `/home/rafael/'\''/nightly.yaml`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The trailing | is what catches word splitting: printf repeats its
			// format once per argument, so a path that arrived as two words comes
			// back with two separators.
			out, err := exec.Command("sh", "-c", "printf '%s|' "+shellQuote(tc.path)).Output()
			if err != nil {
				t.Fatalf("sh rejected the quoted path: %v", err)
			}
			if got := string(out); got != tc.path+"|" {
				t.Errorf("sh saw %q, want %q as a single unexpanded argument", got, tc.path+"|")
			}
		})
	}
}

// The editor is chosen by the first variable that names one, and BERMUDA_EDITOR
// comes first so a flow can be opened with something other than whatever the
// surrounding shell exports.
func TestFirstNonEmptyEnvTakesTheFirstThatIsSet(t *testing.T) {
	t.Setenv("BERMUDA_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	if got := firstNonEmptyEnv("BERMUDA_EDITOR", "VISUAL", "EDITOR"); got != "" {
		t.Errorf("got %q with nothing set, want empty so the path is printed instead", got)
	}

	t.Setenv("EDITOR", "vi")
	if got := firstNonEmptyEnv("BERMUDA_EDITOR", "VISUAL", "EDITOR"); got != "vi" {
		t.Errorf("got %q, want the only one set", got)
	}

	t.Setenv("VISUAL", "code -w")
	if got := firstNonEmptyEnv("BERMUDA_EDITOR", "VISUAL", "EDITOR"); got != "code -w" {
		t.Errorf("got %q, want VISUAL to beat EDITOR", got)
	}

	t.Setenv("BERMUDA_EDITOR", "hx")
	if got := firstNonEmptyEnv("BERMUDA_EDITOR", "VISUAL", "EDITOR"); got != "hx" {
		t.Errorf("got %q, want BERMUDA_EDITOR to win", got)
	}
}

// A variable set to whitespace counts as unset, and the value that comes back
// is trimmed.
//
// Both matter at the same call site: `EDITOR=" "` exported by a login script
// would otherwise run `sh -c "   '/path'"`, and a trailing space in a real
// editor name is invisible in the error when it fails.
func TestFirstNonEmptyEnvIgnoresWhitespaceAndTrims(t *testing.T) {
	t.Setenv("BERMUDA_EDITOR", "   ")
	t.Setenv("VISUAL", "\t\n")
	t.Setenv("EDITOR", "  vi  ")

	if got := firstNonEmptyEnv("BERMUDA_EDITOR", "VISUAL", "EDITOR"); got != "vi" {
		t.Errorf("got %q, want blank variables skipped and the value trimmed", got)
	}
}

// The SPACE column in `thread list` distinguishes four states, and the two that
// are easy to conflate are the ones that matter: a thread with no workspace is
// where @all reaches nobody, and a closed one is where it reaches nobody any
// more. Neither may render as a blank, which reads as missing data.
func TestSpaceColumnNamesEveryState(t *testing.T) {
	closed := time.Date(2026, 7, 30, 21, 0, 0, 0, time.UTC)
	labels := map[string]string{"w7": "Better Lingo"}

	for _, tc := range []struct {
		name   string
		thread store.Thread
		want   string
	}{
		{"no workspace", store.Thread{ID: "global"}, "-"},
		{"labelled", store.Thread{ID: "bl", WorkspaceID: "w7"}, "Better Lingo"},
		{"unlabelled falls back to the id", store.Thread{ID: "x", WorkspaceID: "w9"}, "w9"},
		{"closed", store.Thread{ID: "bl", WorkspaceID: "w7", ClosedAt: &closed}, "(gone)"},
		// Closed beats the label: the space is gone, and printing its old name
		// would say the thread is still reachable.
		{"closed and unlabelled", store.Thread{ID: "x", WorkspaceID: "w9", ClosedAt: &closed}, "(gone)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := spaceColumn(tc.thread, labels); got != tc.want {
				t.Errorf("spaceColumn = %q, want %q", got, tc.want)
			}
		})
	}

	// No labels at all is herdr being unavailable, not every space vanishing.
	if got := spaceColumn(store.Thread{ID: "bl", WorkspaceID: "w7"}, nil); got != "w7" {
		t.Errorf("spaceColumn = %q with no labels, want the id", got)
	}
}

// --thread on a claim command is accepted and does nothing, so it has to say so.
//
// A filter that is silently ignored is worse than one that errors: the caller
// reads a global list believing it was narrowed to their thread, and concludes
// nobody holds the browser.
func TestClaimsAreGlobalSpeaksUpOnlyWhenTheFlagWasPassed(t *testing.T) {
	if got := captureStderr(t, func() { claimsAreGlobal("", "does not narrow this") }); got != "" {
		t.Errorf("said %q with no --thread, want silence", got)
	}

	got := captureStderr(t, func() { claimsAreGlobal("webapp", "does not narrow this") })
	if got == "" {
		t.Fatal("said nothing about a --thread it ignored")
	}
	if !strings.Contains(got, "does not narrow this") {
		t.Errorf("said %q, want the caller's explanation of what the flag did not do", got)
	}

	// Whitespace is the same as unset — a script passing --thread "$BERMUDA_THREAD"
	// with the variable empty has not asked for anything.
	if got := captureStderr(t, func() { claimsAreGlobal("  ", "why") }); got != "" {
		t.Errorf("said %q for a blank --thread, want silence", got)
	}
}

// captureStderr collects what fn writes to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	previous := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = previous
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(out)
}
