package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func paths(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func TestScanTakesMarkdownAndLeavesEverythingElse(t *testing.T) {
	root := t.TempDir()
	write(t, root, "memory/goals.md", "goals")
	write(t, root, "MEMORY.md", "index")
	write(t, root, "assets/diagram.png", "not markdown")
	write(t, root, ".obsidian/workspace.json", "{}")
	write(t, root, ".obsidian/plugins/a/README.md", "a plugin's own docs")
	write(t, root, "node_modules/pkg/readme.md", "someone's dependency")

	files, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(paths(files), ",")
	if got != "MEMORY.md,memory/goals.md" {
		t.Errorf("scanned %q, want only the vault's own markdown", got)
	}
}

func TestScanHashesEveryFileItTakes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "content")
	files, _ := Scan(root)
	if len(files) != 1 || files[0].Digest != Digest([]byte("content")) {
		t.Fatalf("digest missing or wrong: %#v", files)
	}
}

// An oversized note is reported as skipped, not dropped: "not indexed" and
// "too big to index" are different answers to "why did search not find it".
func TestScanSkipsAnOversizedNoteWithAReason(t *testing.T) {
	root := t.TempDir()
	write(t, root, "huge.md", strings.Repeat("x", maxFileBytes+1))
	files, _ := Scan(root)
	if len(files) != 1 {
		t.Fatalf("got %d files", len(files))
	}
	if files[0].Skipped == "" {
		t.Error("an oversized note was indexed")
	}
	if files[0].Digest != "" {
		t.Error("an oversized note was hashed anyway")
	}
}

func TestSectionIsTheTopFolder(t *testing.T) {
	if got := (File{Path: "memory/archive/old.md"}).Section(); got != "memory" {
		t.Errorf("Section = %q", got)
	}
	if got := (File{Path: "MEMORY.md"}).Section(); got != "" {
		t.Errorf("Section = %q, want empty at the vault root", got)
	}
}

func TestCompareSortsFilesIntoNewChangedAndUnchanged(t *testing.T) {
	files := []File{
		{Path: "a.md", Digest: "1"},
		{Path: "b.md", Digest: "2"},
		{Path: "c.md", Digest: "3"},
	}
	plan := Compare(files, map[string]string{"b.md": "old", "c.md": "3"})
	if len(plan.New) != 1 || plan.New[0].Path != "a.md" {
		t.Errorf("New = %v", paths(plan.New))
	}
	if len(plan.Changed) != 1 || plan.Changed[0].Path != "b.md" {
		t.Errorf("Changed = %v", paths(plan.Changed))
	}
	if len(plan.Unchanged) != 1 || plan.Unchanged[0].Path != "c.md" {
		t.Errorf("Unchanged = %v", paths(plan.Unchanged))
	}
	if plan.Empty() {
		t.Error("a plan with work in it reported itself empty")
	}
}

// A note deleted from the vault has to leave the collection, or a search keeps
// answering with a fact the vault no longer holds.
func TestCompareNoticesAFileThatIsGone(t *testing.T) {
	plan := Compare(nil, map[string]string{"gone.md": "1"})
	if len(plan.Gone) != 1 || plan.Gone[0] != "gone.md" {
		t.Errorf("Gone = %v", plan.Gone)
	}
	if plan.Empty() {
		t.Error("a deletion is work, and the plan called itself empty")
	}
}

// A file that was indexed and has since grown past the limit must be removed
// from the collection, not left there for ever as the last version that fit.
func TestCompareRemovesAnIndexedFileThatBecameTooBig(t *testing.T) {
	files := []File{{Path: "huge.md", Skipped: "larger than 2 MiB"}}
	plan := Compare(files, map[string]string{"huge.md": "1"})
	if len(plan.Gone) != 1 || plan.Gone[0] != "huge.md" {
		t.Errorf("Gone = %v, want the now-oversized file removed", plan.Gone)
	}
	if len(plan.Skipped) != 1 {
		t.Errorf("Skipped = %v", paths(plan.Skipped))
	}
}

// The common case, and the one that decides whether this is cheap enough to
// run from the daemon: nothing changed, so no helper process may start.
func TestComparePlanIsEmptyWhenNothingChanged(t *testing.T) {
	files := []File{{Path: "a.md", Digest: "1"}}
	if !Compare(files, map[string]string{"a.md": "1"}).Empty() {
		t.Error("an unchanged vault produced work")
	}
}

func TestResolveRootPrefersTheEnvironmentOverride(t *testing.T) {
	t.Setenv("BERMUDA_INDEX_ROOT", "/somewhere/else")
	if got := ResolveRoot("/state/memory"); got != "/somewhere/else" {
		t.Errorf("ResolveRoot = %q", got)
	}
}

// Memory is usually a symlink into a vault, and the whole vault is what is
// worth searching -- the notes nobody loads every session most of all.
func TestResolveRootClimbsToTheVaultThatHoldsTheMemoryFolder(t *testing.T) {
	t.Setenv("BERMUDA_INDEX_ROOT", "")
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(vault, "memory")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "memory")
	if err := os.Symlink(notes, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want, err := filepath.EvalSymlinks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveRoot(link); got != want {
		t.Errorf("ResolveRoot = %q, want the vault root %q", got, want)
	}
}

// An install with no vault indexes its own memory directory, rather than
// climbing to the home directory and indexing everything on the machine.
func TestResolveRootStopsAtTheMemoryDirWhenThereIsNoVault(t *testing.T) {
	t.Setenv("BERMUDA_INDEX_ROOT", "")
	dir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got := ResolveRoot(dir); got != want {
		t.Errorf("ResolveRoot = %q, want %q", got, want)
	}
}

// A vault is user content. A note that is really a symlink to something
// outside the vault must not be read and put in a searchable store -- a
// private key indexed under whatever folder somebody dropped the link in.
func TestScanRefusesANoteThatPointsOutsideTheVault(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("a private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write(t, root, "real.md", "an ordinary note, long enough to be worth indexing")
	if err := os.Symlink(outside, filepath.Join(root, "escape.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == "escape.md" {
			if f.Digest != "" {
				t.Error("a note pointing outside the vault was indexed")
			}
			if f.Skipped == "" {
				t.Error("the refusal was not reported")
			}
		}
	}
	// And the rest of the vault is unaffected.
	var indexed int
	for _, f := range files {
		if f.Digest != "" {
			indexed++
		}
	}
	if indexed != 1 {
		t.Errorf("indexed %d file(s), want only the real note", indexed)
	}
}
