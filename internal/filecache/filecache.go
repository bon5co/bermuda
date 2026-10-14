// Package filecache keeps what was parsed out of files until the files change.
//
// It exists for readers that run on a timer over directories that mostly sit
// still. The board re-reads A.LOG, IMPROVE, the checklists and the memory vault
// every refresh, and before this it opened and parsed every file in all of them
// each time — two thousand opens every three seconds on a store of ordinary
// size, growing with every card written, to report the same picture as the
// last tick.
//
// A file is trusted unchanged while its size and modification time are the
// ones it was parsed at. A directory whose listing changes — a file added,
// removed or renamed — is forgotten whole and read again in full, so nothing a
// cache could get subtly wrong about renames survives a change to the
// directory.
package filecache

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Cache holds parsed values by path. A nil *Cache caches nothing: every Get
// parses, so a reader can take one without its other callers needing one.
//
// It is safe for concurrent use. The board can have two reads in flight at
// once — a refresh and the one an action asks for — and both go through the
// same cache.
type Cache[T any] struct {
	mu    sync.Mutex
	dirs  map[string][]string
	files map[string]file[T]
	// parses counts the times Get had to parse, which is the work the cache is
	// there to save. Tests read it to say an unchanged directory cost nothing.
	parses atomic.Int64
}

type file[T any] struct {
	mod  time.Time
	size int64
	val  T
	err  error
}

// New returns an empty cache.
func New[T any]() *Cache[T] {
	return &Cache[T]{dirs: map[string][]string{}, files: map[string]file[T]{}}
}

// Listing records what dir holds now. When that differs from the last listing
// recorded for it, everything cached under dir — subdirectories included — is
// forgotten, so this pass reads the directory in full.
func (c *Cache[T]) Listing(dir string, entries []os.DirEntry) {
	if c == nil {
		return
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
		if e.IsDir() {
			// A file and a directory of the same name are different listings.
			names[i] += string(filepath.Separator)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.dirs[dir]; ok && slices.Equal(old, names) {
		return
	}
	prefix := dir + string(filepath.Separator)
	for path := range c.files {
		if strings.HasPrefix(path, prefix) {
			delete(c.files, path)
		}
	}
	for d := range c.dirs {
		if strings.HasPrefix(d, prefix) {
			delete(c.dirs, d)
		}
	}
	c.dirs[dir] = names
}

// Get returns what parse made of path while info still describes the file it
// was parsed from, and parses again when it does not. info is the caller's
// stat of path: Lstat or Stat, whichever matches how parse opens the file.
//
// A parse error is cached like a value. A file that would not parse will not
// parse again until it changes, and re-reading it every tick only to fail the
// same way is the cost this exists to remove.
func (c *Cache[T]) Get(path string, info fs.FileInfo, parse func() (T, error)) (T, error) {
	if c == nil {
		return parse()
	}
	c.mu.Lock()
	f, ok := c.files[path]
	c.mu.Unlock()
	if ok && f.size == info.Size() && f.mod.Equal(info.ModTime()) {
		return f.val, f.err
	}
	c.parses.Add(1)
	val, err := parse()
	c.mu.Lock()
	c.files[path] = file[T]{mod: info.ModTime(), size: info.Size(), val: val, err: err}
	c.mu.Unlock()
	return val, err
}

// Parses is how many times Get has parsed a file since the cache was made.
func (c *Cache[T]) Parses() int64 {
	if c == nil {
		return 0
	}
	return c.parses.Load()
}
