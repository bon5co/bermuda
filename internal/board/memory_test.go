package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v2/internal/index"
	"github.com/bon5co/bermuda/v2/internal/memory"
)

// The MEMORY tab is the one pane with no rows, so the assertions here are on
// rendered output rather than on model state — the same exception the tab-order
// test makes, and for the same reason: there is no other evidence it worked.

func TestMemoryTabCountsWhatIsInTheNotesDirectory(t *testing.T) {
	m := newTestModel(t)
	m.memory = memory.Stats{
		Dir: "/home/someone/.bermuda/memory", Present: true,
		HasIndex: true, Entries: 4, Notes: 12, Archived: 3, Links: 27, Bytes: 8 * 1024,
	}
	m.focus = focusMemory

	body := m.listPane().body
	for _, want := range []string{"/home/someone/.bermuda/memory", "12", "3", "27", "8.0 kB", memory.IndexName} {
		if !strings.Contains(body, want) {
			t.Errorf("memory pane does not show %q:\n%s", want, body)
		}
	}
}

// A directory that was never initialised and one that is merely empty need
// different things done to them, so the pane must not render them alike.
func TestMemoryTabTellsAbsentApartFromEmpty(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusMemory

	m.memory = memory.Stats{Dir: "/nowhere", Present: false}
	absent := m.listPane().body
	if !strings.Contains(absent, "memory init") {
		t.Errorf("an uninitialised memory does not say how to initialise it:\n%s", absent)
	}

	m.memory = memory.Stats{Dir: "/nowhere", Present: true}
	empty := m.listPane().body
	if strings.Contains(empty, "memory init") {
		t.Errorf("an initialised but empty memory was told to initialise itself:\n%s", empty)
	}
}

// `memory init --vault` makes the notes directory a symlink, which is the
// normal wiring rather than an oddity. A reader shown only the link has not
// been told which vault their agents are writing into.
func TestMemoryTabNamesTheVaultBehindASymlink(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusMemory
	m.memory = memory.Stats{
		Dir: "/home/someone/.bermuda/memory", LinkedTo: "/home/someone/vault/facts",
		Present: true, Notes: 2,
	}
	if body := m.listPane().body; !strings.Contains(body, "/home/someone/vault/facts") {
		t.Errorf("the symlink target is not shown:\n%s", body)
	}
}

// An index-less memory works for a human and is useless to an agent, which
// loads the index and nothing else.
func TestMemoryTabSaysWhenTheIndexIsMissing(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusMemory
	m.memory = memory.Stats{Dir: "/d", Present: true, Notes: 5}
	if body := m.listPane().body; !strings.Contains(body, "missing") {
		t.Errorf("a memory with notes but no index does not flag it:\n%s", body)
	}
}

// Nothing on this tab is selectable, so the keys that open a row must do
// nothing rather than open a row belonging to whichever tab was left.
func TestMemoryTabHasNothingToOpen(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusRuns
	m.cursor = 1
	m.press(t, "6")

	if m.focus != focusMemory {
		t.Fatalf("6 opened focus %d, want the memory tab", m.focus)
	}
	if m.cursor != 0 {
		t.Errorf("cursor is %d, want 0: the old index means nothing here", m.cursor)
	}
	if cmd := m.descend(); cmd != nil {
		t.Error("l/→ on the memory tab returned a command, want nothing to open")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")}); cmd != nil {
		t.Error("l on the memory tab did something")
	}
}

// The tab reports on the directory the command layer resolved, not on one it
// worked out for itself — otherwise it can describe a directory the agents do
// not write to.
func TestMemoryTabReadsTheDirectoryItWasGiven(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a-fact.md"), []byte("true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(t)
	m.deps.MemoryDir = dir
	m.apply(t, m.load()())

	if m.memory.Notes != 1 || m.memory.Dir != dir {
		t.Errorf("memory read as %+v, want one note under %s", m.memory, dir)
	}
}

// The search index is the second half of the memory layer, and the questions a
// reader has about it are the same three every time: can I search this, is it
// being kept up, and what does keeping it up cost.
func TestMemoryTabShowsWhatTheSearchIndexHolds(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusMemory
	m.memory = memory.Stats{Dir: "/vault/memory", Present: true, HasIndex: true, Notes: 12}
	m.glance = index.Glance{
		Present: true,
		Summary: index.Summary{Files: 568, Chunks: 5056},
		Swept:   time.Now().Add(-42 * time.Second), Took: 31 * time.Millisecond, Seen: 568,
		Wrote:      time.Now().Add(-18 * time.Minute),
		WroteNotes: 4, WroteChunks: 12, WroteTook: 1400 * time.Millisecond,
	}

	body := m.listPane().body
	for _, want := range []string{"5,056 chunk(s)", "568 note(s)", "31ms", "4 note(s), 12 chunk(s)", "1.4s"} {
		if !strings.Contains(body, want) {
			t.Errorf("memory pane does not show %q:\n%s", want, body)
		}
	}
}

// Never indexed is the state of every fresh install, so it has to read as an
// instruction rather than as a zero.
func TestMemoryTabSaysHowToIndexWhenNothingIsIndexed(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusMemory
	m.memory = memory.Stats{Dir: "/vault/memory", Present: true, HasIndex: true, Notes: 12}
	m.glance = index.Glance{}

	body := m.listPane().body
	if !strings.Contains(body, "bermuda memory index") {
		t.Errorf("an unindexed vault does not say how to index it:\n%s", body)
	}
}

// A sweep that found nothing is the common case and still proves the index is
// alive; the last sweep that wrote something is what says how current the
// contents are. One timestamp cannot answer both.
func TestMemoryTabSeparatesTheLastSweepFromTheLastWrite(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusMemory
	m.memory = memory.Stats{Dir: "/vault/memory", Present: true, HasIndex: true, Notes: 12}
	m.glance = index.Glance{
		Present: true,
		Summary: index.Summary{Files: 10, Chunks: 40},
		Swept:   time.Now().Add(-3 * time.Second), Took: 12 * time.Millisecond,
	}
	body := m.listPane().body
	if !strings.Contains(body, "swept") {
		t.Errorf("a sweep with nothing to do was not reported:\n%s", body)
	}
	if strings.Contains(body, "indexed  ") {
		t.Errorf("an index that has never written anything claimed a write:\n%s", body)
	}
}

func TestTookReadsAsMillisecondsUntilItIsWorthSeconds(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "—",
		31 * time.Millisecond:   "31ms",
		999 * time.Millisecond:  "999ms",
		1400 * time.Millisecond: "1.4s",
	}
	for d, want := range cases {
		if got := took(d); got != want {
			t.Errorf("took(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestThousandsGroupsCountsTheEyeCanRead(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 5056: "5,056", 1234567: "1,234,567"}
	for n, want := range cases {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}
