// Package alog stores the short updates shown in the board's A.LOG tab.
// Each entry is an ordinary Markdown file; no database owns this record.
package alog

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v3/internal/lockfile"
	"github.com/bon5co/bermuda/v3/internal/statefs"
	"gopkg.in/yaml.v3"
)

const MaxWords = 50
const idTimeLayout = "20060102T150405.000000000Z"

type Entry struct {
	ID      string    `json:"id"`
	Repo    string    `json:"repo"`
	Branch  string    `json:"branch"`
	Topic   string    `json:"topic"`
	Body    string    `json:"body"`
	Created time.Time `json:"created"`
	// TimeSource is "birthtime", or "filename" on filesystems without birthtime.
	TimeSource string `json:"time_source"`
	Path       string `json:"path"`
}

type metadata struct {
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
	Topic  string `yaml:"topic"`
}

func Dir(state string) string { return filepath.Join(state, "alog") }

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func entryPath(dir, id string) (string, error) {
	id = strings.TrimSuffix(id, ".md")
	if !safeID.MatchString(id) || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid A.LOG entry id %q", id)
	}
	return filepath.Join(dir, id+".md"), nil
}

func validate(e Entry) error {
	for _, field := range []struct{ name, value string }{{"repo", e.Repo}, {"branch", e.Branch}, {"topic", e.Topic}} {
		if strings.TrimSpace(field.value) == "" || strings.ContainsAny(field.value, "\r\n") {
			return fmt.Errorf("%s must be a nonempty single line", field.name)
		}
	}
	n := len(strings.Fields(e.Body))
	if n == 0 || n > MaxWords {
		return fmt.Errorf("body must contain 1–%d words (got %d)", MaxWords, n)
	}
	return nil
}

// withLock serializes CLI readers and writers. Editors outside the CLI remain
// ordinary filesystem editors, so a malformed file is reported by name.
func withLock(dir string, fn func() error) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := lockfile.Acquire(filepath.Join(dir, ".lock"))
		if err == nil {
			defer lock.Release()
			return fn()
		}
		var held *lockfile.ErrHeld
		if !errors.As(err, &held) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func Read(dir, id string) (entry Entry, err error) {
	err = withLock(dir, func() error { entry, err = read(dir, id); return err })
	return
}

func read(dir, id string) (Entry, error) {
	path, err := entryPath(dir, id)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	if !info.Mode().IsRegular() {
		return Entry{}, fmt.Errorf("%s is not a regular Markdown file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return Entry{}, err
	}
	if !os.SameFile(info, opened) {
		return Entry{}, fmt.Errorf("%s changed while opening", path)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return Entry{}, err
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return Entry{}, fmt.Errorf("%s: missing YAML frontmatter", path)
	}
	parts := strings.SplitN(strings.TrimPrefix(s, "---\n"), "\n---\n", 2)
	if len(parts) != 2 {
		return Entry{}, fmt.Errorf("%s: unclosed YAML frontmatter", path)
	}
	var meta metadata
	if err := yaml.Unmarshal([]byte(parts[0]), &meta); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", path, err)
	}
	e := Entry{ID: strings.TrimSuffix(filepath.Base(path), ".md"), Repo: meta.Repo, Branch: meta.Branch, Topic: meta.Topic, Body: strings.TrimSpace(parts[1]), Path: path}
	if err := validate(e); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", path, err)
	}
	e.Created, err = birthtime(f)
	e.TimeSource = "birthtime"
	if err != nil || e.Created.IsZero() {
		// Creation time is immutable in our generated filename. Unlike mtime or
		// ctime this fallback cannot move an edited card to the top.
		stamp, _, _ := strings.Cut(e.ID, "-")
		e.Created, err = time.Parse(idTimeLayout, stamp)
		if err != nil {
			return Entry{}, fmt.Errorf("%s: filesystem has no creation time and filename has no write timestamp", path)
		}
		e.TimeSource = "filename"
	}
	return e, nil
}

func List(dir string) (entries []Entry, err error) {
	entries = []Entry{}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return entries, nil
	}
	err = withLock(dir, func() error {
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var problems []error
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".md") || file.IsDir() {
				continue
			}
			e, err := read(dir, file.Name())
			if err != nil {
				problems = append(problems, err)
				continue
			}
			entries = append(entries, e)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Created.Equal(entries[j].Created) {
				return entries[i].ID > entries[j].ID
			}
			return entries[i].Created.After(entries[j].Created)
		})
		return errors.Join(problems...)
	})
	return
}

func encode(e Entry) ([]byte, error) {
	if err := validate(e); err != nil {
		return nil, err
	}
	meta, err := yaml.Marshal(metadata{e.Repo, e.Branch, e.Topic})
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(meta) + "---\n\n" + strings.TrimSpace(e.Body) + "\n"), nil
}

func Write(dir string, e Entry) (entry Entry, err error) {
	b, err := encode(e)
	if err != nil {
		return Entry{}, err
	}
	err = withLock(dir, func() error {
		var suffix [6]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return err
		}
		id := time.Now().UTC().Format(idTimeLayout) + "-" + hex.EncodeToString(suffix[:])
		path, _ := entryPath(dir, id)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, statefs.File)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return err
		}
		entry, err = read(dir, id)
		return err
	})
	return
}

// Edit writes in place so the inode and its filesystem creation time survive.
// All four fields are supplied by the caller; Update supports partial edits.
func Edit(dir, id string, e Entry) (entry Entry, err error) {
	return Update(dir, id, func(old *Entry) error { *old = e; return nil })
}

// Update applies a partial edit under the same lock as the read, so concurrent
// CLI edits to different fields do not overwrite each other's changes.
func Update(dir, id string, change func(*Entry) error) (entry Entry, err error) {
	err = withLock(dir, func() error {
		old, err := read(dir, id)
		if err != nil {
			return err
		}
		path := old.Path
		if err := change(&old); err != nil {
			return err
		}
		b, err := encode(old)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_RDWR, statefs.File)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteAt(b, 0); err != nil {
			return err
		}
		if err := f.Truncate(int64(len(b))); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		entry, err = read(dir, id)
		return err
	})
	return
}
