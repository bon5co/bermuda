package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests cover the properties that decide whether the forum is usable by
// agents rather than merely present: a retry does not double-post, an id keeps
// resolving after a delete, a thread reads in conversation order, search finds
// a post by a word in its body, and the feed watermark never shows the same
// post twice.

func newForum(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func post(t *testing.T, s *Store, p ForumPost) ForumPost {
	t.Helper()
	out, err := s.ForumCreate(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestForumPostCreatesItsBoardAndReadsBack(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	p := post(t, s, ForumPost{Board: "Ops Notes", Author: "raphael", Title: "hello", Body: "world"})

	// The board name is folded to one spelling so "Ops Notes" and "ops-notes"
	// are the same board rather than two.
	if p.Board != "ops-notes" {
		t.Errorf("board = %q, want ops-notes", p.Board)
	}
	if p.Root != p.ID {
		t.Errorf("a new thread should be its own root, got root %q for %q", p.Root, p.ID)
	}
	boards, err := s.ForumBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 1 || boards[0].Threads != 1 {
		t.Fatalf("boards = %+v, want one board with one thread", boards)
	}
}

func TestForumPostNeedsAnAuthorAndSomethingToSay(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	if _, err := s.ForumCreate(ctx, ForumPost{Board: "ops", Body: "x"}); err == nil {
		t.Error("a post with no author should be refused")
	}
	if _, err := s.ForumCreate(ctx, ForumPost{Board: "ops", Author: "x"}); err == nil {
		t.Error("a post with no title and no body should be refused")
	}
	if _, err := s.ForumCreate(ctx, ForumPost{Board: "ops", Author: "x", Body: "y", Meta: "{oops"}); err == nil {
		t.Error("meta that is not JSON should be refused")
	}
}

// An agent that is unsure whether its post landed retries it. Without the
// idempotency key that is how a board fills with triplicates.
func TestForumIdemKeyReturnsTheFirstPostInsteadOfDuplicating(t *testing.T) {
	s := newForum(t)
	first := post(t, s, ForumPost{Board: "ops", Author: "raphael", Body: "one", Idem: "k1"})
	again := post(t, s, ForumPost{Board: "ops", Author: "raphael", Body: "changed", Idem: "k1"})
	if again.ID != first.ID {
		t.Fatalf("retry made a second post: %s then %s", first.ID, again.ID)
	}
	if again.Body != "one" {
		t.Errorf("retry rewrote the body to %q", again.Body)
	}
	// The key is per author, so two agents can both use "daily-summary".
	other := post(t, s, ForumPost{Board: "ops", Author: "june", Body: "hers", Idem: "k1"})
	if other.ID == first.ID {
		t.Error("one author's idem key swallowed another author's post")
	}
}

func TestForumReplyInheritsTheBoardAndTheThread(t *testing.T) {
	s := newForum(t)
	root := post(t, s, ForumPost{Board: "ops", Author: "raphael", Body: "question"})
	reply := post(t, s, ForumPost{ParentID: root.ID, Board: "somewhere-else", Author: "june", Body: "answer"})
	if reply.Board != root.Board {
		t.Errorf("reply landed on board %q, want %q", reply.Board, root.Board)
	}
	if reply.Root != root.ID {
		t.Errorf("reply root = %q, want %q", reply.Root, root.ID)
	}
	deep := post(t, s, ForumPost{ParentID: reply.ID, Author: "raphael", Body: "thanks"})
	if deep.Root != root.ID {
		t.Errorf("a reply to a reply left the thread: root %q", deep.Root)
	}
}

func TestForumThreadReadsInConversationOrderWithDepth(t *testing.T) {
	s := newForum(t)
	root := post(t, s, ForumPost{Board: "ops", Author: "a", Body: "root"})
	first := post(t, s, ForumPost{ParentID: root.ID, Author: "b", Body: "first"})
	post(t, s, ForumPost{ParentID: first.ID, Author: "c", Body: "nested"})
	post(t, s, ForumPost{ParentID: root.ID, Author: "d", Body: "second"})

	posts, err := s.ForumThread(context.Background(), first.ID) // any id in the thread
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range posts {
		got = append(got, p.Body)
	}
	want := []string{"root", "first", "nested", "second"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("thread order = %v, want %v", got, want)
	}
	if posts[2].Depth != 2 {
		t.Errorf("nested reply depth = %d, want 2", posts[2].Depth)
	}
	if posts[0].Replies != 2 {
		t.Errorf("root direct replies = %d, want 2", posts[0].Replies)
	}
}

// Ids are quoted between agents, so a delete must leave the id resolvable and
// the replies readable. Anything else breaks references that were correct when
// they were written.
func TestForumDeleteKeepsTheIdAndHidesTheText(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	root := post(t, s, ForumPost{Board: "ops", Author: "raphael", Title: "t", Body: "secret"})
	post(t, s, ForumPost{ParentID: root.ID, Author: "june", Body: "still here"})

	if err := s.ForumDelete(ctx, root.ID, "june"); !errors.Is(err, ErrForumDenied) {
		t.Fatalf("deleting another author's post: err = %v, want ErrForumDenied", err)
	}
	if err := s.ForumDelete(ctx, root.ID, "raphael"); err != nil {
		t.Fatal(err)
	}
	posts, err := s.ForumThread(ctx, root.ID)
	if err != nil {
		t.Fatalf("thread stopped resolving after a delete: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("thread = %d posts, want the tombstone and its reply", len(posts))
	}
	if !posts[0].Deleted() || posts[0].Body != "" || posts[0].Title != "" {
		t.Errorf("deleted post still readable: %+v", posts[0])
	}
	if posts[1].Body != "still here" {
		t.Errorf("reply lost with its parent: %+v", posts[1])
	}
	// Deleting twice is a no-op rather than an error: a retrying agent must be
	// able to repeat it.
	if err := s.ForumDelete(ctx, root.ID, "raphael"); err != nil {
		t.Errorf("second delete: %v", err)
	}
	// And it leaves listings alone.
	live, err := s.ForumList(ctx, ForumQuery{Parent: "-"})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("deleted thread still listed: %+v", live)
	}
}

func TestForumUpdateOnlyByItsAuthorAndOnlyTheFieldsGiven(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	p := post(t, s, ForumPost{Board: "ops", Author: "raphael", Title: "title", Body: "body"})
	if _, err := s.ForumUpdate(ctx, p.ID, "june", "hijack", "", ""); !errors.Is(err, ErrForumDenied) {
		t.Fatalf("err = %v, want ErrForumDenied", err)
	}
	got, err := s.ForumUpdate(ctx, p.ID, "raphael", "", "new body", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "title" {
		t.Errorf("a body edit blanked the title: %q", got.Title)
	}
	if got.Body != "new body" {
		t.Errorf("body = %q", got.Body)
	}
	if got.UpdatedAt < got.CreatedAt {
		t.Error("updated_at did not move")
	}
}

func TestForumSearchFindsPostsAndFollowsEdits(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	p := post(t, s, ForumPost{Board: "ops", Author: "raphael",
		Title: "browser claim stuck", Body: "chromium-cdp died holding the claim"})
	post(t, s, ForumPost{Board: "growth", Author: "june", Body: "nothing to do with browsers"})

	hits, err := s.ForumSearch(ctx, "chromium", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != p.ID {
		t.Fatalf("search hits = %+v, want just %s", hits, p.ID)
	}
	if s.ForumSearchable() && !strings.Contains(hits[0].Snippet, "[") {
		t.Errorf("FTS5 snippet carries no marks: %q", hits[0].Snippet)
	}
	// The board filter narrows rather than being ignored.
	if hits, err = s.ForumSearch(ctx, "chromium", "growth", 10); err != nil || len(hits) != 0 {
		t.Fatalf("board filter: hits = %+v, err = %v", hits, err)
	}
	// An edit has to reach the index, or search answers from the old text.
	if _, err := s.ForumUpdate(ctx, p.ID, "raphael", "", "the unit was masked", ""); err != nil {
		t.Fatal(err)
	}
	if hits, err = s.ForumSearch(ctx, "chromium", "", 10); err != nil {
		t.Fatal(err)
	} else if len(hits) != 0 {
		t.Errorf("edited-away word still matches: %+v", hits)
	}
	if hits, err = s.ForumSearch(ctx, "masked", "", 10); err != nil || len(hits) != 1 {
		t.Fatalf("edited text not searchable: hits = %+v, err = %v", hits, err)
	}
	// And a delete has to leave the index, or search leaks a deleted body.
	if err := s.ForumDelete(ctx, p.ID, "raphael"); err != nil {
		t.Fatal(err)
	}
	if hits, err = s.ForumSearch(ctx, "masked", "", 10); err != nil || len(hits) != 0 {
		t.Fatalf("deleted post still searchable: hits = %+v, err = %v", hits, err)
	}
}

// A malformed FTS expression is a SQLite syntax error, not an empty result.
// Agents type queries with stray quotes and parentheses in them, and being
// handed "fts5: syntax error" is not something the caller can act on.
func TestForumSearchSurvivesAQueryFTSCannotParse(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	post(t, s, ForumPost{Board: "ops", Author: "raphael", Body: "railway template published"})
	hits, err := s.ForumSearch(ctx, `railway (template`, "", 10)
	if err != nil {
		t.Fatalf("unparseable query errored instead of falling back: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("hits = %d, want the one post", len(hits))
	}
}

func TestForumFeedShowsEachPostToAUsernameOnce(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	first := post(t, s, ForumPost{Board: "ops", Author: "raphael", Body: "one"})

	got, err := s.ForumFeed(ctx, "watcher", "", 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("first feed = %+v, want the one post", got)
	}
	if got, err = s.ForumFeed(ctx, "watcher", "", 10, true); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("feed repeated itself: %+v", got)
	}

	// A post written after the watermark shows up; the timestamp is forced
	// because two posts in the same second are otherwise indistinguishable to a
	// second-resolution watermark.
	later := post(t, s, ForumPost{Board: "ops", Author: "june", Body: "two"})
	if _, err := s.db.ExecContext(ctx, `UPDATE forum_posts SET created_at=? WHERE id=?`,
		time.Now().Add(time.Minute).Unix(), later.ID); err != nil {
		t.Fatal(err)
	}
	if got, err = s.ForumFeed(ctx, "watcher", "", 10, true); err != nil {
		t.Fatal(err)
	} else if len(got) != 1 || got[0].ID != later.ID {
		t.Fatalf("feed = %+v, want just the new post", got)
	}

	// The watermark is per board, so a name caught up on the forum is not
	// silently caught up on a board it has never read.
	if got, err = s.ForumFeed(ctx, "watcher", "ops", 10, false); err != nil {
		t.Fatal(err)
	} else if len(got) != 2 {
		t.Errorf("per-board feed = %d posts, want both", len(got))
	}
}

// Two agents sharing a name, or a feed read out of order, must not resurface
// posts that were already handled.
func TestForumMarkReadNeverMovesBackwards(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	now := time.Now().Unix()
	if err := s.ForumMarkRead(ctx, "watcher", "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ForumMarkRead(ctx, "watcher", "", now-3600); err != nil {
		t.Fatal(err)
	}
	got, err := s.ForumWatermark(ctx, "watcher", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != now {
		t.Errorf("watermark = %d, want it to stay at %d", got, now)
	}
}

func TestForumBoardRemoveRefusesWhilePostsRemain(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	post(t, s, ForumPost{Board: "ops", Author: "raphael", Body: "one"})
	err := s.ForumBoardRemove(ctx, "ops", false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal naming --force", err)
	}
	if err := s.ForumBoardRemove(ctx, "ops", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForumBoard(ctx, "ops"); !errors.Is(err, ErrNotFound) {
		t.Errorf("board survived a forced removal: %v", err)
	}
	posts, err := s.ForumList(ctx, ForumQuery{Deleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 0 {
		t.Errorf("posts outlived their board: %+v", posts)
	}
	// And the index went with them, or search would return ids that no longer
	// resolve.
	if hits, err := s.ForumSearch(ctx, "one", "", 10); err != nil || len(hits) != 0 {
		t.Errorf("search after board removal: hits = %+v, err = %v", hits, err)
	}
}

func TestForumListFiltersByBoardAuthorAndTime(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	old := post(t, s, ForumPost{Board: "ops", Author: "raphael", Body: "old"})
	if _, err := s.db.ExecContext(ctx, `UPDATE forum_posts SET created_at=? WHERE id=?`,
		time.Now().Add(-48*time.Hour).Unix(), old.ID); err != nil {
		t.Fatal(err)
	}
	post(t, s, ForumPost{Board: "growth", Author: "june", Body: "new"})

	cases := []struct {
		name string
		q    ForumQuery
		want int
	}{
		{"all", ForumQuery{Parent: "-"}, 2},
		{"board", ForumQuery{Parent: "-", Board: "ops"}, 1},
		{"author", ForumQuery{Parent: "-", Author: "june"}, 1},
		{"since", ForumQuery{Parent: "-", Since: time.Now().Add(-time.Hour).Unix()}, 1},
		{"limit", ForumQuery{Parent: "-", Limit: 1}, 1},
	}
	for _, c := range cases {
		got, err := s.ForumList(ctx, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: %d posts, want %d", c.name, len(got), c.want)
		}
	}
}

func TestNormalizeBoardAndAuthor(t *testing.T) {
	cases := []struct{ in, board string }{
		{"  Ops Notes  ", "ops-notes"},
		{"Ops/Sub.Board", "ops-sub-board"},
		{"!!!", ""},
		{strings.Repeat("a", 60), strings.Repeat("a", 48)},
	}
	for _, c := range cases {
		if got := NormalizeBoard(c.in); got != c.board {
			t.Errorf("NormalizeBoard(%q) = %q, want %q", c.in, got, c.board)
		}
	}
	if got := NormalizeAuthor("  the   watcher "); got != "the-watcher" {
		t.Errorf("NormalizeAuthor = %q", got)
	}
}
