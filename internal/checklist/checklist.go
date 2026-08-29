// Package checklist is the record of what is still outstanding on one piece of
// work, and who it is blocked on.
//
// It is the fourth kind, and it exists because the other three answer different
// questions. A thread is append-only and time-ordered: it records that a PR was
// opened, and reading "is it merged?" out of it means scrolling the whole log
// and mentally diffing one event against another — inside a window that drops
// the oldest entry first, which is the one still outstanding. A flow carries
// step state, but only for steps declared before the run started and only until
// the run ends; half of any real session is work discovered after step one. The
// forum is for what should still be findable in a month, and an open PR has the
// wrong half-life for that. Memory is for what is true, and an open PR is not a
// standing fact.
//
// So: one Markdown page per piece of work, ordinary checkboxes, in a folder of
// the same vault memory lives in. Nothing here is a database. The page is
// readable and editable in Obsidian by a human who never runs the CLI, and
// greppable by an agent that has only a filesystem — and it inherits that
// vault's sync, file history and search for free. Bermuda's part is to create
// the page, to tick a line without disturbing the rest of the file, and to
// count what is left.
//
// What this is deliberately not: a task tracker. No assignment, no due dates,
// no priorities, no cross-agent anything. Issues and project boards already do
// that and are where humans look. This is scratch state for one piece of work
// in flight, whose whole job is to survive an agent restart and answer "is it
// done yet" without a human having to ask.
package checklist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v3/internal/statefs"
)

// Ext is the extension a checklist page carries. Markdown, because the page is
// the interface: the thing a human opens in Obsidian is the same bytes the CLI
// reads.
const Ext = ".md"

// FolderName is the folder inside the memory directory that holds the pages.
//
// A folder rather than loose files next to the notes, and deliberately not a
// line in MEMORY.md: same filesystem, different half-life. The index is what
// every session loads, and filling it with work that will be finished by
// Thursday is how the standing facts stop being read.
const FolderName = "checklists"

// Env names the checklist a command should act on when none is given. It is
// what a flow step is told, so a step can tick an item without being handed a
// list id it would have to remember.
const Env = "BERMUDA_CHECK"

// stamp is the filename prefix, in local time.
//
// Datetime-first is what makes the folder sort newest-last in Obsidian's file
// list with no index, no frontmatter and no metadata to maintain — which is the
// whole reason there is no store here. Local rather than UTC because the person
// reading the folder is reading it where they are.
const stamp = "2006-01-02T1504"

// Dir is where checklists live under a memory directory.
func Dir(memoryDir string) string { return filepath.Join(memoryDir, FolderName) }

// Item is one checkbox line, as read from a page.
type Item struct {
	// Text is what the line says about the work: what it is and, where one was
	// given, the url or issue that names it. A reference is not split back out
	// of it. It was written into the line by whoever added the item, and a
	// parser trying to recover it would have to guess where a URL ends and a
	// sentence begins — on a page a human is free to edit by hand.
	Text string

	// BlockedOn names who the item is waiting on, and Why says what only they
	// can do. This is the distinction the whole feature turns on: "the agent has
	// not done it yet" and "the agent cannot do it" look identical in every
	// other record, and a checklist that cannot tell them apart reads as an idle
	// agent.
	BlockedOn string
	Why       string

	Done bool

	// Index is the item's 1-based position on the page, which is what `tick 3`
	// means.
	Index int

	// Offset is the byte offset of the single character between the checkbox
	// brackets. Ticking writes one byte there, which is what lets a page open in
	// somebody's editor survive being ticked from a terminal.
	Offset int64
}

// Blocked reports whether this item is waiting on somebody.
func (i Item) Blocked() bool { return strings.TrimSpace(i.BlockedOn) != "" }

// Box renders the item's state the way the page does.
func (i Item) Box() string {
	if i.Done {
		return "[x]"
	}
	return "[ ]"
}

// String renders an item the way it reads on the page, without the checkbox.
func (i Item) String() string {
	s := i.Text
	if i.Blocked() {
		s += " — BLOCKED: " + i.BlockedOn
		if i.Why != "" {
			s += " (" + i.Why + ")"
		}
	}
	return s
}

// Entry is a new item as it is typed, before it becomes a line.
//
// Separate from Item because the two directions are not symmetric: an entry
// carries a reference as its own field, and once written that reference is part
// of the text forever. Modelling both as one struct would imply a round trip
// that does not exist.
type Entry struct {
	Text string
	// Ref is a url, an issue number, whatever names the artifact — appended to
	// the text so the line stays one readable sentence.
	Ref       string
	BlockedOn string
	Why       string
	Done      bool
}

// Line renders an entry as the Markdown line it becomes.
func (e Entry) Line() string {
	box := "- [ ] "
	if e.Done {
		box = "- [x] "
	}
	s := box + strings.TrimSpace(e.Text)
	if ref := strings.TrimSpace(e.Ref); ref != "" {
		s += " — " + ref
	}
	if who := strings.TrimSpace(e.BlockedOn); who != "" {
		s += " — BLOCKED: " + who
		if why := strings.TrimSpace(e.Why); why != "" {
			s += " (" + why + ")"
		}
	}
	return s
}

// List is one checklist page.
type List struct {
	// Path is the file it was read from.
	Path string
	// Name is the filename without its extension — the datetime stamp and the
	// slug, which is how a person refers to it.
	Name string
	// Title is the page's heading, and About the line under it. Both are what
	// the page says, not a second copy kept anywhere else.
	Title string
	About string
	// Head is every line before the first checkbox, kept verbatim so `show` can
	// print the page rather than a reconstruction of it.
	Head  []string
	Items []Item
}

// Slug is the name without its datetime stamp.
func (l List) Slug() string {
	if _, rest, ok := strings.Cut(l.Name, " "); ok {
		return rest
	}
	return l.Name
}

// Counts is what `ls` reports: how much of a page is left, and who it is
// waiting on.
type Counts struct {
	Total, Done, Blocked int
	// Blockers are the distinct names the open items are waiting on, sorted.
	Blockers []string
}

// Open reports how many items are not yet ticked.
func (c Counts) Open() int { return c.Total - c.Done }

// Line is the one-line summary of a page. It is deliberately the whole answer:
// everything past "2/5 done, 2 blocked on operator" is detail somebody can go
// and read.
func (c Counts) Line() string {
	s := fmt.Sprintf("%d/%d done", c.Done, c.Total)
	switch {
	case c.Blocked == 0:
	case len(c.Blockers) == 1:
		s += fmt.Sprintf(", %d blocked on %s", c.Blocked, c.Blockers[0])
	default:
		s += fmt.Sprintf(", %d blocked (%s)", c.Blocked, strings.Join(c.Blockers, ", "))
	}
	return s
}

// Counts summarises a page. Only open items count as blocked: an item that is
// ticked was not, in the end, waiting on anybody.
func (l List) Counts() Counts {
	c := Counts{Total: len(l.Items)}
	seen := map[string]bool{}
	for _, it := range l.Items {
		if it.Done {
			c.Done++
			continue
		}
		if it.Blocked() {
			c.Blocked++
			if !seen[it.BlockedOn] {
				seen[it.BlockedOn] = true
				c.Blockers = append(c.Blockers, it.BlockedOn)
			}
		}
	}
	sort.Strings(c.Blockers)
	return c
}

// itemLine matches a checkbox at the start of a line, after optional indent.
//
// `*` and `+` are accepted alongside `-` because Obsidian renders all three and
// editors differ about which they insert. An item bermuda cannot see is not a
// missing feature, it is a count that quietly lies — a page reporting 2/4 when
// four of six are done is worse than one that refuses to parse.
var itemLine = regexp.MustCompile(`^([ \t]*)([-*+]) \[([ xX])\](.*)$`)

// blockedTail matches the marker at the end of an item.
//
// Every dash is accepted because every dash gets typed: `—` is what bermuda
// writes, and `-` or `--` is what somebody adding a line by hand in a terminal
// produces. The separator is part of the match so the text in front of it comes
// back without a dangling dash on the end.
//
// It is anchored, so an item whose own text contains the word — "explain
// BLOCKED: to the reviewer" — is not read as a blocked item.
var blockedTail = regexp.MustCompile(`\s*(?:—|-{1,2})?\s*BLOCKED:\s*([^\s(]+)\s*(?:\(([^)]*)\))?\s*$`)

// New writes a page and returns it.
//
// The filename is the timestamp and a slug of the title, so the folder sorts
// itself. An existing file is refused rather than appended to: two pages made
// in the same minute with the same title is a retry, and silently merging them
// would put one piece of work's items on another's page.
func New(dir, title, about string, now time.Time) (List, error) {
	t := strings.TrimSpace(title)
	if t == "" {
		return List{}, errors.New("a checklist needs a title: what piece of work is this")
	}
	slug := Slugify(t)
	if slug == "" {
		return List{}, fmt.Errorf("%q has nothing in it a filename can be made of", title)
	}
	if err := os.MkdirAll(dir, statefs.Dir); err != nil {
		return List{}, err
	}
	name := now.Format(stamp) + " " + slug
	path := filepath.Join(dir, name+Ext)
	if _, err := os.Stat(path); err == nil {
		return List{}, fmt.Errorf("checklist %s already exists at %s", name, path)
	}

	// The trailing blank line is load-bearing: the first `add` appends after it,
	// so the items start one line below the description rather than glued to it.
	body := "# " + t + "\n"
	if a := strings.TrimSpace(about); a != "" {
		body += a + "\n"
	}
	body += "\n"
	if err := os.WriteFile(path, []byte(body), statefs.File); err != nil {
		return List{}, err
	}
	return Load(path)
}

// Load reads one page.
func Load(path string) (List, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path is resolved from the checklist folder
	if err != nil {
		return List{}, err
	}
	return parse(path, data), nil
}

// parse reads a page's bytes into a List.
//
// It records byte offsets as it goes rather than re-finding the line later,
// because "the fourth checkbox" and "the byte at 312" have to be the same thing
// for ticking to be safe.
func parse(path string, data []byte) List {
	l := List{Path: path, Name: strings.TrimSuffix(filepath.Base(path), Ext)}
	var offset int64
	for _, line := range strings.SplitAfter(string(data), "\n") {
		body := strings.TrimSuffix(line, "\n")
		if m := itemLine.FindStringSubmatch(body); m != nil {
			it := Item{
				Index: len(l.Items) + 1,
				Done:  m[3] != " ",
				// len("- [") past the indent, and the marker and the brackets are
				// one byte each in every encoding this file can be in.
				Offset: offset + int64(len(m[1])) + 3,
			}
			it.Text, it.BlockedOn, it.Why = splitBlocked(strings.TrimSpace(m[4]))
			l.Items = append(l.Items, it)
		} else if len(l.Items) == 0 {
			l.Head = append(l.Head, body)
		}
		offset += int64(len(line))
	}
	// SplitAfter on a file ending in a newline yields a trailing empty string.
	if n := len(l.Head); n > 0 && l.Head[n-1] == "" && len(l.Items) == 0 {
		l.Head = l.Head[:n-1]
	}
	l.Title, l.About = titleAndAbout(l.Head)
	return l
}

// titleAndAbout reads the heading and the line under it out of a page's head.
func titleAndAbout(head []string) (title, about string) {
	for _, line := range head {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if title == "" {
			title = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			continue
		}
		return title, trimmed
	}
	return title, ""
}

// splitBlocked pulls the BLOCKED marker off the end of an item's body.
func splitBlocked(body string) (text, who, why string) {
	loc := blockedTail.FindStringSubmatchIndex(body)
	if loc == nil {
		return body, "", ""
	}
	// Submatch pairs are (start, end) and -1 when the group did not
	// participate, which is the case for a blocker given with no reason.
	group := func(n int) string {
		if loc[2*n] < 0 {
			return ""
		}
		return strings.TrimSpace(body[loc[2*n]:loc[2*n+1]])
	}
	return strings.TrimSpace(body[:loc[0]]), group(1), group(2)
}

// All reads every checklist in a directory, oldest first.
//
// A page that cannot be read comes back as an error alongside the ones that
// can, rather than failing the listing: one unreadable file must not make the
// other nine invisible, and the unreadable one is exactly what the reader needs
// told.
func All(dir string) ([]List, []error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	var out []List
	var bad []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), Ext) {
			continue
		}
		l, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			bad = append(bad, err)
			continue
		}
		out = append(out, l)
	}
	// By name, which is by datetime, which is the order they were started in.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, bad
}

// Resolve works out which page a command is about: the query, then $BERMUDA_CHECK,
// then the most recent one.
//
// Defaulting to the most recent is the same bet `--thread` makes and for the
// same reason: an agent is working on one thing, and a flag it has to repeat on
// every call is a flag it eventually forgets. Guessing wrong here is cheap and
// visible — the page is named in every reply — where guessing an identity wrong
// is not.
func Resolve(dir, query string) (List, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		q = strings.TrimSpace(os.Getenv(Env))
	}
	// A path is a legitimate answer and the one $BERMUDA_CHECK is most likely to
	// hold, since a page's path is what `check new` prints.
	if q != "" && (strings.ContainsRune(q, filepath.Separator) || strings.HasSuffix(q, Ext)) {
		if _, err := os.Stat(q); err == nil {
			return Load(q)
		}
	}

	lists, bad := All(dir)
	if len(lists) == 0 {
		if len(bad) > 0 {
			return List{}, bad[0]
		}
		return List{}, fmt.Errorf("no checklists in %s — `bermuda check new \"<what you are doing>\"` starts one", dir)
	}
	if q == "" {
		return lists[len(lists)-1], nil
	}

	// Tried most specific first, and only the best tier that matched anything is
	// considered: a slug that is an exact name must not be called ambiguous
	// because it is also a substring of two others.
	lower := strings.ToLower(q)
	tiers := [][]List{{}, {}, {}, {}}
	for _, l := range lists {
		name, slug := strings.ToLower(l.Name), strings.ToLower(l.Slug())
		switch {
		case name == lower || slug == lower:
			tiers[0] = append(tiers[0], l)
		case strings.HasPrefix(name, lower) || strings.HasPrefix(slug, lower):
			tiers[1] = append(tiers[1], l)
		case strings.Contains(name, lower) || strings.Contains(slug, lower):
			tiers[2] = append(tiers[2], l)
		case strings.Contains(strings.ToLower(l.Title), lower):
			tiers[3] = append(tiers[3], l)
		}
	}
	for _, tier := range tiers {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			names := make([]string, 0, len(tier))
			for _, l := range tier {
				names = append(names, l.Name)
			}
			return List{}, fmt.Errorf("%d checklists match %q: %s — name one of them",
				len(tier), q, strings.Join(names, ", "))
		}
	}
	return List{}, fmt.Errorf("no checklist matches %q — `bermuda check ls` says which there are", q)
}

// Find picks one item out of a page by 1-based number or by the start of its
// text.
func (l List) Find(selector string) (Item, error) {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return Item{}, errors.New("which item? a number from `bermuda check show`, or the start of its text")
	}
	if n, err := strconv.Atoi(sel); err == nil {
		if n < 1 || n > len(l.Items) {
			return Item{}, fmt.Errorf("%s has %d items, so there is no item %d", l.Name, len(l.Items), n)
		}
		return l.Items[n-1], nil
	}

	lower := strings.ToLower(sel)
	var prefix, contains []Item
	for _, it := range l.Items {
		text := strings.ToLower(it.Text)
		switch {
		case strings.HasPrefix(text, lower):
			prefix = append(prefix, it)
		case strings.Contains(text, lower):
			contains = append(contains, it)
		}
	}
	for _, tier := range [][]Item{prefix, contains} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			var lines []string
			for _, it := range tier {
				lines = append(lines, fmt.Sprintf("%d. %s", it.Index, it.Text))
			}
			return Item{}, fmt.Errorf("%d items in %s match %q: %s — use its number",
				len(tier), l.Name, sel, strings.Join(lines, "; "))
		}
	}
	return Item{}, fmt.Errorf("no item in %s starts with %q — `bermuda check show %s` lists them, "+
		"and if you meant a checklist rather than an item, name both: bermuda check tick <list> <n|prefix>",
		l.Name, sel, l.Name)
}

// Set ticks or unticks one item, and reports whether it changed anything.
//
// It writes the one byte between the brackets and nothing else, so a page open
// in somebody's editor is not rewritten under them and a `git diff` of the vault
// shows the one line that moved. The page is re-read here rather than trusted
// from the caller's copy, because the offset has to be the offset in the file as
// it is now — a human who added two lines since it was loaded would otherwise
// have a different item ticked than the one that was named.
func Set(path, selector string, done bool) (Item, bool, error) {
	l, err := Load(path)
	if err != nil {
		return Item{}, false, err
	}
	it, err := l.Find(selector)
	if err != nil {
		return Item{}, false, err
	}
	if it.Done == done {
		return it, false, nil
	}
	mark := byte(' ')
	if done {
		mark = 'x'
	}
	f, err := os.OpenFile(path, os.O_WRONLY, statefs.File) // #nosec G304 -- resolved from the checklist folder
	if err != nil {
		return Item{}, false, err
	}
	if _, err := f.WriteAt([]byte{mark}, it.Offset); err != nil {
		f.Close()
		return Item{}, false, err
	}
	it.Done = done
	// Closed rather than deferred, because its error is the one that says the
	// byte did not reach the disk and a tick nobody can see is the whole failure
	// this feature exists to stop.
	return it, true, f.Close()
}

// Add appends one item to a page.
//
// It goes after the last existing item rather than at the end of the file, so
// notes a person wrote under the list stay under it. This one does rewrite the
// file — an insert has to — which is why ticking, the verb that runs while
// somebody has the page open, does not.
func Add(path string, e Entry) (Item, error) {
	if strings.TrimSpace(e.Text) == "" {
		return Item{}, errors.New("an item needs text: what is the thing that has to happen")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- resolved from the checklist folder
	if err != nil {
		return Item{}, err
	}
	line := e.Line() + "\n"
	at := insertAt(string(data))
	out := string(data[:at]) + line + string(data[at:])
	if err := os.WriteFile(path, []byte(out), statefs.File); err != nil {
		return Item{}, err
	}
	l := parse(path, []byte(out))
	if len(l.Items) == 0 {
		return Item{}, fmt.Errorf("%s: wrote an item the page does not read back", path)
	}
	// Which item it became is worth returning rather than assuming: the caller
	// prints its number, and appending after the last item is not the same as
	// appending last if a human left a stray checkbox further down.
	for _, it := range l.Items {
		if it.Offset >= int64(at) {
			return it, nil
		}
	}
	return l.Items[len(l.Items)-1], nil
}

// insertAt finds the byte offset a new item should be written at: just past the
// last existing item's line, or the end of the file when there are none.
func insertAt(data string) int {
	var offset, last int
	found := false
	for _, line := range strings.SplitAfter(data, "\n") {
		if itemLine.MatchString(strings.TrimSuffix(line, "\n")) {
			last, found = offset+len(line), true
		}
		offset += len(line)
	}
	if found {
		return last
	}
	return len(data)
}

// Ensure adds an item only if the page does not already have one with that
// text, and reports whether it added it.
//
// This is what lets a flow declare its steps as checklist items without
// duplicating them on every resume: the second run of a step finds its item
// already there and ticks that one.
func Ensure(path string, e Entry) (Item, bool, error) {
	l, err := Load(path)
	if err != nil {
		return Item{}, false, err
	}
	want := strings.ToLower(strings.TrimSpace(e.Text))
	for _, it := range l.Items {
		if strings.ToLower(it.Text) == want {
			return it, false, nil
		}
	}
	it, err := Add(path, e)
	return it, err == nil, err
}

// slugPattern is every run of characters a filename slug may not contain.
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a title into the filename half of a page's name.
func Slugify(title string) string {
	return strings.Trim(slugPattern.ReplaceAllString(strings.ToLower(title), "-"), "-")
}
