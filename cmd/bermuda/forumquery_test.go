package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// The read side of the forum, driven through argv the way an agent drives it.
//
// Posting is already covered; what was not is everything an agent does to find
// a post again a week later. These assert the --json output, because that is
// the contract another agent parses — a listing that quietly included replies,
// a --board filter that did not narrow, or a feed that handed back a post the
// caller had already marked read would all still print something plausible.

// decodeForumJSON runs a forum subcommand with --json and decodes what it
// printed into v.
func decodeForumJSON(t *testing.T, v any, argv ...string) {
	t.Helper()
	out, err := captureStdout(t, func() error { return forumCmd(argv) })
	if err != nil {
		t.Fatalf("forum %s: %v", strings.Join(argv, " "), err)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("forum %s printed %q, which is not JSON: %v",
			strings.Join(argv, " "), out, err)
	}
}

// seedForum posts a root on each of two boards plus one reply, and returns the
// roots in the order they were created.
func seedForum(t *testing.T, s *store.Store) (ops, notes store.ForumPost) {
	t.Helper()
	ctx := context.Background()
	var err error
	ops, err = s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "raphael",
		Title: "browser claim stuck", Body: "chromium-cdp died holding the claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForumCreate(ctx, store.ForumPost{
		Board: "ops", Author: "june", ParentID: ops.ID, Body: "restarting the unit clears it",
	}); err != nil {
		t.Fatal(err)
	}
	notes, err = s.ForumCreate(ctx, store.ForumPost{
		Board: "notes", Author: "june",
		Title: "railway template pricing", Body: "the listing converts better with a screenshot",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ops, notes
}

// A listing shows threads, not the answers to them. Mixing replies in by
// default would bury each question under its own thread.
func TestForumListShowsThreadRootsUnlessAskedForReplies(t *testing.T) {
	s := forumEnv(t)
	seedForum(t, s)

	var roots []store.ForumPost
	decodeForumJSON(t, &roots, "ls", "--json")
	if len(roots) != 2 {
		t.Fatalf("ls returned %d posts, want the 2 thread roots", len(roots))
	}
	for _, p := range roots {
		if p.ParentID != "" {
			t.Errorf("ls returned reply %s; roots only", p.ID)
		}
	}

	var all []store.ForumPost
	decodeForumJSON(t, &all, "ls", "--replies", "--json")
	if len(all) != 3 {
		t.Fatalf("ls --replies returned %d posts, want all 3", len(all))
	}
}

func TestForumListFiltersNarrow(t *testing.T) {
	s := forumEnv(t)
	ops, notes := seedForum(t, s)

	cases := []struct {
		name string
		argv []string
		want []string // ids, in any order
	}{
		{"board", []string{"ls", "--board", "ops"}, []string{ops.ID}},
		{"author", []string{"ls", "--author", "june"}, []string{notes.ID}},
		{"board and author together", []string{"ls", "--board", "ops", "--author", "june"}, nil},
		{"limit caps the listing", []string{"ls", "--limit", "1"}, []string{notes.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []store.ForumPost
			decodeForumJSON(t, &got, append(tc.argv, "--json")...)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d posts, want %d: %+v", len(got), len(tc.want), got)
			}
			seen := map[string]bool{}
			for _, p := range got {
				seen[p.ID] = true
			}
			for _, id := range tc.want {
				if !seen[id] {
					t.Errorf("missing %s from %+v", id, got)
				}
			}
		})
	}
}

// --since takes the forms an agent actually types. A silently wrong cutoff
// hides recent posts rather than erroring, so each form is pinned.
func TestForumListSinceAcceptsTheDocumentedForms(t *testing.T) {
	s := forumEnv(t)
	seedForum(t, s)

	for _, since := range []string{"2h", "7d", "1970-01-01", "1970-01-01T00:00:00Z"} {
		t.Run(since, func(t *testing.T) {
			var got []store.ForumPost
			decodeForumJSON(t, &got, "ls", "--since", since, "--json")
			if len(got) != 2 {
				t.Fatalf("--since %s returned %d roots, want both", since, len(got))
			}
		})
	}

	// A cutoff in the future hides everything rather than being ignored.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	var got []store.ForumPost
	decodeForumJSON(t, &got, "ls", "--since", future, "--json")
	if len(got) != 0 {
		t.Fatalf("--since %s returned %+v, want nothing", future, got)
	}

	if err := forumCmd([]string{"ls", "--since", "nonsense", "--json"}); err == nil {
		t.Error("an unreadable --since should be refused, not treated as no cutoff")
	}
}

// A deleted post stays addressable but leaves the listing, so replies quoting
// it still resolve while nobody reads a retracted answer as current.
func TestForumListHidesDeletedPostsUnlessAsked(t *testing.T) {
	s := forumEnv(t)
	ops, _ := seedForum(t, s)
	if err := s.ForumDelete(context.Background(), ops.ID, "raphael"); err != nil {
		t.Fatal(err)
	}

	var live []store.ForumPost
	decodeForumJSON(t, &live, "ls", "--json")
	for _, p := range live {
		if p.ID == ops.ID {
			t.Fatalf("deleted post %s still listed", ops.ID)
		}
	}

	var withDeleted []store.ForumPost
	decodeForumJSON(t, &withDeleted, "ls", "--deleted", "--json")
	found := false
	for _, p := range withDeleted {
		if p.ID == ops.ID {
			found = true
			if !p.Deleted() {
				t.Error("the deleted post is not marked deleted")
			}
		}
	}
	if !found {
		t.Errorf("--deleted did not include %s", ops.ID)
	}
}

// show prints the whole thread, and the reply is nested under what it answers.
func TestForumShowReturnsTheThreadWithDepth(t *testing.T) {
	s := forumEnv(t)
	ops, _ := seedForum(t, s)

	var thread []store.ForumPost
	decodeForumJSON(t, &thread, "show", ops.ID, "--json")
	if len(thread) != 2 {
		t.Fatalf("thread has %d posts, want the root and its reply", len(thread))
	}
	if thread[0].ID != ops.ID || thread[0].Depth != 0 {
		t.Errorf("first post = %s depth %d, want the root at depth 0", thread[0].ID, thread[0].Depth)
	}
	if thread[1].ParentID != ops.ID || thread[1].Depth != 1 {
		t.Errorf("reply = parent %s depth %d, want %s at depth 1",
			thread[1].ParentID, thread[1].Depth, ops.ID)
	}
	if err := forumCmd([]string{"show"}); err == nil {
		t.Error("show without an id should be refused")
	}
}

// Search is the whole reason to post: an agent that hits the same wall later
// finds the answer by a word from the body, on any board it did not know about.
func TestForumSearchFindsByBodyAndNarrowsByBoard(t *testing.T) {
	s := forumEnv(t)
	ops, _ := seedForum(t, s)

	var hits []store.ForumHit
	decodeForumJSON(t, &hits, "search", "chromium", "--json")
	if len(hits) == 0 {
		t.Fatal("searching for a word in the body found nothing")
	}
	if hits[0].ID != ops.ID {
		t.Errorf("first hit = %s, want %s", hits[0].ID, ops.ID)
	}

	var wrongBoard []store.ForumHit
	decodeForumJSON(t, &wrongBoard, "search", "chromium", "--board", "notes", "--json")
	if len(wrongBoard) != 0 {
		t.Errorf("--board notes returned %+v, want nothing", wrongBoard)
	}

	var none []store.ForumHit
	decodeForumJSON(t, &none, "search", "nothingmatchesthis", "--json")
	if len(none) != 0 {
		t.Errorf("search for an absent term returned %+v", none)
	}

	if err := forumCmd([]string{"search"}); err == nil {
		t.Error("search without a query should be refused")
	}
}

// The feed answers "what have I not seen", and --mark makes that answer shrink.
// Without the watermark advancing, a job on a timer re-reads the whole forum
// every run and every post looks new forever.
func TestForumFeedAdvancesOnlyWhenMarked(t *testing.T) {
	s := forumEnv(t)
	seedForum(t, s)

	var first []store.ForumPost
	decodeForumJSON(t, &first, "feed", "--as", "watcher", "--json")
	if len(first) != 3 {
		t.Fatalf("first feed returned %d posts, want everything", len(first))
	}

	// Reading without --mark leaves the position where it was.
	var again []store.ForumPost
	decodeForumJSON(t, &again, "feed", "--as", "watcher", "--json")
	if len(again) != 3 {
		t.Fatalf("feed without --mark returned %d posts, want the same 3", len(again))
	}

	var marked []store.ForumPost
	decodeForumJSON(t, &marked, "feed", "--as", "watcher", "--mark", "--json")
	if len(marked) != 3 {
		t.Fatalf("feed --mark returned %d posts, want the same 3", len(marked))
	}
	var afterMark []store.ForumPost
	decodeForumJSON(t, &afterMark, "feed", "--as", "watcher", "--json")
	if len(afterMark) != 0 {
		t.Fatalf("after --mark the feed returned %+v, want nothing new", afterMark)
	}
}

// `forum read` is the same watermark without printing the backlog first.
func TestForumMarkReadCatchesAUsernameUp(t *testing.T) {
	s := forumEnv(t)
	seedForum(t, s)

	out, err := captureStdout(t, func() error {
		return forumCmd([]string{"read", "--as", "watcher"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "watcher") {
		t.Errorf("read printed %q, which does not name who was caught up", out)
	}

	var feed []store.ForumPost
	decodeForumJSON(t, &feed, "feed", "--as", "watcher", "--json")
	if len(feed) != 0 {
		t.Fatalf("feed after `read` returned %+v, want nothing", feed)
	}

	// A per-board watermark must not silence the boards it did not name.
	if _, err := s.ForumCreate(context.Background(), store.ForumPost{
		Board: "ops", Author: "raphael", Body: "unit restarted",
	}); err != nil {
		t.Fatal(err)
	}
	if err := forumCmd([]string{"read", "--as", "second", "--board", "notes"}); err != nil {
		t.Fatal(err)
	}
	var partial []store.ForumPost
	decodeForumJSON(t, &partial, "feed", "--as", "second", "--board", "ops", "--json")
	if len(partial) == 0 {
		t.Error("marking notes read also silenced ops")
	}
}

// boards is how an agent discovers where to post. The counters are the whole
// content of that answer, and a thread count that counted replies would make an
// idle board look busy.
func TestForumBoardsCountsThreadsAndPosts(t *testing.T) {
	s := forumEnv(t)
	seedForum(t, s)

	var boards []store.ForumBoard
	decodeForumJSON(t, &boards, "boards", "--json")
	byName := map[string]store.ForumBoard{}
	for _, b := range boards {
		byName[b.Name] = b
	}
	ops, ok := byName["ops"]
	if !ok {
		t.Fatalf("boards = %+v, want one named ops", boards)
	}
	if ops.Threads != 1 || ops.Posts != 2 {
		t.Errorf("ops = %d threads / %d posts, want 1 thread and 2 posts", ops.Threads, ops.Posts)
	}
	if ops.LastAt == 0 {
		t.Error("ops has no last-post time")
	}
	if _, ok := byName["notes"]; !ok {
		t.Errorf("boards = %+v, want notes too", boards)
	}
}

// A board can be declared before anything is posted to it, which is how a
// description gets attached at all.
func TestForumBoardNewRegistersAnEmptyBoard(t *testing.T) {
	forumEnv(t)
	if _, err := captureStdout(t, func() error {
		return forumCmd([]string{"board", "new", "railway", "--about", "template work"})
	}); err != nil {
		t.Fatal(err)
	}

	var boards []store.ForumBoard
	decodeForumJSON(t, &boards, "boards", "--json")
	if len(boards) != 1 {
		t.Fatalf("boards = %+v, want just railway", boards)
	}
	if boards[0].Name != "railway" || boards[0].Description != "template work" {
		t.Errorf("board = %+v, want railway described as template work", boards[0])
	}
	if boards[0].Posts != 0 || boards[0].Threads != 0 {
		t.Errorf("a fresh board counts %d posts / %d threads, want zero",
			boards[0].Posts, boards[0].Threads)
	}

	if err := forumCmd([]string{"board", "new"}); err == nil {
		t.Error("board new without a name should be refused")
	}
	if err := forumCmd([]string{"board"}); err == nil {
		t.Error("board without a subcommand should be refused")
	}
}
