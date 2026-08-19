package store

import (
	"context"
	"strings"
	"testing"
)

// The forum's board table and its search index are the two places where a
// silent wrong answer costs the most: a board row that duplicates instead of
// updating splits one board into several, and an index that drifts from the
// posts table makes a post that exists unfindable. Neither shows up as an
// error — the caller gets a plausible, wrong answer — so both are asserted
// here against a real database.

func TestForumBoardPutDeclaresABoardAndThenEditsIt(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()

	b, err := s.ForumBoardPut(ctx, "Ops Notes", "  what broke and why  ")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "ops-notes" {
		t.Errorf("name = %q, want ops-notes", b.Name)
	}
	if b.Description != "what broke and why" {
		t.Errorf("description = %q, want it trimmed", b.Description)
	}
	if b.Posts != 0 || b.Threads != 0 {
		t.Errorf("a declared board starts empty, got %d posts / %d threads", b.Posts, b.Threads)
	}

	// Putting the same board again is an edit, not a second board: the name is
	// the identity, so an agent that re-declares a board it already made must
	// not end up with two of them.
	again, err := s.ForumBoardPut(ctx, "ops-notes", "incidents only")
	if err != nil {
		t.Fatal(err)
	}
	if again.Description != "incidents only" {
		t.Errorf("description = %q, want the new one", again.Description)
	}
	boards, err := s.ForumBoards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 1 {
		t.Fatalf("boards = %d, want the put to have updated one board, not added one", len(boards))
	}
}

func TestForumBoardPutKeepsThePostsAlreadyOnTheBoard(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	root := post(t, s, ForumPost{Board: "ops", Author: "raphael", Title: "disk full", Body: "cleared logs"})
	post(t, s, ForumPost{Board: "ops", Author: "sentinel", ParentID: root.ID, Body: "still full"})

	b, err := s.ForumBoardPut(ctx, "ops", "incidents")
	if err != nil {
		t.Fatal(err)
	}
	// Describing a board must not disturb what is on it — the upsert touches
	// the description column and nothing else.
	if b.Posts != 2 || b.Threads != 1 {
		t.Errorf("board = %d posts / %d threads, want 2 / 1", b.Posts, b.Threads)
	}
	if b.LastAt == 0 {
		t.Error("last activity = 0, want the reply's timestamp")
	}
}

func TestForumBoardPutRejectsANameThatNormalizesToNothing(t *testing.T) {
	s := newForum(t)
	for _, name := range []string{"", "   ", "!!!", "///"} {
		if _, err := s.ForumBoardPut(context.Background(), name, "d"); err == nil {
			t.Errorf("ForumBoardPut(%q) succeeded, want an error rather than a board with no name", name)
		}
	}
}

func TestForumReindexRebuildsSearchFromThePostsTable(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	if !s.ForumSearchable() {
		t.Skip("this build has no FTS5")
	}
	live := post(t, s, ForumPost{Board: "ops", Author: "raphael", Title: "railway", Body: "templatepublish worked"})
	gone := post(t, s, ForumPost{Board: "ops", Author: "raphael", Title: "obsolete", Body: "templatepublish failed"})
	if err := s.ForumDelete(ctx, gone.ID, "raphael"); err != nil {
		t.Fatal(err)
	}

	// Simulate the case reindex exists for: posts written by a build without
	// FTS5, so the index is empty while the posts table is not.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM forum_fts`); err != nil {
		t.Fatal(err)
	}
	if hits := search(t, s, "templatepublish"); len(hits) != 0 {
		t.Fatalf("search before reindex = %d hits, want the emptied index to find nothing", len(hits))
	}

	n, err := s.ForumReindex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A deleted post keeps its row but must not come back into the index: a
	// tombstone that is searchable serves a body the author retracted.
	if n != 1 {
		t.Errorf("reindexed %d posts, want 1 live post and no tombstone", n)
	}
	hits := search(t, s, "templatepublish")
	if len(hits) != 1 || hits[0].ID != live.ID {
		t.Fatalf("search after reindex = %+v, want only %s", hitIDs(hits), live.ID)
	}
}

func TestForumReindexIsRepeatableWithoutDuplicating(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	if !s.ForumSearchable() {
		t.Skip("this build has no FTS5")
	}
	post(t, s, ForumPost{Board: "ops", Author: "raphael", Title: "note", Body: "flockwithretry"})

	for i := range 3 {
		n, err := s.ForumReindex(ctx)
		if err != nil {
			t.Fatalf("reindex %d: %v", i, err)
		}
		if n != 1 {
			t.Errorf("reindex %d returned %d, want 1", i, n)
		}
		// Reindexing clears the index first, so running it twice must not
		// return the same post twice from one search.
		if hits := search(t, s, "flockwithretry"); len(hits) != 1 {
			t.Fatalf("after reindex %d, search = %d hits, want 1", i, len(hits))
		}
	}
}

func TestForumUpdateLeavesTheFieldsTheCallerDidNotSend(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	p := post(t, s, ForumPost{
		Board: "ops", Author: "raphael", Title: "keep me", Body: "old body\n\n", Meta: `{"a":1}`,
	})

	// A body-only edit is the common case, and it must not blank the title.
	got, err := s.ForumUpdate(ctx, p.ID, "raphael", "", "new body", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "keep me" {
		t.Errorf("title = %q, want it untouched by a body edit", got.Title)
	}
	if got.Body != "new body" {
		t.Errorf("body = %q, want the new one", got.Body)
	}
	if got.Meta != `{"a":1}` {
		t.Errorf("meta = %q, want it untouched", got.Meta)
	}
	if got.UpdatedAt < p.CreatedAt {
		t.Errorf("updated_at = %d, want it at or after created_at %d", got.UpdatedAt, p.CreatedAt)
	}

	// The edit is what a later reader gets, not just what this call returned.
	back, err := s.ForumGet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Body != "new body" || back.Title != "keep me" {
		t.Errorf("read back %q / %q, want \"keep me\" / \"new body\"", back.Title, back.Body)
	}
}

func TestForumUpdateRejectsMetaThatIsNotJSON(t *testing.T) {
	s := newForum(t)
	ctx := context.Background()
	p := post(t, s, ForumPost{Board: "ops", Author: "raphael", Title: "t", Body: "b", Meta: `{"a":1}`})

	if _, err := s.ForumUpdate(ctx, p.ID, "raphael", "", "", "not json"); err == nil {
		t.Fatal("ForumUpdate accepted invalid meta, want an error")
	}
	back, err := s.ForumGet(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Meta != `{"a":1}` {
		t.Errorf("meta = %q, want the rejected edit to have changed nothing", back.Meta)
	}
}

func search(t *testing.T, s *Store, q string) []ForumHit {
	t.Helper()
	hits, err := s.ForumSearch(context.Background(), q, "", 25)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func hitIDs(hits []ForumHit) string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return "[" + strings.Join(out, " ") + "]"
}
