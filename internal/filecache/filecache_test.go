package filecache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// read lists dir through c and returns what each file parsed to, the way the
// readers that use this package do.
func read(t *testing.T, c *Cache[string], dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Listing(dir, entries)
	out := map[string]string{}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		v, err := c.Get(path, info, func() (string, error) {
			b, err := os.ReadFile(path)
			return string(b), err
		})
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = v
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnchangedFilesAreNotParsedAgain(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.md"), "alpha")
	write(t, filepath.Join(dir, "b.md"), "beta")
	c := New[string]()
	read(t, c, dir)
	if c.Parses() != 2 {
		t.Fatalf("first read parsed %d, want 2", c.Parses())
	}
	got := read(t, c, dir)
	if c.Parses() != 2 || got["a.md"] != "alpha" || got["b.md"] != "beta" {
		t.Fatalf("second read parsed %d (want 2) or served %v", c.Parses(), got)
	}
}

func TestAChangedFileIsParsedAgain(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.md")
	write(t, a, "alpha")
	write(t, filepath.Join(dir, "b.md"), "beta")
	c := New[string]()
	read(t, c, dir)
	write(t, a, "alpha, edited")
	got := read(t, c, dir)
	if c.Parses() != 3 || got["a.md"] != "alpha, edited" {
		t.Fatalf("edit: parses %d (want 3), served %q", c.Parses(), got["a.md"])
	}
	// Same size, later time: still a change.
	later := time.Now().Add(time.Minute)
	write(t, a, "ALPHA, EDITED")
	if err := os.Chtimes(a, later, later); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c, dir); got["a.md"] != "ALPHA, EDITED" {
		t.Fatalf("same-size edit served %q", got["a.md"])
	}
}

func TestAListingChangeRereadsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.md"), "alpha")
	write(t, filepath.Join(dir, "b.md"), "beta")
	c := New[string]()
	read(t, c, dir)
	write(t, filepath.Join(dir, "c.md"), "gamma")
	got := read(t, c, dir)
	if c.Parses() != 5 || got["c.md"] != "gamma" {
		t.Fatalf("new file: parses %d (want 5: the directory read in full), served %v", c.Parses(), got)
	}
	if err := os.Remove(filepath.Join(dir, "a.md")); err != nil {
		t.Fatal(err)
	}
	got = read(t, c, dir)
	if _, ok := got["a.md"]; ok || len(got) != 2 {
		t.Fatalf("deleted file still served: %v", got)
	}
	c.mu.Lock()
	_, kept := c.files[filepath.Join(dir, "a.md")]
	c.mu.Unlock()
	if kept {
		t.Fatal("deleted file still held in the cache")
	}
}

func TestParseErrorsAreTriedAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.md")
	write(t, path, "bad")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	c := New[string]()
	// A failed read — a permission, an I/O error — can clear without anything
	// a stat compares changing, so a failure is never kept.
	bad := errors.New("will not read")
	for range 3 {
		if _, err := c.Get(path, info, func() (string, error) { return "", bad }); !errors.Is(err, bad) {
			t.Fatalf("got %v, want the read error", err)
		}
	}
	if c.Parses() != 3 {
		t.Fatalf("an unreadable, unchanged file was tried %d times over three reads, want 3", c.Parses())
	}
	got, err := c.Get(path, info, func() (string, error) { return "repaired", nil })
	if err != nil || got != "repaired" {
		t.Fatalf("after the failure cleared: %q, %v", got, err)
	}
}

// Size and mtime are not enough: a restore that keeps the timestamp of an
// equal-size file must still read as a change.
func TestAnEditThatKeepsSizeAndMtimeIsParsedAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	write(t, path, "alpha")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	c := New[string]()
	read(t, c, dir)
	time.Sleep(10 * time.Millisecond)
	write(t, path, "bravo")
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := read(t, c, dir); got["a.md"] != "bravo" {
		t.Fatalf("equal-size edit with the old mtime served %q", got["a.md"])
	}
}

func TestNilCacheParsesEveryTime(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.md"), "alpha")
	var c *Cache[string]
	calls := 0
	entries, _ := os.ReadDir(dir)
	c.Listing(dir, entries)
	info, _ := os.Stat(filepath.Join(dir, "a.md"))
	for range 2 {
		_, _ = c.Get(filepath.Join(dir, "a.md"), info, func() (string, error) { calls++; return "", nil })
	}
	if calls != 2 || c.Parses() != 0 {
		t.Fatalf("nil cache: %d calls, %d parses; want 2 and 0", calls, c.Parses())
	}
}

// within decides what a listing change forgets, so every way a directory can
// be spelled has to agree with the joined paths files are cached under.
func TestWithinAgreesAcrossSpellings(t *testing.T) {
	for _, tc := range []struct {
		dir, p string
		want   bool
	}{
		{"/", "/vault/a.md", true},
		{"/", "/", false},
		{"/vault", "/vault/a.md", true},
		{"/vault", "/vault/notes/a.md", true},
		{"/vault", "/vault", false},
		{"/vault/", "/vault/a.md", true},
		{"/vault/", "/vault", false},
		{"/vault", "/a.md", false},
		{".", "a.md", true},
		{".", "notes/a.md", true},
		{".", ".", false},
		{".", "../a.md", false},
		{"./", "a.md", true},
		{"", "a.md", true},
		{"notes", "notes/a.md", true},
		{"notes", "notes2/a.md", false},
		{"notes", "a.md", false},
		{"child/..", "a.md", true},
		{"child/..", "child/a.md", true},
		{"/a/notes", "/a/notes2/a.md", false},
		{"/a/notes", "/a/notes2", false},
		{"/a/notes", "/a/notes/x/a.md", true},
		{"/vault", "vault/a.md", false}, // absolute against relative never matches
	} {
		dir, p := filepath.FromSlash(tc.dir), filepath.FromSlash(tc.p)
		if got := within(dir, p); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.dir, tc.p, got, tc.want)
		}
	}
}
