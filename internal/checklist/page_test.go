package checklist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What these hold the package to, beyond the parse-and-tick promises in
// checklist_test.go.
//
// Two more things a caller depends on. The first is that a line bermuda writes
// is a line bermuda reads: `check add --blocked-on operator --why '...'` and
// `check show` are the two halves of one round trip, and a writer that emits a
// separator the parser does not accept loses the blocker silently — the item
// still lists, so nothing looks broken, and "blocked on operator" quietly
// becomes part of the item's own text. The second is that starting a page is
// refused rather than fudged when it cannot be named or already exists: both
// failures otherwise put one piece of work's items on another's page.

// TestEntryLineRoundTripsThroughTheParser is the writer and the reader checked
// against each other. Every field a caller can set has to survive being written
// to the page and read back off it.
func TestEntryLineRoundTripsThroughTheParser(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
		text  string // what Item.Text should read back as
	}{
		{
			name:  "text only",
			entry: Entry{Text: "client-side date picker"},
			text:  "client-side date picker",
		},
		{
			// The reference becomes part of the text on purpose: the line stays
			// one readable sentence, and nothing tries to split a URL back out
			// of prose a human is free to edit.
			name:  "text and reference",
			entry: Entry{Text: "open PR into dev", Ref: "https://github.com/acme/widget/pull/474"},
			text:  "open PR into dev — https://github.com/acme/widget/pull/474",
		},
		{
			name:  "blocked with a reason",
			entry: Entry{Text: "merge release-42", BlockedOn: "operator", Why: "auto-deploys to QA"},
			text:  "merge release-42",
		},
		{
			// A blocker with no reason given: the parenthesised group does not
			// participate, which is the case that reads back as a stray "()"
			// or an empty blocker if the submatch indices are mishandled.
			name:  "blocked with no reason",
			entry: Entry{Text: "human approving review", BlockedOn: "operator"},
			text:  "human approving review",
		},
		{
			name:  "reference and blocker together",
			entry: Entry{Text: "merge release-42", Ref: "tracker#812", BlockedOn: "ci", Why: "red on main"},
			text:  "merge release-42 — tracker#812",
		},
		{
			// Surrounding whitespace is the caller's, not the page's.
			name:  "whitespace is trimmed off every field",
			entry: Entry{Text: "  verify on QA  ", Ref: " #475 ", BlockedOn: " operator ", Why: " needs the key "},
			text:  "verify on QA — #475",
		},
		{
			name:  "an item that starts out already done",
			entry: Entry{Text: "open PR into dev", Done: true},
			text:  "open PR into dev",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := parse("2026-08-28T1631 round-trip.md", []byte(tc.entry.Line()+"\n"))
			if len(l.Items) != 1 {
				t.Fatalf("%q parsed into %d items, want 1", tc.entry.Line(), len(l.Items))
			}
			it := l.Items[0]
			if it.Text != tc.text {
				t.Errorf("Text = %q, want %q (line was %q)", it.Text, tc.text, tc.entry.Line())
			}
			if want := strings.TrimSpace(tc.entry.BlockedOn); it.BlockedOn != want {
				t.Errorf("BlockedOn = %q, want %q (line was %q)", it.BlockedOn, want, tc.entry.Line())
			}
			if want := strings.TrimSpace(tc.entry.Why); it.Why != want {
				t.Errorf("Why = %q, want %q (line was %q)", it.Why, want, tc.entry.Line())
			}
			if it.Done != tc.entry.Done {
				t.Errorf("Done = %v, want %v (line was %q)", it.Done, tc.entry.Done, tc.entry.Line())
			}
			if it.Blocked() != (strings.TrimSpace(tc.entry.BlockedOn) != "") {
				t.Errorf("Blocked() = %v for line %q", it.Blocked(), tc.entry.Line())
			}
		})
	}
}

// An item printed by `check show` and pasted back into the page has to come
// back as the same item. Item.String is the display half of the same contract
// Entry.Line writes, so the two must not drift apart.
func TestItemStringIsReadBackAsTheSameItem(t *testing.T) {
	l, err := Load(page(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range l.Items {
		round := parse(l.Path, []byte("- "+want.Box()+" "+want.String()+"\n"))
		if len(round.Items) != 1 {
			t.Fatalf("%q parsed into %d items, want 1", want.String(), len(round.Items))
		}
		got := round.Items[0]
		if got.Text != want.Text || got.BlockedOn != want.BlockedOn || got.Why != want.Why || got.Done != want.Done {
			t.Errorf("item %d did not survive String(): got %+v, want text=%q blocked=%q why=%q done=%v",
				want.Index, got, want.Text, want.BlockedOn, want.Why, want.Done)
		}
	}
}

// The checkbox a page shows is the one the parser reads. `[x]` and `[ ]` are
// the two states, and nothing else.
func TestBoxMatchesTheParsedState(t *testing.T) {
	for _, done := range []bool{true, false} {
		box := Item{Done: done}.Box()
		l := parse("p.md", []byte("- "+box+" a thing\n"))
		if len(l.Items) != 1 {
			t.Fatalf("%q is not a checkbox the parser accepts", box)
		}
		if l.Items[0].Done != done {
			t.Errorf("Box() for Done=%v rendered %q, which reads back as Done=%v", done, box, l.Items[0].Done)
		}
	}
}

// Open is what "is it done yet" actually asks, and it is Total minus Done —
// not Total minus Blocked, which would report a page of open unblocked work as
// finished.
func TestCountsOpenIsWhatIsNotTickedYet(t *testing.T) {
	l, err := Load(page(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	c := l.Counts()
	if got, want := c.Open(), c.Total-c.Done; got != want {
		t.Errorf("Open() = %d, want %d", got, want)
	}
	if c.Open() != 3 {
		t.Errorf("Open() = %d on the sample page, want 3", c.Open())
	}
	if empty := (Counts{}); empty.Open() != 0 {
		t.Errorf("Open() on an empty page = %d, want 0", empty.Open())
	}
}

// New refuses rather than guessing. Both of these produce a page under a name
// that is not the one asked for, and the items of one piece of work then land
// on another's page.
func TestNewRefusesAPageItCannotName(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		title string
	}{
		{"empty title", ""},
		{"only whitespace", "   \t "},
		{"nothing a filename can be made of", "### !!! ###"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(dir, tc.title, "why", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC)); err == nil {
				t.Fatalf("New(%q) succeeded, want an error", tc.title)
			}
		})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("New wrote %d files while refusing every title", len(entries))
	}
}

// Two pages made in the same minute with the same title is a retry, and merging
// them silently is how one run's items end up on another's page.
func TestNewRefusesToReuseAnExistingPage(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC)
	first, err := New(dir, "ship 640 fix", "timezone handling", at)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := New(dir, "ship 640 fix", "something else", at); err == nil {
		t.Fatal("New over an existing page succeeded, want an error")
	}
	// And the page that was there is untouched.
	again, err := Load(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if again.About != "timezone handling" {
		t.Errorf("About = %q after the refused New, want the original %q", again.About, "timezone handling")
	}
}

// The page New writes is the page a human opens: a heading, the description
// under it, and room for the first item to land below rather than glued to the
// description.
func TestNewWritesAPageThatReadsBackAsItself(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "Ship 640 fix", "  timezone handling in the export path  ",
		time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := "2026-08-28T1631 ship-640-fix"; l.Name != want {
		t.Errorf("Name = %q, want %q", l.Name, want)
	}
	if l.Slug() != "ship-640-fix" {
		t.Errorf("Slug() = %q, want %q", l.Slug(), "ship-640-fix")
	}
	if l.Title != "Ship 640 fix" {
		t.Errorf("Title = %q, want %q", l.Title, "Ship 640 fix")
	}
	if l.About != "timezone handling in the export path" {
		t.Errorf("About = %q, want the trimmed description", l.About)
	}
	if len(l.Items) != 0 {
		t.Errorf("a fresh page has %d items, want 0", len(l.Items))
	}
	if got := filepath.Dir(l.Path); got != dir {
		t.Errorf("page written to %q, want it in %q", got, dir)
	}

	// The first item lands under the description, not appended to it.
	if _, err := Add(l.Path, Entry{Text: "open PR into dev"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "export path- [ ]") {
		t.Errorf("the first item was glued to the description:\n%s", data)
	}
	back, err := Load(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if back.About != l.About {
		t.Errorf("About = %q after the first Add, want %q", back.About, l.About)
	}
}

// New creates the folder. `check new` on a machine whose vault has no
// checklists folder yet is the first thing anybody runs.
func TestNewCreatesTheFolder(t *testing.T) {
	dir := Dir(filepath.Join(t.TempDir(), "memory"))
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the folder already existed")
	}
	if _, err := New(dir, "ship 640 fix", "", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC)); err != nil {
		t.Fatalf("New into a folder that does not exist: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("folder not created: %v", err)
	}
}

// Dir is where every command looks, so it has to be the same place New writes
// to and All reads from.
func TestDirIsTheFolderInsideTheMemoryDirectory(t *testing.T) {
	if got, want := Dir("/vault/memory"), filepath.Join("/vault/memory", FolderName); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

// A name with no datetime stamp in front of it is still a name, and Slug must
// return it rather than the empty string — a page a human created by hand is
// still a page.
func TestSlugOfANameWithNoStamp(t *testing.T) {
	if got := (List{Name: "ship-640-fix"}).Slug(); got != "ship-640-fix" {
		t.Errorf("Slug() = %q, want %q", got, "ship-640-fix")
	}
}

// All is what `check ls` prints. Order is the order work was started in, and
// what is in the folder that is not a page is not a page.
func TestAllReadsEveryPageOldestFirst(t *testing.T) {
	dir := t.TempDir()
	for _, at := range []time.Time{
		time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC),
	} {
		if _, err := New(dir, "work "+at.Format("0102"), "", at); err != nil {
			t.Fatal(err)
		}
	}
	// Things a vault folder actually contains: a note that is not a checklist
	// page, and a directory.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a page"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "archive.md"), 0o700); err != nil {
		t.Fatal(err)
	}

	lists, bad := All(dir)
	if len(bad) != 0 {
		t.Fatalf("All reported %v", bad)
	}
	var names []string
	for _, l := range lists {
		names = append(names, l.Name)
	}
	want := []string{"2026-08-27T0900 work-0827", "2026-08-28T1631 work-0828", "2026-08-29T0900 work-0829"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("All returned %v, want %v", names, want)
	}
}

// A folder that does not exist is not an error: it is a machine with no
// checklists yet, and `check ls` there should say so rather than fail.
func TestAllOnAFolderThatDoesNotExist(t *testing.T) {
	lists, bad := All(filepath.Join(t.TempDir(), "nothing-here"))
	if len(lists) != 0 || len(bad) != 0 {
		t.Errorf("All on a missing folder = %v, %v; want nothing and no error", lists, bad)
	}
}

// One unreadable page must not make the other pages invisible — and it must
// still be reported, because the unreadable one is exactly what the reader
// needs told.
func TestAllReportsAnUnreadablePageWithoutHidingTheRest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file, so there is nothing to fail on")
	}
	dir := t.TempDir()
	good, err := New(dir, "ship 640 fix", "", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(dir, "2026-08-29T0900 locked-away.md")
	if err := os.WriteFile(unreadable, []byte("# nope\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	lists, bad := All(dir)
	if len(bad) != 1 {
		t.Fatalf("All reported %d errors, want 1", len(bad))
	}
	if !strings.Contains(bad[0].Error(), "locked-away") {
		t.Errorf("the error does not name the page that could not be read: %v", bad[0])
	}
	if len(lists) != 1 || lists[0].Name != good.Name {
		t.Errorf("All returned %v, want just the readable page %q", lists, good.Name)
	}
}

// Resolve takes a path, which is what `check new` prints and what
// $BERMUDA_CHECK is most likely to hold.
func TestResolveTakesAPathAsWellAsAName(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "ship 640 fix", "", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere")
	if _, err := New(other, "different work", "", time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(Env, "")

	// A path resolves even when it is not in the folder being listed.
	got, err := Resolve(other, l.Path)
	if err != nil {
		t.Fatalf("Resolve by path: %v", err)
	}
	if got.Name != l.Name {
		t.Errorf("Resolve by path returned %q, want %q", got.Name, l.Name)
	}

	// And $BERMUDA_CHECK holding a path does the same.
	t.Setenv(Env, l.Path)
	got, err = Resolve(other, "")
	if err != nil {
		t.Fatalf("Resolve from %s: %v", Env, err)
	}
	if got.Name != l.Name {
		t.Errorf("Resolve from %s returned %q, want %q", Env, got.Name, l.Name)
	}
}

// A query that matches nothing says so, rather than falling back to the most
// recent page — ticking an item on a page nobody named is the one outcome worse
// than an error.
func TestResolveRefusesAQueryThatMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, "ship 640 fix", "", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(Env, "")
	if _, err := Resolve(dir, "something-else-entirely"); err == nil {
		t.Fatal("Resolve of an unmatched query succeeded, want an error")
	}
}

// Find says what is wrong rather than returning the nearest item: an off-by-one
// that ticks item 3 when 4 was asked for is invisible on the page.
func TestFindRefusesWhatItCannotIdentify(t *testing.T) {
	l, err := Load(page(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		selector string
	}{
		{"nothing at all", "  "},
		{"item zero", "0"},
		{"one past the end", "6"},
		{"negative", "-1"},
		{"text nothing starts with", "deploy to prod"},
		{"text two items start with", "open PR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it, err := l.Find(tc.selector)
			if err == nil {
				t.Fatalf("Find(%q) returned item %d, want an error", tc.selector, it.Index)
			}
		})
	}
}

// Find matches on a substring only when no item starts with the query: an
// exact prefix must not be called ambiguous because two other items happen to
// contain it.
func TestFindPrefersAPrefixOverASubstring(t *testing.T) {
	l, err := Load(page(t, `# tiers

- [ ] review the export path
- [ ] rewrite the review checklist
- [ ] annotate the review notes
`))
	if err != nil {
		t.Fatal(err)
	}
	it, err := l.Find("review")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if it.Index != 1 {
		t.Errorf("Find(%q) = item %d, want the one that starts with it (1)", "review", it.Index)
	}
}

// The number Add reports is the number `check tick` will accept, and prose a
// person wrote under the list stays under it. An index printed back that is not
// the one the page reads is how the wrong item gets ticked.
func TestAddReportsTheNumberThatTicksTheItem(t *testing.T) {
	path := page(t, `# ship 640 fix

- [x] open PR into dev
- [ ] merge release-42

## notes

QA needs the feature flag off before any of this is worth checking.
`)
	it, err := Add(path, Entry{Text: "verify on QA"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if it.Index != 3 || it.Text != "verify on QA" {
		t.Fatalf("Add returned item %d %q, want item 3 %q", it.Index, it.Text, "verify on QA")
	}

	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 3 {
		t.Fatalf("page has %d items, want 3", len(l.Items))
	}
	if l.Items[2].Text != "verify on QA" {
		t.Errorf("item 3 is %q, want the new one", l.Items[2].Text)
	}

	// The prose below the list is still below it, not above the new item.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Index(body, "verify on QA") > strings.Index(body, "## notes") {
		t.Errorf("the new item was written below the notes:\n%s", body)
	}
	if !strings.Contains(body, "QA needs the feature flag off") {
		t.Errorf("the notes were lost:\n%s", body)
	}

	// And the number Add reported is the number that ticks it.
	ticked, changed, err := Set(path, "3", true)
	if err != nil || !changed {
		t.Fatalf("Set(3): changed=%v err=%v", changed, err)
	}
	if ticked.Text != "verify on QA" {
		t.Errorf("ticking item 3 ticked %q", ticked.Text)
	}
}

// A checkbox a person left in their notes below the list is an item, because a
// page reporting 2/4 when four of six checkboxes are done is worse than one
// that counts everything it can see. Add goes after the last of them, and the
// index it reports is still the one that ticks it.
func TestAddCountsACheckboxLeftInTheNotes(t *testing.T) {
	path := page(t, `# ship 640 fix

- [x] open PR into dev
- [ ] merge release-42

## notes

- [ ] this one was typed under the notes
`)
	it, err := Add(path, Entry{Text: "verify on QA"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if it.Index != 4 || it.Text != "verify on QA" {
		t.Fatalf("Add returned item %d %q, want item 4 %q", it.Index, it.Text, "verify on QA")
	}
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Counts().Total; got != 4 {
		t.Errorf("Counts().Total = %d, want 4 — every checkbox on the page", got)
	}
	ticked, changed, err := Set(path, "4", true)
	if err != nil || !changed {
		t.Fatalf("Set(4): changed=%v err=%v", changed, err)
	}
	if ticked.Text != "verify on QA" {
		t.Errorf("ticking item 4 ticked %q", ticked.Text)
	}
}

// An item with no text is refused: a blank checkbox on the page is a line
// nobody can act on and one the counts still hold open forever.
func TestAddRefusesAnItemWithNoText(t *testing.T) {
	path := page(t, sample)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(path, Entry{Text: "   ", Ref: "#475"}); err == nil {
		t.Fatal("Add with no text succeeded, want an error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the page was written to by a refused Add")
	}
}

// The BLOCKED marker is anchored to the end of the line, so an item that talks
// about being blocked is not read as one. An item silently marked blocked stops
// `check ls` reporting the work as outstanding-and-actionable, and reads as an
// idle agent instead.
func TestBlockedMarkerIsOnlyReadAtTheEndOfTheLine(t *testing.T) {
	l, err := Load(page(t, `# wording

- [ ] explain BLOCKED: to the reviewer
- [ ] say why it is BLOCKED: operator
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Items) != 2 {
		t.Fatalf("page has %d items, want 2", len(l.Items))
	}
	if l.Items[0].Blocked() {
		t.Errorf("item 1 read as blocked on %q; its text is prose, not a marker", l.Items[0].BlockedOn)
	}
	if l.Items[0].Text != "explain BLOCKED: to the reviewer" {
		t.Errorf("item 1 text = %q, want the whole line", l.Items[0].Text)
	}
	// The one that really does end in a marker still parses as blocked.
	if !l.Items[1].Blocked() || l.Items[1].BlockedOn != "operator" {
		t.Errorf("item 2 = %+v, want blocked on operator", l.Items[1])
	}
}

// Set re-reads the page rather than trusting an offset the caller loaded
// earlier. A person who added a line in Obsidian since the load would otherwise
// have a different item ticked than the one that was named — and the tick looks
// perfectly successful.
func TestSetUsesTheFileAsItIsNowNotAsItWasLoaded(t *testing.T) {
	path := page(t, `# ship 640 fix

- [ ] merge release-42
`)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	// Somebody edits the page in their editor: a longer heading and a new first
	// item, both of which move every byte offset below them.
	if err := os.WriteFile(path, []byte(`# ship 640 fix, and the export path with it

- [ ] open PR into dev
- [ ] merge release-42
`), 0o600); err != nil {
		t.Fatal(err)
	}

	it, changed, err := Set(path, "merge release-42", true)
	if err != nil || !changed {
		t.Fatalf("Set: changed=%v err=%v", changed, err)
	}
	if it.Text != "merge release-42" {
		t.Errorf("Set ticked %q", it.Text)
	}
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Items[0].Done {
		t.Error("the item added since the load was ticked instead")
	}
	if !l.Items[1].Done {
		t.Error("the named item is not ticked")
	}
}
