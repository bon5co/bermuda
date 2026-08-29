package index

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bon5co/bermuda/v3/internal/statefs"
	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so nix builds stay simple
)

// chunkerVersion is bumped whenever the chunking rules change.
//
// Chunk text is what was embedded, so a change to how a note is split makes
// every stored vector describe something the current code would never produce.
// The digests would still match and nothing would reindex — a silently stale
// index that looks healthy. Recording the version turns that into a one-line
// "chunking changed, reindexing everything" instead.
const chunkerVersion = "1"

// Manifest records what has been indexed, so a sweep can tell what changed
// without asking the collection.
//
// It is deliberately its own SQLite file inside the index directory rather
// than a table in bermuda's store: everything this feature owns then lives
// under one directory that can be deleted to retire it, and the store's schema
// carries nothing that is only a cache. The manifest *is* a cache — losing it
// costs one full reindex and nothing else.
type Manifest struct {
	db   *sql.DB
	path string
}

// Row is one file's indexing state.
type Row struct {
	Path      string
	Digest    string
	Chunks    int
	Size      int64
	Modified  time.Time
	IndexedAt time.Time
}

// OpenManifest opens, and creates if needed, the manifest in dir.
func OpenManifest(dir string) (*Manifest, error) {
	if err := os.MkdirAll(dir, statefs.Dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "manifest.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS files (
  path       TEXT PRIMARY KEY,
  digest     TEXT NOT NULL,
  chunks     INTEGER NOT NULL,
  size       INTEGER NOT NULL,
  modified   INTEGER NOT NULL,
  indexed_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create manifest schema: %w", err)
	}
	return &Manifest{db: db, path: path}, nil
}

// Close releases the manifest.
func (m *Manifest) Close() error { return m.db.Close() }

// Digests is every path the index knows about, and the content hash it was
// indexed at.
func (m *Manifest) Digests() (map[string]string, error) {
	rows, err := m.db.Query(`SELECT path, digest FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, d string
		if err := rows.Scan(&p, &d); err != nil {
			return nil, err
		}
		out[p] = d
	}
	return out, rows.Err()
}

// Record stores what a file was indexed as.
func (m *Manifest) Record(f File, chunks int, at time.Time) error {
	_, err := m.db.Exec(`
INSERT INTO files (path, digest, chunks, size, modified, indexed_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
  digest=excluded.digest, chunks=excluded.chunks, size=excluded.size,
  modified=excluded.modified, indexed_at=excluded.indexed_at`,
		f.Path, f.Digest, chunks, f.Size, f.Modified.Unix(), at.Unix())
	return err
}

// Forget removes paths that are no longer indexed.
func (m *Manifest) Forget(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`DELETE FROM files WHERE path = ?`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, p := range paths {
		if _, err := st.Exec(p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Summary is what `memory index --status` reports without starting a helper.
type Summary struct {
	Files  int
	Chunks int
	Newest time.Time
}

// Summary counts what the manifest holds.
func (m *Manifest) Summary() (Summary, error) {
	var (
		s    Summary
		last sql.NullInt64
	)
	err := m.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(chunks),0), MAX(indexed_at) FROM files`).
		Scan(&s.Files, &s.Chunks, &last)
	if err != nil {
		return s, err
	}
	if last.Valid {
		s.Newest = time.Unix(last.Int64, 0)
	}
	return s, nil
}

// Setting reads a stored setting, empty when unset.
func (m *Manifest) Setting(key string) string {
	var v string
	if err := m.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// SetSetting stores a setting.
func (m *Manifest) SetSetting(key, value string) error {
	_, err := m.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Reset empties the manifest, which is how a rebuild forgets everything
// without deleting the file out from under an open handle.
func (m *Manifest) Reset() error {
	_, err := m.db.Exec(`DELETE FROM files`)
	return err
}

// StaleForRules reports whether the recorded root or chunker no longer matches
// what this build would produce, and stores the current values.
//
// Both are grounds for a full reindex: pointing the index at a different vault
// leaves every path in the manifest describing a file that no longer exists,
// and changing the chunker leaves every vector describing text the code would
// no longer produce.
func (m *Manifest) StaleForRules(root string) (bool, string, error) {
	stale, why := false, ""
	if prev := m.Setting("root"); prev != "" && prev != root {
		stale, why = true, fmt.Sprintf("indexed vault changed from %s to %s", prev, root)
	}
	if prev := m.Setting("chunker"); prev != "" && prev != chunkerVersion {
		stale, why = true, "chunking rules changed since the last index"
	}
	if err := m.SetSetting("root", root); err != nil {
		return stale, why, err
	}
	return stale, why, m.SetSetting("chunker", chunkerVersion)
}

// Glance is what the board can say about the index without scanning the vault.
//
// The board refreshes every three seconds. Hashing the whole vault on that
// tick would cost about a percent of a core for as long as anyone leaves the
// board open, to answer a question that has not changed since the last daemon
// sweep — so the sweep records what it saw and the board reads that instead.
// Everything here is one SQLite read.
type Glance struct {
	Present bool // the manifest exists and could be read
	Summary
	// Swept is when the last sweep ran, and Took how long it spent hashing the
	// vault. A sweep runs whether or not anything changed, so this is the line
	// that says the index is alive.
	Swept time.Time
	Took  time.Duration
	// Seen is how many notes the last sweep hashed.
	Seen int
	// Wrote, WroteNotes, WroteChunks and WroteTook describe the last sweep
	// that actually indexed something, which is usually not the last sweep.
	Wrote       time.Time
	WroteNotes  int
	WroteChunks int
	WroteTook   time.Duration
}

// ReadGlance opens the manifest read-only and summarises it. A missing or
// unreadable index is not an error: it is the state of every install that has
// never indexed, and the board has to render it either way.
func ReadGlance(dir string) Glance {
	var g Glance
	if _, err := os.Stat(filepath.Join(dir, "manifest.db")); err != nil {
		return g
	}
	m, err := OpenManifest(dir)
	if err != nil {
		return g
	}
	defer m.Close()
	g.Present = true
	if g.Summary, err = m.Summary(); err != nil {
		return g
	}
	g.Swept, g.Took = m.stamp("swept")
	g.Wrote, g.WroteTook = m.stamp("wrote")
	g.Seen = m.number("swept_files")
	g.WroteNotes = m.number("wrote_notes")
	g.WroteChunks = m.number("wrote_chunks")
	return g
}

// RecordSweep stores what a sweep did, so the board never has to repeat it.
//
// Two stamps rather than one: a sweep that found nothing is the common case
// and proves the index is being kept up, while the last sweep that actually
// wrote something is what says how current the contents are. Collapsing them
// into one timestamp loses whichever question the reader had.
func (m *Manifest) RecordSweep(at time.Time, took time.Duration, seen, wroteNotes, wroteChunks int) error {
	if err := m.setStamp("swept", at, took); err != nil {
		return err
	}
	if err := m.SetSetting("swept_files", itoa(seen)); err != nil {
		return err
	}
	if wroteNotes == 0 && wroteChunks == 0 {
		return nil
	}
	if err := m.setStamp("wrote", at, took); err != nil {
		return err
	}
	if err := m.SetSetting("wrote_notes", itoa(wroteNotes)); err != nil {
		return err
	}
	return m.SetSetting("wrote_chunks", itoa(wroteChunks))
}

// setStamp stores a moment and a duration under one name.
func (m *Manifest) setStamp(key string, at time.Time, took time.Duration) error {
	if err := m.SetSetting(key+"_at", itoa(int(at.Unix()))); err != nil {
		return err
	}
	return m.SetSetting(key+"_ms", itoa(int(took.Milliseconds())))
}

// stamp reads a moment and a duration back.
func (m *Manifest) stamp(key string) (time.Time, time.Duration) {
	var at time.Time
	if sec := m.number(key + "_at"); sec > 0 {
		at = time.Unix(int64(sec), 0)
	}
	return at, time.Duration(m.number(key+"_ms")) * time.Millisecond
}

// number reads a setting stored as an integer, zero when absent or unreadable.
func (m *Manifest) number(key string) int {
	n, err := strconv.Atoi(m.Setting(key))
	if err != nil {
		return 0
	}
	return n
}
