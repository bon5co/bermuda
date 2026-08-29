package index

import (
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxFileBytes is the largest note that gets indexed.
//
// A vault accumulates the occasional pasted transcript or exported thread, and
// embedding a megabyte of it buys nothing a person would ever search for while
// costing every sweep. Oversized files are reported as skipped rather than
// dropped silently — "not in the index" and "too big for the index" are
// different answers to "why did search not find it".
const maxFileBytes = 2 << 20

// File is one markdown file as the sweep sees it.
type File struct {
	// Path is relative to the vault root, slash-separated. It is the unique
	// key for everything this file contributed to the index.
	Path string
	// Abs is where to read it.
	Abs string
	// Digest is the SHA-256 of the file's bytes.
	Digest string
	Size   int64
	// Modified is reported, never compared: mtime is what a restore, a git
	// checkout and a sync client all get wrong, and the whole point of hashing
	// content is not to trust it.
	Modified time.Time
	// Skipped says why this file was seen and not indexed, empty when it was
	// indexed.
	Skipped string
}

// Section is the top folder a note lives in — memory, playbooks, procedures —
// or "" for a note at the vault root. It is stored as metadata so a search can
// be narrowed to one shelf without knowing the path layout.
func (f File) Section() string {
	if i := strings.IndexByte(f.Path, '/'); i > 0 {
		return f.Path[:i]
	}
	return ""
}

// skipDir is a directory the walk never descends into.
//
// Dot-directories cover .obsidian, .git and .trash in one rule: none of them
// hold notes a person wrote, and .obsidian in particular holds enough JSON to
// dominate a small vault's index.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

// Scan walks a vault root and hashes every markdown file in it.
//
// Every file is read and hashed on every sweep, rather than being pre-filtered
// on size and mtime. A vault is tens of megabytes and lives in page cache, so
// the read costs milliseconds; the filter costs correctness, because the two
// changes it misses — a restore from backup and a git checkout of an older
// note — are exactly the changes nobody would think to reindex by hand.
//
// The walk is root-scoped through os.Root, not a bare filepath.WalkDir. A
// vault is user content, and a note that is really a symlink to something
// outside it would otherwise be read and put in a searchable store — a private
// key indexed under whatever folder somebody dropped the link in. Rooted, that
// link is refused and reported as skipped instead.
func Scan(root string) ([]File, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return scanIn(r, root)
}

// scanIn is Scan against an already-open root, so a sweep that goes on to read
// the same files opens the root once.
func scanIn(r *os.Root, root string) ([]File, error) {
	fsys := r.FS()
	var out []File
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory must not end the sweep: a vault with a
			// permission problem in one folder should still index the rest.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != "." && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(path.Ext(d.Name()), ".md") {
			return nil
		}
		f := File{Path: p, Abs: filepath.Join(root, filepath.FromSlash(p))}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f.Size, f.Modified = info.Size(), info.ModTime()
		if info.Size() > maxFileBytes {
			f.Skipped = "larger than 2 MiB"
			out = append(out, f)
			return nil
		}
		b, err := ReadIn(r, p)
		if err != nil {
			// Unreadable, or a symlink pointing out of the vault, which is
			// refused rather than followed.
			f.Skipped = "unreadable: " + err.Error()
			out = append(out, f)
			return nil
		}
		f.Digest = Digest(b)
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Sorted so a run is reproducible and a diff of two --json runs is
	// readable.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadIn reads one vault-relative note through the root, so no symlink inside
// the vault can reach a file outside it.
func ReadIn(r *os.Root, rel string) ([]byte, error) {
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFileBytes+1))
}

// Plan is what one sweep has to do.
type Plan struct {
	// New is files the index has never seen.
	New []File
	// Changed is files whose digest no longer matches the manifest.
	Changed []File
	// Unchanged is files that need nothing done.
	Unchanged []File
	// Gone is paths in the manifest with no file behind them any more.
	Gone []string
	// Skipped is files seen and deliberately not indexed.
	Skipped []File
}

// Stale is everything that has to be reindexed: new files and changed ones.
func (p Plan) Stale() []File { return append(append([]File{}, p.New...), p.Changed...) }

// Empty reports whether the sweep has nothing to do — the common case, and the
// one where no helper process should be started at all.
func (p Plan) Empty() bool { return len(p.New) == 0 && len(p.Changed) == 0 && len(p.Gone) == 0 }

// Compare works out what changed, given what is on disk and what the manifest
// last recorded.
//
// The manifest is a cache of digests keyed by path, and the path is the unique
// key throughout: a file that changed is deleted from the collection by path
// and re-inserted, so a note that lost half its paragraphs does not leave the
// lost ones searchable. A file that moved is a delete plus an insert, which is
// correct — the path is the identity.
func Compare(files []File, known map[string]string) Plan {
	var p Plan
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if f.Skipped != "" {
			p.Skipped = append(p.Skipped, f)
			// A file that was indexed and has since grown past the limit is
			// still in the collection; leaving it in `seen` would keep it
			// there forever, so it is deliberately not marked seen and gets
			// deleted below.
			continue
		}
		seen[f.Path] = true
		switch prev, ok := known[f.Path]; {
		case !ok:
			p.New = append(p.New, f)
		case prev != f.Digest:
			p.Changed = append(p.Changed, f)
		default:
			p.Unchanged = append(p.Unchanged, f)
		}
	}
	for path := range known {
		if !seen[path] {
			p.Gone = append(p.Gone, path)
		}
	}
	sort.Strings(p.Gone)
	return p
}
