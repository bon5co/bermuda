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
