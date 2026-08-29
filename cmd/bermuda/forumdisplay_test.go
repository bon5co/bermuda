package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// forumBody is where an agent's post text actually comes from. A slip here does
// not fail loudly — it posts the wrong text, or an empty body, and nobody
// notices until the post is read weeks later.
func TestForumBodyTakesTheTextFromFlagFileOrStdin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "body.md")
	const fromFile = "line one\nline two\n"
	if err := os.WriteFile(file, []byte(fromFile), 0o644); err != nil {
		t.Fatal(err)
	}

	// A file wins over an inline body: --body-file is the deliberate choice.
	got, err := forumBody("inline", file)
	if err != nil {
		t.Fatal(err)
	}
	if got != fromFile {
		t.Fatalf("body from file = %q, want %q", got, fromFile)
	}

	// The file is taken whole, newlines and all — that is why the flag exists.
	if got, err := forumBody("", file); err != nil || got != fromFile {
		t.Fatalf("body from file alone = %q, %v", got, err)
	}

	if _, err := forumBody("", filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Fatal("a missing --body-file should be an error, not an empty post")
	}

	// A literal body passes through untouched, including a leading dash that is
	// not the stdin sentinel.
	for _, body := range []string{"", "plain text", "-x", "a - b"} {
		got, err := forumBody(body, "")
		if err != nil {
			t.Fatalf("forumBody(%q): %v", body, err)
		}
		if got != body {
			t.Fatalf("forumBody(%q) = %q, want it unchanged", body, got)
		}
	}
}

func TestForumBodyDashReadsStdinWhole(t *testing.T) {
	const piped = "heredoc \"quoted\"\n\nsecond paragraph\n"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = previous }()

	go func() {
		io.WriteString(w, piped)
		w.Close()
	}()

	got, err := forumBody("-", "")
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got != piped {
		t.Fatalf("body from stdin = %q, want %q", got, piped)
	}
}

// oneLine feeds a table cell. The two things a caller relies on are that the
// result is a single line and that it never exceeds the cell, whatever bytes
// the body holds.
func TestOneLineFlattensAndBoundsTheCell(t *testing.T) {
	const limit = 72

	cases := []struct {
		name string
		in   string
		want string // empty means "only check the invariants"
	}{
		{name: "empty", in: "", want: ""},
		{name: "trims surrounding space", in: "  hello  ", want: "hello"},
		{name: "newlines become spaces", in: "one\ntwo\nthree", want: "one two three"},
		{name: "runs of whitespace collapse", in: "one   \t  two\n\n\nthree", want: "one two three"},
		{name: "short body is untouched", in: "a short note", want: "a short note"},
		{name: "exactly at the limit is untouched", in: strings.Repeat("a", limit), want: strings.Repeat("a", limit)},
		{name: "one over the limit is cut", in: strings.Repeat("a", limit+1), want: strings.Repeat("a", limit-1) + "…"},
		{name: "long ascii body"},
		{name: "long multibyte body"},
	}
	cases[len(cases)-2].in = strings.Repeat("word ", 100)
	cases[len(cases)-1].in = strings.Repeat("日本語", 100)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := oneLine(tc.in)
			if tc.want != "" && got != tc.want {
				t.Fatalf("oneLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.ContainsAny(got, "\n\r\t") {
				t.Fatalf("oneLine(%q) = %q, want a single line", tc.in, got)
			}
			if n := len([]rune(got)); n > limit {
				t.Fatalf("oneLine(%q) is %d runes, want at most %d", tc.in, n, limit)
			}
			if strings.ContainsRune(got, '�') {
				t.Fatalf("oneLine(%q) = %q, want no rune cut in half", tc.in, got)
			}
		})
	}
}

// printForumPosts is the listing every `bermuda forum` subcommand prints. The
// invariants a reader depends on: one row per post, a deleted post shows
// nothing of its body, and a reply is marked as one.
func TestPrintForumPostsMarksRepliesAndHidesDeletedBodies(t *testing.T) {
	posts := []store.ForumPost{
		{ID: "p1", Board: "ops", Author: "sentinel", Title: "daemon restart", CreatedAt: 1, Replies: 2},
		{ID: "p2", Board: "ops", Author: "runner", Body: "no title here,\njust a body", CreatedAt: 2},
		{ID: "p3", Board: "ops", Author: "runner", ParentID: "p1", Title: "restarted", CreatedAt: 3},
		{ID: "p4", Board: "ops", Author: "runner", Title: "secret", Body: "leaked-secret", DeletedAt: 4, CreatedAt: 4},
	}

	out, err := captureStdout(t, func() error {
		printForumPosts(posts)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(posts)+1 {
		t.Fatalf("got %d lines, want a header plus %d posts:\n%s", len(lines), len(posts), out)
	}
	if !strings.Contains(lines[0], "TITLE") || !strings.Contains(lines[0], "REPLIES") {
		t.Fatalf("header line = %q, want the column names", lines[0])
	}

	for i, p := range posts {
		if !strings.Contains(lines[i+1], p.ID) {
			t.Fatalf("row %d = %q, want it to carry id %s", i, lines[i+1], p.ID)
		}
	}
	if !strings.Contains(lines[2], "no title here, just a body") {
		t.Fatalf("untitled row = %q, want the body flattened into the title cell", lines[2])
	}
	if !strings.Contains(lines[3], "re: restarted") {
		t.Fatalf("reply row = %q, want it marked as a reply", lines[3])
	}
	if !strings.Contains(lines[4], "[deleted]") {
		t.Fatalf("deleted row = %q, want it to read [deleted]", lines[4])
	}
	if strings.Contains(out, "leaked-secret") || strings.Contains(lines[4], "secret") {
		t.Fatalf("deleted row = %q, want nothing of the removed post shown", lines[4])
	}
}

func TestPrintForumPostsSaysSoWhenThereAreNone(t *testing.T) {
	out, err := captureStdout(t, func() error {
		printForumPosts(nil)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "no posts" {
		t.Fatalf("empty listing printed %q, want a plain \"no posts\"", out)
	}
}

// A row must stay one line, or the table stops lining up: a body with newlines
// in it is the case that breaks this, and it arrives from every agent that
// writes a multi-paragraph post without a title.
func TestPrintForumPostsKeepsEachPostOnOneRow(t *testing.T) {
	posts := []store.ForumPost{
		{ID: "p1", Board: "ops", Author: "runner", Body: strings.Repeat("long body text ", 40)},
		{ID: "p2", Board: "ops", Author: "runner", Body: "first\nsecond\nthird"},
	}
	out, err := captureStdout(t, func() error {
		printForumPosts(posts)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(posts)+1 {
		t.Fatalf("got %d lines, want a header plus %d posts:\n%s", len(lines), len(posts), out)
	}
}
