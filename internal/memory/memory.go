// Package memory reports on the memory layer: curated facts as Markdown notes
// in an Obsidian vault.
//
// Nothing here parses a note's meaning. Agents read and write the notes with
// their own file tools, and Bermuda's part is only to anchor where they live —
// so this package answers the two questions a human actually asks about that
// anchor: where is it, and is anything in it. Counting files is the whole of
// it, deliberately. The moment this package starts interpreting note contents
// it has become a second, worse Obsidian.
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Dir resolves where memory notes live: $BERMUDA_MEMORY_DIR, else memory/
// inside the given state directory. The state-directory default keeps the
// whole of Bermuda findable by looking in one place; the override exists
// because a vault often already has a home.
//
// The state directory arrives as an argument rather than being resolved here,
// because resolving it is the command layer's job and importing that layer
// would be a cycle.
func Dir(state string) string {
	if d := os.Getenv("BERMUDA_MEMORY_DIR"); d != "" {
		return d
	}
	return filepath.Join(state, "memory")
}

// IndexName is the file every session is expected to load first.
const IndexName = "MEMORY.md"

// archiveName holds notes that are resolved but kept for history. They are
// counted separately: an archive that has grown large is a healthy sign, while
// the same number hidden inside the live count would read as clutter. A vault
// has one of these per section, not one at the root, so it is matched at any
// depth.
const archiveName = "archive"

// Stats is a filesystem-level summary of a memory directory.
type Stats struct {
	Dir      string    // where the notes live, as resolved
	LinkedTo string    // symlink target, empty when Dir is a real directory
	Present  bool      // the directory exists and could be read
	HasIndex bool      // MEMORY.md is present
	Entries  int       // non-blank lines in the index
	Notes    int       // .md notes anywhere in the tree, excluding the index and the archive
	Archived int       // .md notes under any archive/ folder
	Links    int       // total [[wikilinks]] across the live notes
	Bytes    int64     // total size of the live notes
	Newest   string    // most recently modified live note, path relative to Dir, without .md
	Written  time.Time // when that note was last written
}

// Read walks a memory directory and summarises it.
//
// The walk is recursive, because a memory directory is usually a whole vault
// and a vault keeps its notes in folders. Counting only the top level reported
// eight notes for a vault holding five hundred, which reads as an empty
// harness rather than a full one.
//
// A missing directory is not an error: memory is opt-in, and "not initialised
// yet" is a state the caller has to render either way. It comes back as Stats
// with Present false, so the difference between empty and absent survives —
// they call for different advice, and collapsing them would hide which one
// the reader is looking at.
func Read(dir string) Stats {
	st := Stats{Dir: dir}

	if target, err := os.Readlink(dir); err == nil {
		st.LinkedTo = target
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return st
	}
	st.Present = true

	if b, err := os.ReadFile(filepath.Join(dir, IndexName)); err == nil {
		st.HasIndex = true
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) != "" {
				st.Entries++
			}
		}
	}

	walk(dir, "", &st)
	return st
}

// walk descends one directory, accumulating into st. rel is the path of dir
// relative to the memory root, so Newest can name a nested note the way a
// reader would type it.
//
// It is written by hand rather than with filepath.WalkDir because the root is
// normally a symlink to a vault, and WalkDir does not follow those — the whole
// walk would end at the link. os.ReadDir resolves it.
func walk(dir, rel string, st *Stats) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		child := name
		if rel != "" {
			child = filepath.Join(rel, name)
		}

		if e.IsDir() {
			// Dot directories are the vault's own machinery — .obsidian,
			// .trash, .git — and none of it is a fact anyone wrote down.
			if strings.HasPrefix(name, ".") {
				continue
			}
			walk(path, child, st)
			continue
		}
		if !isNote(name) || name == IndexName {
			continue
		}
		if inArchive(child) {
			st.Archived++
			continue
		}

		st.Notes++
		if fi, err := e.Info(); err == nil {
			st.Bytes += fi.Size()
			if fi.ModTime().After(st.Written) {
				st.Written = fi.ModTime()
				st.Newest = strings.TrimSuffix(child, ".md")
			}
		}
		if b, err := os.ReadFile(path); err == nil {
			st.Links += strings.Count(string(b), "[[")
		}
	}
}

// inArchive reports whether a note sits anywhere under an archive/ folder.
// Vaults keep more than one — memory/archive/ and tasks/archive/ both hold
// resolved notes — so the test is on any path segment, not just the top one.
func inArchive(rel string) bool {
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == archiveName {
			return true
		}
	}
	return false
}

// isNote is deliberately extension-only. A vault holds attachments, canvases
// and whatever else the human keeps there, and counting those as facts would
// make the number meaningless the first time somebody pastes in a screenshot.
func isNote(name string) bool {
	return strings.HasSuffix(name, ".md")
}
