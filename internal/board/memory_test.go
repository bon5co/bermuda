package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
