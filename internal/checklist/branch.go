package checklist

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v3/internal/lockfile"
	"github.com/bon5co/bermuda/v3/internal/statefs"
)

const branchMarker = "<!-- bermuda-check: "

type branchMetadata struct {
	Repo    string    `json:"repo"`
	Branch  string    `json:"branch"`
	Created time.Time `json:"created"`
}

// NewBranch creates one page per repository and branch. Retrying returns the
// existing page without changing its title, timestamps or items. Legacy pages
// are never assigned to a branch or rewritten.
func NewBranch(dir, title, about, repo, branch string, now time.Time) (List, error) {
	repo, branch = strings.TrimSpace(repo), strings.TrimSpace(branch)
	if repo == "" || branch == "" {
		return List{}, errors.New("a checklist needs --repo and --branch (or a current git branch)")
	}
	if strings.TrimSpace(title) == "" || Slugify(title) == "" {
		return List{}, errors.New("a checklist needs a title that can form a filename")
	}
	lock, err := lockfile.Acquire(filepath.Join(dir, ".lock"))
	if err != nil {
		return List{}, err
	}
	defer lock.Release()
	if l, err := ForBranch(dir, repo, branch); err == nil {
		return l, nil
	} else if !errors.Is(err, ErrBranchMissing) {
		return List{}, err
	}
	// The identity hash separates branch names that slugify alike and identical
	// branch names in different repositories. The title stays readable in CLI ids.
	id := sha256.Sum256([]byte(repo + "\x00" + branch))
	name := fmt.Sprintf("branch-%x %s", id[:12], Slugify(title))
	path := filepath.Join(dir, name+Ext)
	meta, err := json.Marshal(branchMetadata{repo, branch, now})
	if err != nil {
		return List{}, err
	}
	body := "# " + strings.TrimSpace(title) + "\n"
	if a := strings.TrimSpace(about); a != "" {
		body += a + "\n"
	}
	body += branchMarker + string(meta) + " -->\n\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, statefs.File)
	if err != nil {
		return List{}, err
	}
	_, err = f.WriteString(body)
	closeErr := f.Close()
	if err != nil {
		return List{}, err
	}
	if closeErr != nil {
		return List{}, closeErr
	}
	return Load(path)
}

var ErrBranchMissing = errors.New("no checklist for this repository and branch")

// ForBranch looks only at pages explicitly created with a branch identity.
func ForBranch(dir, repo, branch string) (List, error) {
	lists, bad := All(dir)
	if len(bad) > 0 {
		return List{}, bad[0]
	}
	var found *List
	for _, l := range lists {
		if l.Repo == repo && l.Branch == branch {
			if found != nil {
				return List{}, fmt.Errorf("duplicate checklists for %s · %s: %s, %s", repo, branch, found.Name, l.Name)
			}
			copy := l
			found = &copy
		}
	}
	if found == nil {
		return List{}, ErrBranchMissing
	}
	return *found, nil
}

func readBranchMetadata(l *List, data string) error {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, branchMarker) {
			continue
		}
		if l.Branch != "" {
			return errors.New("multiple branch metadata records")
		}
		if !strings.HasSuffix(line, " -->") {
			return errors.New("invalid checklist branch metadata")
		}
		var meta branchMetadata
		if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(line, branchMarker), " -->")), &meta); err != nil {
			return fmt.Errorf("invalid checklist branch metadata: %w", err)
		}
		if strings.TrimSpace(meta.Repo) == "" || strings.TrimSpace(meta.Branch) == "" {
			return errors.New("checklist branch metadata needs repo and branch")
		}
		l.Repo, l.Branch = meta.Repo, meta.Branch
		// Hand-added checkboxes have no stamp: use creation time, not the whole
		// page's mtime, so toggling one item does not reorder all unstamped items.
		for i := range l.Items {
			if l.Items[i].Updated.IsZero() {
				l.Items[i].Updated = meta.Created
			}
		}
	}
	return nil
}

var updateTail = regexp.MustCompile(`\s*<!-- bermuda-updated: ([^<>]+) -->\s*$`)

func itemMetadata(text string) (string, time.Time) {
	loc := updateTail.FindStringSubmatchIndex(text)
	if loc == nil {
		return text, time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, text[loc[2]:loc[3]])
	if err != nil {
		return text, time.Time{}
	}
	return strings.TrimSpace(text[:loc[0]]), t
}
func updatedComment(now time.Time) string {
	return " <!-- bermuda-updated: " + now.UTC().Format(time.RFC3339Nano) + " -->"
}

func pageLock(path string) (*lockfile.Lock, error) {
	return lockfile.Acquire(filepath.Join(filepath.Dir(path), ".lock"))
}

// setBranchItem changes only the selected line. Original ordering, line endings
// and all other Markdown bytes stay intact; legacy Set retains its one-byte write.
func setBranchItem(l List, it Item, done bool, now time.Time) (Item, bool, error) {
	data, err := os.ReadFile(l.Path)
	if err != nil {
		return Item{}, false, err
	}
	if sha256.Sum256(data) != l.Revision {
		return Item{}, false, errors.New("checklist changed; refresh and select the item again")
	}
	start := strings.LastIndex(string(data[:it.Offset]), "\n") + 1
	end := strings.IndexByte(string(data[it.Offset:]), '\n')
	if end < 0 {
		end = len(data)
	} else {
		end += int(it.Offset)
	}
	line := string(data[start:end])
	ending := ""
	if strings.HasSuffix(line, "\r") {
		ending = "\r"
		line = strings.TrimSuffix(line, "\r")
	}
	line, _ = itemMetadata(line)
	mark := byte(' ')
	if done {
		mark = 'x'
	}
	changed := []byte(line)
	changed[int(it.Offset)-start] = mark
	line = string(changed) + updatedComment(now) + ending
	out := append([]byte{}, data[:start]...)
	out = append(out, []byte(line)...)
	out = append(out, data[end:]...)
	if err := os.WriteFile(l.Path, out, statefs.File); err != nil {
		return Item{}, false, err
	}
	it.Done, it.Updated = done, now
	return it, true, nil
}

// Newest sorts display copies without changing page order or CLI item numbers.
func Newest(lists []List) {
	sort.SliceStable(lists, func(i, j int) bool {
		if lists[i].Updated.Equal(lists[j].Updated) {
			return lists[i].Name < lists[j].Name
		}
		return lists[i].Updated.After(lists[j].Updated)
	})
}
func (l List) NewestItems() []Item {
	items := append([]Item(nil), l.Items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Updated.After(items[j].Updated) })
	return items
}

// Toggle acts on the row the board displayed, refusing a stale selection if an
// editor inserted or changed items before the asynchronous action reached disk.
func Toggle(path string, selected Item, revision [32]byte) (Item, error) {
	lock, err := pageLock(path)
	if err != nil {
		return Item{}, err
	}
	defer lock.Release()
	l, err := Load(path)
	if err != nil {
		return Item{}, err
	}
	if l.Revision != revision {
		return Item{}, errors.New("checklist changed; press r, then select the item again")
	}
	it, err := l.Find(fmt.Sprint(selected.Index))
	if err != nil {
		return Item{}, err
	}
	if it.String() != selected.String() || it.Done != selected.Done {
		return Item{}, errors.New("checklist item changed; press r, then select it again")
	}
	if l.Branch != "" {
		updated, _, err := setBranchItem(l, it, !it.Done, time.Now())
		return updated, err
	}
	// Preserve the historical one-byte behavior for unowned pages.
	f, err := os.OpenFile(path, os.O_WRONLY, statefs.File)
	if err != nil {
		return Item{}, err
	}
	mark := byte(' ')
	if !it.Done {
		mark = 'x'
	}
	_, err = f.WriteAt([]byte{mark}, it.Offset)
	closeErr := f.Close()
	if err != nil {
		return Item{}, err
	}
	if closeErr != nil {
		return Item{}, closeErr
	}
	it.Done = !it.Done
	return it, nil
}
