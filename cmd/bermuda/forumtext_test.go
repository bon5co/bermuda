package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The forum's human-readable output.
//
// The --json contract is covered elsewhere, and it is the half a machine reads.
// This is the other half: what an agent or Handler actually looks at in a
// terminal. The two are rendered by separate branches of the same command, so a
// passing JSON test says nothing about them — a thread that lost the indent
// saying who answered whom, a tombstone that printed the body it was supposed
// to withhold, or an empty result that printed a bare header and no explanation
// would all go unnoticed with every existing test still green.
//
// These assert invariants rather than exact rows: the tabwriter's padding is
// free to change, but a deleted body staying out of the output is not.

// leadingSpaces reports the indent on a line, which is how forum show says
// which post answered which.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// lineWith returns the first output line containing want.
func lineWith(t *testing.T, out, want string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no line containing %q in:\n%s", want, out)
	return ""
}

// A thread is printed root first, with each reply indented under what it
// answered. The depth is the only thing in the plain output that carries the
// shape of the conversation: flatten it and a five-post thread reads as five
// unrelated posts.
func TestForumShowIndentsRepliesUnderWhatTheyAnswer(t *testing.T) {
	s := forumEnv(t)
	ctx := context.Background()

	root, err := s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "raphael",
		Title: "browser claim stuck", Body: "chromium-cdp died holding the claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "june", ParentID: root.ID,
		Body: "restarting the unit clears it",
	})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "raphael", ParentID: reply.ID,
		Body: "confirmed, the claim is released with it",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return forumCmd([]string{"show", root.ID}) })
	if err != nil {
		t.Fatalf("forum show: %v", err)
	}

	rootIndent := leadingSpaces(lineWith(t, out, root.ID))
	replyIndent := leadingSpaces(lineWith(t, out, reply.ID))
	nestedIndent := leadingSpaces(lineWith(t, out, nested.ID))
	if rootIndent != 0 {
		t.Errorf("the thread root is indented by %d, want 0", rootIndent)
	}
	if replyIndent <= rootIndent {
		t.Errorf("a reply is indented by %d and its root by %d, want the reply deeper",
			replyIndent, rootIndent)
	}
	if nestedIndent <= replyIndent {
		t.Errorf("a reply to a reply is indented by %d and its parent by %d, want it deeper",
			nestedIndent, replyIndent)
	}

	// The head line carries the title, and the line under it says who wrote it
	// and where — an agent reading a thread it did not post in has no other
	// way to tell whose claim it is looking at.
	if head := lineWith(t, out, root.ID); !strings.Contains(head, "browser claim stuck") {
		t.Errorf("the root's head line %q does not carry its title", head)
	}
	for _, want := range []string{"raphael", "june", "ops",
		"chromium-cdp died holding the claim", "restarting the unit clears it"} {
		if !strings.Contains(out, want) {
			t.Errorf("forum show did not print %q:\n%s", want, out)
		}
	}
}

// A body that spans lines keeps its lines, each one indented to the post it
// belongs to. Printing a multi-line body with only the first line indented puts
// the rest at the margin, where it reads as a different post.
func TestForumShowIndentsEveryLineOfABody(t *testing.T) {
	s := forumEnv(t)
	ctx := context.Background()

	root, err := s.ForumCreate(ctx, store.ForumPost{Board: "ops", Author: "raphael", Body: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "june", ParentID: root.ID,
		Body: "first line\nsecond line\nthird line",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return forumCmd([]string{"show", root.ID}) })
	if err != nil {
		t.Fatalf("forum show: %v", err)
	}
	first := leadingSpaces(lineWith(t, out, "first line"))
	for _, line := range []string{"second line", "third line"} {
		if got := leadingSpaces(lineWith(t, out, line)); got != first {
			t.Errorf("%q is indented by %d, want %d like the rest of its body", line, got, first)
		}
	}
}

// Deleting a post keeps the id resolvable so replies quoting it still make
// sense — the body is what goes away. Printing it anyway would make `forum rm`
// a lie in the one view a human actually reads.
func TestForumShowWithholdsTheBodyOfADeletedPost(t *testing.T) {
	s := forumEnv(t)
	ctx := context.Background()

	const secret = "the body that was withdrawn"
	root, err := s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "raphael", Title: "withdrawn", Body: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "june", ParentID: root.ID, Body: "still readable",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForumDelete(ctx, root.ID, "raphael"); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return forumCmd([]string{"show", root.ID}) })
	if err != nil {
		t.Fatalf("forum show: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("forum show printed the body of a deleted post:\n%s", out)
	}
	if !strings.Contains(out, "[deleted]") {
		t.Errorf("forum show did not mark the tombstone as deleted:\n%s", out)
	}
	if !strings.Contains(out, "still readable") {
		t.Errorf("forum show dropped a live reply under a deleted root:\n%s", out)
	}
}

// A search that matched nothing says so. A bare header with no rows under it is
// indistinguishable from a search that crashed after printing it, and the
// caller's next move differs.
func TestForumSearchSaysWhenNothingMatched(t *testing.T) {
	s := forumEnv(t)
	if _, err := s.ForumCreate(context.Background(), store.ForumPost{
		Board: "ops", Author: "raphael", Body: "chromium-cdp died holding the claim",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return forumCmd([]string{"search", "postgres"})
	})
	if err != nil {
		t.Fatalf("forum search: %v", err)
	}
	if !strings.Contains(out, "no matches") {
		t.Errorf("a search with no hits printed %q, want it to say there were none", out)
	}
}

// A hit carries the four things needed to act on it: which post, which board,
// who wrote it, and enough of the text to tell whether it is the right one. The
// id most of all — a hit nobody can pass to `forum show` is not a hit.
func TestForumSearchRowNamesThePostAndItsBoard(t *testing.T) {
	s := forumEnv(t)
	if !s.ForumSearchable() {
		t.Skip("this build has no FTS5, so search has nothing to render")
	}
	p, err := s.ForumCreate(context.Background(), store.ForumPost{
		Board: "ops", Author: "raphael",
		Title: "browser claim stuck", Body: "chromium-cdp died holding the claim",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return forumCmd([]string{"search", "chromium"})
	})
	if err != nil {
		t.Fatalf("forum search: %v", err)
	}
	row := lineWith(t, out, p.ID)
	for _, want := range []string{"ops", "raphael"} {
		if !strings.Contains(row, want) {
			t.Errorf("the hit row %q does not carry %q", row, want)
		}
	}
	// Every hit is one row: a body with newlines in it must not break the
	// table into rows that look like separate posts.
	if n := strings.Count(strings.TrimSpace(out), "\n"); n != 1 {
		t.Errorf("one hit and a header printed %d newlines, want 1:\n%s", n, out)
	}
}

// An empty forum is a state a fresh install is in, and the answer to `forum
// boards` there has to say what to do next rather than print nothing.
func TestForumBoardsSaysWhenThereAreNone(t *testing.T) {
	forumEnv(t)
	out, err := captureStdout(t, func() error { return forumCmd([]string{"boards"}) })
	if err != nil {
		t.Fatalf("forum boards: %v", err)
	}
	if !strings.Contains(out, "no boards yet") {
		t.Errorf("an empty forum printed %q, want it to say there are no boards", out)
	}
}

// The board table's counts are what an agent uses to decide where to look, and
// threads and posts are different numbers: a board with one root and three
// replies is one thread and four posts. Reporting either in the other's column
// sends the reader to the busiest-looking board rather than the busiest one.
func TestForumBoardsCountsThreadsApartFromPosts(t *testing.T) {
	s := forumEnv(t)
	ctx := context.Background()

	root, err := s.ForumCreate(ctx, store.ForumPost{Board: "ops", Author: "raphael", Body: "root"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"one", "two", "three"} {
		if _, err := s.ForumCreate(ctx, store.ForumPost{
			Board: "ops", Author: "june", ParentID: root.ID, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error { return forumCmd([]string{"boards"}) })
	if err != nil {
		t.Fatalf("forum boards: %v", err)
	}
	row := lineWith(t, out, "ops")
	fields := strings.Fields(row)
	if len(fields) < 3 {
		t.Fatalf("board row %q has %d fields, want at least name, threads and posts",
			row, len(fields))
	}
	if fields[1] != "1" || fields[2] != "4" {
		t.Errorf("board row %q reports threads=%s posts=%s, want 1 and 4",
			row, fields[1], fields[2])
	}
	if strings.Contains(row, "never") {
		t.Errorf("board row %q says its last post was never, but it has four", row)
	}
}

// A board nobody has posted on yet reports its last post as "never" rather than
// rendering a zero timestamp as a date in 1970.
func TestForumBoardsSaysNeverForABoardWithNoPosts(t *testing.T) {
	s := forumEnv(t)
	if _, err := s.ForumBoardPut(context.Background(), "empty", "nothing here yet"); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return forumCmd([]string{"boards"}) })
	if err != nil {
		t.Fatalf("forum boards: %v", err)
	}
	row := lineWith(t, out, "empty")
	if !strings.Contains(row, "never") {
		t.Errorf("an unused board's row is %q, want its last post reported as never", row)
	}
	if !strings.Contains(row, "nothing here yet") {
		t.Errorf("an unused board's row is %q, want it to carry the description", row)
	}
}

// The feed's empty answer names the user it answered for. The watermark is per
// username, so "nothing new" without a name is unreadable on a machine where
// several agents share one terminal — the reader cannot tell whose position
// was consulted.
func TestForumFeedNamesTheUserItFoundNothingFor(t *testing.T) {
	forumEnv(t)
	out, err := captureStdout(t, func() error {
		return forumCmd([]string{"feed", "--as", "raphael"})
	})
	if err != nil {
		t.Fatalf("forum feed: %v", err)
	}
	if !strings.Contains(out, "nothing new") || !strings.Contains(out, "raphael") {
		t.Errorf("an empty feed printed %q, want it to say nothing is new for raphael", out)
	}
}
