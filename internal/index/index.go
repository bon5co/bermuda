package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CollectionName is the one collection bermuda writes to.
const CollectionName = "vault"

// filesPerCall bounds how many files go into one helper invocation.
//
// One call for the whole vault would be simpler and is what the first version
// did. It is worse for the two things that actually happen: a first index has
// nothing to show for fifteen minutes, and a failure at file 500 loses the
// work done on the first 499 because the manifest is only written after a
// successful call. Batching costs about a second and a half per extra call.
const filesPerCall = 300

// Client is the index: a directory holding Chroma's store and the manifest,
// and a vault root to keep it in step with.
type Client struct {
	dir        string
	root       string
	collection string
}

// New builds a client. dir is where the index lives (Chroma's directory and
// the manifest); root is the vault it mirrors.
func New(dir, root string) *Client {
	return &Client{dir: dir, root: root, collection: CollectionName}
}

// Dir is where the whole index lives. Deleting it retires the feature.
func (c *Client) Dir() string { return c.dir }

// Root is the vault being indexed.
func (c *Client) Root() string { return c.root }

// DefaultDir is index/ inside the state directory.
func DefaultDir(state string) string { return filepath.Join(state, "index") }

// ResolveRoot decides which directory gets indexed.
//
// $BERMUDA_INDEX_ROOT wins outright. Otherwise it starts from the memory
// directory — following the symlink, because memory is usually a link into a
// vault the person already keeps — and walks up looking for the .obsidian
// folder that marks a vault root. Finding one indexes the whole vault, which
// is the point: the notes nobody loads every session are the ones worth
// searching. Finding none indexes the memory directory alone, which is the
// right answer for an install that has no vault.
func ResolveRoot(memoryDir string) string {
	if r := os.Getenv("BERMUDA_INDEX_ROOT"); r != "" {
		return r
	}
	start := memoryDir
	if resolved, err := filepath.EvalSymlinks(memoryDir); err == nil {
		start = resolved
	}
	for dir := start; ; {
		if st, err := os.Stat(filepath.Join(dir, ".obsidian")); err == nil && st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// Report is what one sweep did.
type Report struct {
	Root      string
	Indexed   int // files reindexed, new and changed
	New       int
	Changed   int
	Unchanged int
	Removed   int // files gone from disk, deleted from the collection
	Chunks    int // chunks written this sweep
	Skipped   []File
	Rebuilt   bool
	Why       string // why a rebuild happened, when one did
	Took      time.Duration
}

// Sweep brings the index back in step with the vault.
//
// The stale check is pure Go and touches no helper: it hashes the vault and
// compares against the manifest. Only when something has actually changed does
// a Python process start. That is what makes this safe to call from the daemon
// on a timer — the common case is a directory walk and nothing else.
func (c *Client) Sweep(ctx context.Context, rebuild bool, progress func(string)) (Report, error) {
	start := time.Now()
	rep := Report{Root: c.root}
	if progress == nil {
		progress = func(string) {}
	}

	man, err := OpenManifest(c.dir)
	if err != nil {
		return rep, err
	}
	defer man.Close()

	stale, why, err := man.StaleForRules(c.root)
	if err != nil {
		return rep, err
	}
	if stale && !rebuild {
		rebuild, rep.Why = true, why
	}
	if rebuild {
		rep.Rebuilt = true
		if rep.Why == "" {
			rep.Why = "asked for"
		}
		progress("rebuilding: " + rep.Why)
		if _, err := c.call(ctx, writeTimeout, map[string]any{"op": "drop"}); err != nil {
			return rep, err
		}
		if err := man.Reset(); err != nil {
			return rep, err
		}
	}

	root, err := os.OpenRoot(c.root)
	if err != nil {
		return rep, fmt.Errorf("open vault %s: %w", c.root, err)
	}
	defer root.Close()
	files, err := scanIn(root, c.root)
	if err != nil {
		return rep, fmt.Errorf("scan %s: %w", c.root, err)
	}
	known, err := man.Digests()
	if err != nil {
		return rep, err
	}
	plan := Compare(files, known)
	rep.New, rep.Changed = len(plan.New), len(plan.Changed)
	rep.Unchanged, rep.Removed = len(plan.Unchanged), len(plan.Gone)
	rep.Skipped = plan.Skipped
	if plan.Empty() {
		rep.Took = time.Since(start)
		return rep, man.RecordSweep(time.Now(), rep.Took, len(files), 0, 0)
	}

	// Deletions ride along with the first write call rather than taking a
	// process of their own: a note deleted in the same edit that changed
	// another one is the ordinary case, and two spawns for it is one too many.
	pending := append([]string{}, plan.Gone...)
	for _, f := range plan.Changed {
		pending = append(pending, f.Path)
	}

	stale2 := plan.Stale()
	for i := 0; i < len(stale2) || len(pending) > 0; i += filesPerCall {
		end := min(i+filesPerCall, len(stale2))
		batch := stale2[i:end]

		docs := make([]map[string]any, 0, len(batch)*8)
		counts := make([]int, len(batch))
		for j, f := range batch {
			b, err := ReadIn(root, f.Path)
			if err != nil {
				// A note deleted between the scan and the read is not an
				// error; the next sweep will see it gone.
				continue
			}
			note := Parse(strings.TrimSuffix(filepath.Base(f.Path), ".md"), string(b))
			counts[j] = len(note.Chunks)
			for _, ch := range note.Chunks {
				docs = append(docs, map[string]any{
					"id":   ch.ID(f.Path),
					"text": ch.Embedded(note.Title),
					"metadata": map[string]any{
						"path":        f.Path,
						"title":       note.Title,
						"description": note.Description,
						"type":        note.Type,
						"section":     f.Section(),
						"heading":     ch.Heading,
						"ord":         ch.Ord,
						"digest":      f.Digest,
						"modified":    f.Modified.Unix(),
					},
				})
			}
		}

		if len(docs) > 0 || len(pending) > 0 {
			progress(fmt.Sprintf("indexing %d file(s), %d chunk(s)%s",
				len(batch), len(docs), removalNote(len(pending))))
			if _, err := c.call(ctx, writeTimeout, map[string]any{
				"op":           "upsert",
				"delete_paths": pending,
				"docs":         docs,
			}); err != nil {
				return rep, err
			}
		}
		pending = nil

		// The manifest is written only after the helper has accepted the
		// batch, so a crash mid-sweep leaves those files stale rather than
		// recorded-but-absent. Re-running the sweep is then always the fix.
		now := time.Now()
		for j, f := range batch {
			if err := man.Record(f, counts[j], now); err != nil {
				return rep, err
			}
			rep.Chunks += counts[j]
		}
		rep.Indexed += len(batch)
		if err := man.Forget(plan.Gone...); err != nil {
			return rep, err
		}
		if end >= len(stale2) {
			break
		}
	}

	rep.Took = time.Since(start)
	return rep, man.RecordSweep(time.Now(), rep.Took, len(files), rep.Indexed, rep.Chunks)
}

// removalNote renders the deletions half of a progress line, or nothing.
func removalNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", removing %d", n)
}

// Hit is one search result.
type Hit struct {
	Path        string
	Title       string
	Description string
	Heading     string
	Section     string
	Type        string
	Ord         int
	Text        string
	// Score is 1 - cosine distance: 1.0 is identical, 0 is unrelated. It is
	// reported instead of the raw distance because "higher is better" is the
	// only convention a reader does not have to be told.
	Score float64
}

// SearchOpts narrows a search.
type SearchOpts struct {
	N int
	// Section restricts results to one top-level folder of the vault —
	// "memory", "playbooks". Empty searches everything.
	Section string
	// Type restricts to a memory note type: user, feedback, project,
	// reference.
	Type string
}

// Search asks the index a question in the words the asker has, not the words
// the note used.
func (c *Client) Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("nothing to search for")
	}
	if opts.N <= 0 {
		opts.N = 8
	}
	req := map[string]any{"op": "query", "text": query, "n": opts.N}
	var filters []map[string]any
	if opts.Section != "" {
		filters = append(filters, map[string]any{"section": map[string]any{"$eq": opts.Section}})
	}
	if opts.Type != "" {
		filters = append(filters, map[string]any{"type": map[string]any{"$eq": opts.Type}})
	}
	switch len(filters) {
	case 0:
	case 1:
		req["where"] = filters[0]
	default:
		req["where"] = map[string]any{"$and": filters}
	}

	resp, err := c.call(ctx, queryTimeout, req)
	if err != nil {
		return nil, err
	}
	raw, _ := resp["hits"].([]any)
	hits := make([]Hit, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		meta, _ := m["metadata"].(map[string]any)
		h := Hit{Text: str(m["text"])}
		if d, ok := m["distance"].(float64); ok {
			h.Score = 1 - d
		}
		h.Path = str(meta["path"])
		h.Title = str(meta["title"])
		h.Description = str(meta["description"])
		h.Heading = str(meta["heading"])
		h.Section = str(meta["section"])
		h.Type = str(meta["type"])
		if o, ok := meta["ord"].(float64); ok {
			h.Ord = int(o)
		}
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	return hits, nil
}

// Status is what the index can say about itself without embedding anything.
type Status struct {
	Dir    string
	Root   string
	Python string // how the helper would be run, empty when nothing can run it
	// Glance is everything the manifest already knows: what is indexed, when
	// the last sweep ran and what it cost, and what the last sweep that wrote
	// anything did. The board reads the same thing.
	Glance
	// Stale is how many files on disk differ from what was indexed, and
	// Missing how many indexed files are gone. Both come from hashing the
	// vault, so they are true regardless of what the collection holds -- and
	// they are the two fields the board deliberately does not compute, because
	// they cost a scan.
	Stale   int
	Missing int
}

// Status reports where the index stands. It starts no helper: everything here
// is the manifest and a walk of the vault.
func (c *Client) Status(ctx context.Context) (Status, error) {
	st := Status{Dir: c.dir, Root: c.root, Glance: ReadGlance(c.dir)}
	if py, err := findPython(ctx); err == nil {
		st.Python = py.how
	}
	man, err := OpenManifest(c.dir)
	if err != nil {
		return st, err
	}
	defer man.Close()
	files, err := Scan(c.root)
	if err != nil {
		return st, err
	}
	known, err := man.Digests()
	if err != nil {
		return st, err
	}
	plan := Compare(files, known)
	st.Stale, st.Missing = len(plan.New)+len(plan.Changed), len(plan.Gone)
	return st, nil
}

// Drop deletes the collection and forgets everything the manifest recorded.
// The vault is not touched: the index is derived data, and this is the button
// that says so.
func (c *Client) Drop(ctx context.Context) error {
	if _, err := c.call(ctx, writeTimeout, map[string]any{"op": "drop"}); err != nil {
		return err
	}
	man, err := OpenManifest(c.dir)
	if err != nil {
		return err
	}
	defer man.Close()
	return man.Reset()
}

// str reads a JSON string field that may be absent or null.
func str(v any) string {
	s, _ := v.(string)
	return s
}
