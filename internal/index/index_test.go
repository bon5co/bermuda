package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// client builds a client over a fresh vault and index directory, with the fake
// helper in place of Python.
func client(t *testing.T, response string) (*Client, string, string) {
	t.Helper()
	_, log := fakeHelper(t, response)
	root := t.TempDir()
	return New(filepath.Join(t.TempDir(), "index"), root), root, log
}

const ok = `{"ok":true}`

// The guarantee that makes this safe to run from the daemon every five
// minutes: an unchanged vault starts no process at all.
func TestSweepStartsNoHelperWhenNothingChanged(t *testing.T) {
	c, root, log := client(t, ok)
	write(t, root, "a.md", "# A\n\nSomething long enough to be worth indexing on its own.\n")
	if _, err := c.Sweep(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	first := len(requests(t, log))
	if first == 0 {
		t.Fatal("the first sweep indexed nothing")
	}
	rep, err := c.Sweep(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(requests(t, log)); got != first {
		t.Errorf("a second sweep made %d more request(s), want none", got-first)
	}
	if rep.Indexed != 0 || rep.Unchanged != 1 {
		t.Errorf("report = %+v", rep)
	}
}

func TestSweepIndexesNewNotesAndCountsTheirChunks(t *testing.T) {
	c, root, log := client(t, ok)
	write(t, root, "memory/goals.md", "---\nname: goals\n---\n\n# Money\n\nFinancial independence is the standing objective here.\n")
	rep, err := c.Sweep(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.New != 1 || rep.Indexed != 1 || rep.Chunks == 0 {
		t.Fatalf("report = %+v", rep)
	}
	req := strings.Join(requests(t, log), "\n")
	if !strings.Contains(req, `"id":"memory/goals.md#0"`) {
		t.Errorf("the chunk id is not the path and ordinal: %s", req)
	}
	if !strings.Contains(req, `"section":"memory"`) {
		t.Errorf("the section metadata is missing: %s", req)
	}
}

// A note that lost half its paragraphs must not leave them searchable, so a
// reindex deletes by path before it writes.
func TestSweepDeletesByPathBeforeReindexingAChangedNote(t *testing.T) {
	c, root, log := client(t, ok)
	write(t, root, "a.md", "# A\n\nThe first version of this note, long enough to index.\n")
	if _, err := c.Sweep(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.md", "# A\n\nA second version, which says something else entirely.\n")
	rep, err := c.Sweep(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	last := requests(t, log)
	if len(last) < 2 {
		t.Fatalf("got %d requests", len(last))
	}
	if !strings.Contains(last[len(last)-1], `"delete_paths":["a.md"]`) {
		t.Errorf("the reindex did not delete the old chunks: %s", last[len(last)-1])
	}
}

// A deleted note has to leave the collection, or search keeps answering with a
// fact the vault no longer holds.
func TestSweepRemovesANoteThatIsGoneFromTheVault(t *testing.T) {
	c, root, log := client(t, ok)
	p := write(t, root, "gone.md", "# Gone\n\nThis note is about to be deleted from the vault.\n")
	if _, err := c.Sweep(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Sweep(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	reqs := requests(t, log)
	if !strings.Contains(reqs[len(reqs)-1], `"delete_paths":["gone.md"]`) {
		t.Errorf("the deletion was not sent: %s", reqs[len(reqs)-1])
	}
	// And it is forgotten, so the next sweep has nothing to do.
	rep, err = c.Sweep(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 0 {
		t.Errorf("the deletion was sent twice: %+v", rep)
	}
}

func TestSweepRebuildDropsTheCollectionFirst(t *testing.T) {
	c, root, log := client(t, ok)
	write(t, root, "a.md", "# A\n\nSomething long enough to be worth indexing on its own.\n")
	rep, err := c.Sweep(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Rebuilt {
		t.Error("the report does not say it rebuilt")
	}
	if !strings.Contains(requests(t, log)[0], `"op":"drop"`) {
		t.Errorf("the collection was not dropped first: %s", requests(t, log)[0])
	}
}

// Pointing the index at a different vault rebuilds without being asked: every
// path recorded describes a file that no longer exists.
func TestSweepRebuildsItselfWhenTheVaultChanges(t *testing.T) {
	c, root, _ := client(t, ok)
	write(t, root, "a.md", "# A\n\nSomething long enough to be worth indexing on its own.\n")
	if _, err := c.Sweep(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	write(t, other, "b.md", "# B\n\nA different vault with a different note inside it.\n")
	moved := New(c.Dir(), other)
	rep, err := moved.Sweep(context.Background(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Rebuilt || rep.Why == "" {
		t.Errorf("report = %+v, want an explained rebuild", rep)
	}
}

// The manifest is written only after the helper accepts the batch, so a
// failure leaves the file stale and re-running the sweep is always the fix.
func TestSweepRecordsNothingWhenTheHelperFails(t *testing.T) {
	c, root, _ := client(t, `{"ok":false,"error":"disk full"}`)
	write(t, root, "a.md", "# A\n\nSomething long enough to be worth indexing on its own.\n")
	if _, err := c.Sweep(context.Background(), false, nil); err == nil {
		t.Fatal("a failing helper reported success")
	}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 0 || st.Stale != 1 {
		t.Errorf("status = %+v, want the note still stale", st)
	}
}

func TestSearchReadsHitsAndScoresThemHigherIsBetter(t *testing.T) {
	c, _, log := client(t, `{"ok":true,"hits":[
{"id":"memory/a.md#1","text":"a\nH\n\nthe paragraph","metadata":{"path":"memory/a.md","title":"a","heading":"H","section":"memory","type":"project","ord":1},"distance":0.25},
{"id":"b.md#0","text":"b\n\nanother","metadata":{"path":"b.md","title":"b","ord":0},"distance":0.75}]}`)
	hits, err := c.Search(context.Background(), "what did we decide", SearchOpts{N: 5, Section: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits", len(hits))
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("hits are not ranked best first: %v", hits)
	}
	if hits[0].Path != "memory/a.md" || hits[0].Heading != "H" || hits[0].Type != "project" {
		t.Errorf("metadata was not carried through: %+v", hits[0])
	}
	req := requests(t, log)[0]
	if !strings.Contains(req, `"section":{"$eq":"memory"}`) {
		t.Errorf("the section filter was not sent: %s", req)
	}
}

func TestSearchRefusesAnEmptyQueryWithoutStartingAnything(t *testing.T) {
	c, _, log := client(t, ok)
	if _, err := c.Search(context.Background(), "   ", SearchOpts{}); err == nil {
		t.Fatal("an empty query was searched for")
	}
	if len(requests(t, log)) != 0 {
		t.Error("an empty query started a helper")
	}
}

// Status is what a person runs when search is not working, so it has to work
// when nothing can run the helper at all.
func TestStatusReportsStalenessWithoutAHelper(t *testing.T) {
	t.Setenv("BERMUDA_INDEX_PYTHON", "")
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	write(t, root, "a.md", "# A\n\nSomething long enough to be worth indexing on its own.\n")
	c := New(filepath.Join(t.TempDir(), "index"), root)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Python != "" {
		t.Errorf("Python = %q, want empty when nothing can run it", st.Python)
	}
	if st.Stale != 1 || st.Files != 0 {
		t.Errorf("status = %+v", st)
	}
}

// The board draws its lines from what the sweep recorded, so a sweep that did
// nothing still has to leave a stamp behind -- otherwise an index being kept
// perfectly up to date is indistinguishable from one nothing has touched in a
// week.
func TestSweepRecordsItselfEvenWhenItHadNothingToDo(t *testing.T) {
	c, root, _ := client(t, ok)
	write(t, root, "a.md", "# A\n\nSomething long enough to be worth indexing on its own.\n")
	if _, err := c.Sweep(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	first := ReadGlance(c.Dir())
	if first.Wrote.IsZero() || first.WroteNotes != 1 {
		t.Fatalf("the first sweep did not record its write: %+v", first)
	}
	if _, err := c.Sweep(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	second := ReadGlance(c.Dir())
	if second.Swept.Before(first.Swept) {
		t.Error("a sweep with nothing to do did not stamp that it ran")
	}
	if second.WroteNotes != 1 {
		t.Errorf("a sweep with nothing to do overwrote the last write: %+v", second)
	}
	if second.Seen != 1 {
		t.Errorf("Seen = %d, want the files it hashed", second.Seen)
	}
}
