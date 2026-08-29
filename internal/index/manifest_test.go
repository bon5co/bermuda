package index

import (
	"path/filepath"
	"testing"
	"time"
)

func openManifest(t *testing.T) *Manifest {
	t.Helper()
	m, err := OpenManifest(filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestManifestRecordsWhatWasIndexedAndAtWhichDigest(t *testing.T) {
	m := openManifest(t)
	f := File{Path: "memory/goals.md", Digest: "abc", Size: 10, Modified: time.Unix(1000, 0)}
	if err := m.Record(f, 3, time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	got, err := m.Digests()
	if err != nil {
		t.Fatal(err)
	}
	if got["memory/goals.md"] != "abc" {
		t.Errorf("digests = %v", got)
	}
	sum, err := m.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 1 || sum.Chunks != 3 {
		t.Errorf("summary = %+v", sum)
	}
}

// Reindexing replaces the row rather than adding one, because the path is the
// identity of a file everywhere in this package.
func TestManifestRecordIsKeyedByPath(t *testing.T) {
	m := openManifest(t)
	f := File{Path: "a.md", Digest: "1"}
	if err := m.Record(f, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.Digest = "2"
	if err := m.Record(f, 5, time.Now()); err != nil {
		t.Fatal(err)
	}
	sum, _ := m.Summary()
	if sum.Files != 1 || sum.Chunks != 5 {
		t.Errorf("summary = %+v, want the row replaced", sum)
	}
	d, _ := m.Digests()
	if d["a.md"] != "2" {
		t.Errorf("digest = %q, want the newer one", d["a.md"])
	}
}

func TestManifestForgetsDeletedPaths(t *testing.T) {
	m := openManifest(t)
	m.Record(File{Path: "a.md", Digest: "1"}, 1, time.Now())
	m.Record(File{Path: "b.md", Digest: "1"}, 1, time.Now())
	if err := m.Forget("a.md"); err != nil {
		t.Fatal(err)
	}
	d, _ := m.Digests()
	if _, still := d["a.md"]; still {
		t.Error("a forgotten path is still recorded")
	}
	if _, gone := d["b.md"]; !gone {
		t.Error("Forget removed a path it was not given")
	}
}

func TestManifestForgetOfNothingIsNotAnError(t *testing.T) {
	if err := openManifest(t).Forget(); err != nil {
		t.Fatal(err)
	}
}

// Pointing the index at a different vault leaves every recorded path
// describing a file that no longer exists.
func TestManifestCallsForARebuildWhenTheVaultChanges(t *testing.T) {
	m := openManifest(t)
	if stale, _, err := m.StaleForRules("/vault/one"); err != nil || stale {
		t.Fatalf("first run asked for a rebuild: %v %v", stale, err)
	}
	if stale, _, err := m.StaleForRules("/vault/one"); err != nil || stale {
		t.Fatalf("the same vault asked for a rebuild: %v %v", stale, err)
	}
	stale, why, err := m.StaleForRules("/vault/two")
	if err != nil {
		t.Fatal(err)
	}
	if !stale || why == "" {
		t.Errorf("a changed vault did not ask for a rebuild: %v %q", stale, why)
	}
}

// Changing how a note is chunked makes every stored vector describe text this
// build would never produce -- and the digests would still match, so nothing
// would reindex. That is the silent staleness this check exists for.
func TestManifestCallsForARebuildWhenTheChunkerChanges(t *testing.T) {
	m := openManifest(t)
	if _, _, err := m.StaleForRules("/vault"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSetting("chunker", "an older version"); err != nil {
		t.Fatal(err)
	}
	stale, why, err := m.StaleForRules("/vault")
	if err != nil {
		t.Fatal(err)
	}
	if !stale || why == "" {
		t.Errorf("a changed chunker did not ask for a rebuild: %v %q", stale, why)
	}
}

func TestManifestResetForgetsEverything(t *testing.T) {
	m := openManifest(t)
	m.Record(File{Path: "a.md", Digest: "1"}, 1, time.Now())
	if err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if sum, _ := m.Summary(); sum.Files != 0 {
		t.Errorf("summary = %+v after a reset", sum)
	}
}

// The board reads this instead of hashing the vault every three seconds, so
// the sweep has to leave behind everything the board wants to say.
func TestGlanceReportsWhatTheLastSweepRecorded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	m, err := OpenManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Record(File{Path: "a.md", Digest: "1"}, 4, time.Now()); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := m.RecordSweep(at, 31*time.Millisecond, 568, 2, 9); err != nil {
		t.Fatal(err)
	}
	m.Close()

	g := ReadGlance(dir)
	if !g.Present {
		t.Fatal("an indexed vault came back absent")
	}
	if g.Files != 1 || g.Chunks != 4 {
		t.Errorf("summary = %+v", g.Summary)
	}
	if !g.Swept.Equal(at) || g.Took != 31*time.Millisecond || g.Seen != 568 {
		t.Errorf("sweep = %v %v %d", g.Swept, g.Took, g.Seen)
	}
	if g.WroteNotes != 2 || g.WroteChunks != 9 {
		t.Errorf("write = %d notes, %d chunks", g.WroteNotes, g.WroteChunks)
	}
}

// A sweep with nothing to do still stamps that it ran -- that is the line
// proving the index is being kept up -- but it must not claim to have written
// anything, or "how current are the contents" is answered with a lie.
func TestGlanceKeepsAnEmptySweepApartFromAWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	m, err := OpenManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	if err := m.RecordSweep(at, 12*time.Millisecond, 568, 0, 0); err != nil {
		t.Fatal(err)
	}
	m.Close()

	g := ReadGlance(dir)
	if g.Swept.IsZero() {
		t.Error("a sweep that found nothing did not record that it ran")
	}
	if !g.Wrote.IsZero() {
		t.Errorf("a sweep that wrote nothing claimed a write at %v", g.Wrote)
	}
}

// Never indexed is the state of every fresh install, and it must not read as
// an error to a caller that only wants to render it.
func TestGlanceOfAnIndexThatDoesNotExistIsNotAnError(t *testing.T) {
	if g := ReadGlance(filepath.Join(t.TempDir(), "nothing")); g.Present {
		t.Errorf("an absent index came back present: %+v", g)
	}
}
