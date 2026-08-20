package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirPrefersTheOverrideOverTheStateDirectory(t *testing.T) {
	t.Setenv("BERMUDA_MEMORY_DIR", "")
	if got, want := Dir("/state"), filepath.Join("/state", "memory"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}

	t.Setenv("BERMUDA_MEMORY_DIR", "/vault/facts")
	if got := Dir("/state"); got != "/vault/facts" {
		t.Errorf("Dir = %q, want the override to win", got)
	}
}

func TestReadSaysWhenMemoryWasNeverInitialised(t *testing.T) {
	st := Read(filepath.Join(t.TempDir(), "memory"))
	if st.Present {
		t.Fatal("a directory that does not exist came back Present")
	}
	if st.Notes != 0 || st.HasIndex {
		t.Errorf("absent memory reported content: %+v", st)
	}
}

// An initialised but empty memory is a different state from an absent one —
// one needs `memory init`, the other needs an agent to write something down.
func TestReadDistinguishesEmptyFromAbsent(t *testing.T) {
	dir := t.TempDir()
	st := Read(dir)
	if !st.Present {
		t.Fatal("an existing directory came back absent")
	}
	if st.Notes != 0 {
		t.Errorf("notes = %d, want 0", st.Notes)
	}
}

func TestReadCountsNotesLinksAndTheArchiveSeparately(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, IndexName), "Index of memory notes.\n\n- [a](a.md)\n- [b](b.md)\n")
	write(t, filepath.Join(dir, "a.md"), "fact one, see [[b]] and [[c]]\n")
	write(t, filepath.Join(dir, "b.md"), "fact two, see [[a]]\n")
	write(t, filepath.Join(dir, "screenshot.png"), "not a note")
	write(t, filepath.Join(dir, "archive", "old.md"), "resolved\n")
	write(t, filepath.Join(dir, "archive", "older.md"), "resolved\n")

	st := Read(dir)

	if st.Notes != 2 {
		t.Errorf("notes = %d, want 2: the index and the attachment are not facts", st.Notes)
	}
	if st.Archived != 2 {
		t.Errorf("archived = %d, want 2", st.Archived)
	}
	if st.Links != 3 {
		t.Errorf("links = %d, want 3", st.Links)
	}
	if !st.HasIndex {
		t.Error("index present but not reported")
	}
	if st.Entries != 3 {
		t.Errorf("entries = %d, want 3 non-blank index lines", st.Entries)
	}
	if st.Bytes == 0 {
		t.Error("bytes = 0, want the size of the two notes")
	}
}

func TestReadNamesTheMostRecentlyWrittenNote(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "old.md"), "older\n")
	write(t, filepath.Join(dir, "fresh.md"), "newer\n")

	// Set the times explicitly: two files written in the same millisecond
	// would otherwise make the winner a coin toss.
	old := filepath.Join(dir, "old.md")
	if err := os.Chtimes(old, time.Unix(1_000_000, 0), time.Unix(1_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh.md")
	if err := os.Chtimes(fresh, time.Unix(2_000_000, 0), time.Unix(2_000_000, 0)); err != nil {
		t.Fatal(err)
	}

	if got := Read(dir).Newest; got != "fresh" {
		t.Errorf("newest = %q, want %q", got, "fresh")
	}
}

// The vault wiring is the common setup on a real machine, and a symlinked
// directory has to read exactly like a real one or the tab lies about where
// the notes are.
func TestReadFollowsAVaultSymlinkAndReportsTheTarget(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	write(t, filepath.Join(vault, "one.md"), "fact\n")

	link := filepath.Join(root, "memory")
	if err := os.Symlink(vault, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	st := Read(link)
	if !st.Present || st.Notes != 1 {
		t.Errorf("symlinked memory read as %+v, want one note through the link", st)
	}
	if st.LinkedTo != vault {
		t.Errorf("linkedTo = %q, want %q", st.LinkedTo, vault)
	}
}

// A memory directory is normally a whole vault, and a vault files its notes in
// folders. Counting only the top level reported eight notes for a vault holding
// five hundred, which reads as an empty harness rather than a full one.
func TestReadCountsNotesInSubdirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, IndexName), "index\n")
	write(t, filepath.Join(dir, "loose.md"), "at the root\n")
	write(t, filepath.Join(dir, "memory", "a-fact.md"), "see [[loose]]\n")
	write(t, filepath.Join(dir, "projects", "deep", "b-fact.md"), "nested two down\n")
	write(t, filepath.Join(dir, "projects", "diagram.png"), "not a note")

	st := Read(dir)

	if st.Notes != 3 {
		t.Errorf("notes = %d, want 3: the whole tree counts, the attachment does not", st.Notes)
	}
	if st.Links != 1 {
		t.Errorf("links = %d, want 1 from the nested note", st.Links)
	}
}

// An index is an index at any depth: a vault wired as the memory directory has
// its own MEMORY.md at the root and another inside memory/, and neither is a
// fact somebody wrote down.
func TestReadNeverCountsAnIndexAsANote(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, IndexName), "root index\n")
	write(t, filepath.Join(dir, "memory", IndexName), "inner index\n")
	write(t, filepath.Join(dir, "memory", "fact.md"), "the only fact\n")

	if got := Read(dir).Notes; got != 1 {
		t.Errorf("notes = %d, want 1", got)
	}
}

// Archives live per section rather than once at the root, so the split between
// live and resolved has to hold at any depth.
func TestReadCountsNestedArchivesAsArchived(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "live.md"), "current\n")
	write(t, filepath.Join(dir, "memory", "archive", "old.md"), "resolved\n")
	write(t, filepath.Join(dir, "tasks", "archive", "done.md"), "resolved\n")

	st := Read(dir)

	if st.Notes != 1 {
		t.Errorf("notes = %d, want 1: archived notes are not live", st.Notes)
	}
	if st.Archived != 2 {
		t.Errorf("archived = %d, want 2 across both archive folders", st.Archived)
	}
}

// A vault's dot directories are its own machinery — .obsidian holds plugin
// data, .trash holds deletions — and counting either would inflate the number
// the tab exists to report.
func TestReadSkipsTheVaultsOwnDotDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "fact.md"), "real\n")
	write(t, filepath.Join(dir, ".obsidian", "plugins", "notes.md"), "plugin data\n")
	write(t, filepath.Join(dir, ".trash", "deleted.md"), "thrown away\n")

	if got := Read(dir).Notes; got != 1 {
		t.Errorf("notes = %d, want 1: dot directories are not memory", got)
	}
}

// Newest names a note so a reader can go open it, which for a nested note
// means the path they would type and not just the basename.
func TestReadNamesTheNewestNoteByItsPath(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "old.md"), "older\n")
	write(t, filepath.Join(dir, "projects", "fresh.md"), "newer\n")

	if err := os.Chtimes(filepath.Join(dir, "old.md"), time.Unix(1_000_000, 0), time.Unix(1_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "projects", "fresh.md"), time.Unix(2_000_000, 0), time.Unix(2_000_000, 0)); err != nil {
		t.Fatal(err)
	}

	if got, want := Read(dir).Newest, filepath.Join("projects", "fresh"); got != want {
		t.Errorf("newest = %q, want %q", got, want)
	}
}
