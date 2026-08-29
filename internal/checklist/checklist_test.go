package checklist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What these hold the package to.
//
// Two promises do the work. The first is that a page is *just a page*: a human
// with Obsidian and no CLI must be able to read it, edit it, and hand it back,
// so the parser has to survive what a person actually types and the writer has
// to leave everything it did not mean to touch alone. The second is that a tick
// is one byte — that is what lets it happen while the page is open in somebody's
// editor, and it is invisible if it regresses, because a whole-file rewrite
// produces exactly the same content.

// page writes a checklist file directly, so a test can start from bytes a human
// would have typed rather than from ones this package produced.
func page(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "2026-08-28T1631 ship-640-fix.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const sample = `# ship 640 fix
timezone handling in the export path, webapp

- [x] open PR into dev — https://github.com/org/repo/pull/474
- [x] open PR into release-42 — #475
- [ ] human approving review on 474 — BLOCKED: operator (agent has admin:false, rulesets)
- [ ] merge release-42 — BLOCKED: operator (auto-deploys to QA, breaks it until 812 ships)
- [ ] client-side date picker — tracker#812
`

func TestLoadReadsTheHeadTheItemsAndWhoIsBlocking(t *testing.T) {
	l, err := Load(page(t, sample))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if l.Title != "ship 640 fix" {
		t.Errorf("title = %q", l.Title)
	}
	if !strings.HasPrefix(l.About, "timezone handling") {
		t.Errorf("about = %q", l.About)
	}
	if l.Name != "2026-08-28T1631 ship-640-fix" || l.Slug() != "ship-640-fix" {
		t.Errorf("name = %q, slug = %q", l.Name, l.Slug())
	}
	if len(l.Items) != 5 {
		t.Fatalf("read %d items, want 5", len(l.Items))
	}
	if !l.Items[0].Done || l.Items[2].Done {
		t.Error("the ticked and unticked items did not come back as written")
	}
	// The reference stays in the text, deliberately: it was written there and
	// guessing where a URL ends is not this package's job.
	if want := "open PR into dev — https://github.com/org/repo/pull/474"; l.Items[0].Text != want {
		t.Errorf("item 1 text = %q, want %q", l.Items[0].Text, want)
	}
	blocked := l.Items[3]
	if blocked.BlockedOn != "operator" {
		t.Errorf("blocked on %q, want operator", blocked.BlockedOn)
	}
	if blocked.Why != "auto-deploys to QA, breaks it until 812 ships" {
		t.Errorf("why = %q", blocked.Why)
	}
	if blocked.Text != "merge release-42" {
		t.Errorf("blocked item text = %q, want the marker stripped off", blocked.Text)
	}
}

// A page is edited by hand, in an editor that may not insert the same bullet
// this package writes. An item bermuda cannot see is not a missing feature: it
// is a count that quietly lies about how much is left.
func TestLoadReadsWhatAHumanActuallyTypes(t *testing.T) {
	l, err := Load(page(t, `# hand written

* [X] uppercase tick with a star
+ [ ] plus bullet
  - [ ] indented -- BLOCKED: ci
- [ ] explain BLOCKED: to the reviewer
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(l.Items) != 4 {
		t.Fatalf("read %d items, want 4", len(l.Items))
	}
	if !l.Items[0].Done {
		t.Error("[X] did not read as ticked")
	}
	if l.Items[2].BlockedOn != "ci" {
		t.Errorf("`-- BLOCKED: ci` read as %q", l.Items[2].BlockedOn)
	}
	if l.Items[2].Text != "indented" {
		t.Errorf("indented item text = %q", l.Items[2].Text)
	}
	// Anchored: the word in the middle of a sentence is not a blocker.
	if l.Items[3].Blocked() {
		t.Errorf("an item mentioning the word read as blocked on %q", l.Items[3].BlockedOn)
	}
}

// The promise the whole design rests on: ticking touches one byte, so a page
// open in somebody's editor is not rewritten under them.
func TestSetWritesOneByteAndLeavesTheRestOfTheFileAlone(t *testing.T) {
	path := page(t, sample)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	it, changed, err := Set(path, "merge release-42", true)
	if err != nil || !changed {
		t.Fatalf("tick: changed=%v err=%v", changed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("the file changed length: %d -> %d", len(before), len(after))
	}
	var diffs []int
	for i := range before {
		if before[i] != after[i] {
			diffs = append(diffs, i)
		}
	}
	if len(diffs) != 1 {
		t.Fatalf("%d bytes changed, want exactly 1 (at %v)", len(diffs), diffs)
	}
	if int64(diffs[0]) != it.Offset || after[diffs[0]] != 'x' {
		t.Errorf("byte %d became %q, want %q at offset %d", diffs[0], after[diffs[0]], "x", it.Offset)
	}
}

// Ticking twice is not an error and is not a second tick. An agent that ran the
// same command an hour apart should be able to tell which it did.
func TestSetReportsAnItemThatWasAlreadyInThatState(t *testing.T) {
	path := page(t, sample)
	if _, changed, err := Set(path, "1", true); err != nil || changed {
		t.Errorf("re-ticking a ticked item: changed=%v err=%v", changed, err)
	}
	if _, changed, err := Set(path, "1", false); err != nil || !changed {
		t.Fatalf("untick: changed=%v err=%v", changed, err)
	}
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Items[0].Done {
		t.Error("untick left the item ticked")
	}
}

func TestFindTakesANumberOrThestartOfTheText(t *testing.T) {
	l, err := Load(page(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if it, err := l.Find("3"); err != nil || it.Index != 3 {
		t.Errorf("Find(3) = %v, %v", it.Index, err)
	}
	if it, err := l.Find("client-side"); err != nil || it.Index != 5 {
		t.Errorf("Find(client-side) = %v, %v", it.Index, err)
	}
	// Two items start with "open PR", so picking one of them silently would tick
	// the wrong artifact. The refusal has to name both.
	_, err = l.Find("open PR")
	if err == nil || !strings.Contains(err.Error(), "2 items") {
		t.Errorf("an ambiguous prefix returned %v, want a refusal naming the candidates", err)
	}
	if _, err := l.Find("9"); err == nil || !strings.Contains(err.Error(), "5 items") {
		t.Errorf("Find(9) returned %v, want an error saying how many there are", err)
	}
}

func TestAddPutsTheItemAfterTheLastOneAndKeepsWhatIsBelow(t *testing.T) {
	path := page(t, sample+"\nNotes a person wrote under the list.\n")
	it, err := Add(path, Entry{
		Text: "client-side date picker rollback", Ref: "tracker#813",
		BlockedOn: "operator", Why: "needs a release window",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if it.Index != 6 {
		t.Errorf("the new item is number %d, want 6", it.Index)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	want := "- [ ] client-side date picker rollback — tracker#813 — BLOCKED: operator (needs a release window)\n"
	if !strings.Contains(body, want) {
		t.Errorf("page does not contain the written line:\n%s", body)
	}
	if !strings.HasSuffix(body, "Notes a person wrote under the list.\n") {
		t.Errorf("the item was appended past a person's notes:\n%s", body)
	}
	// And it reads back as what was written, marker and all.
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	added := l.Items[5]
	if added.BlockedOn != "operator" || added.Why != "needs a release window" {
		t.Errorf("round trip lost the blocker: %+v", added)
	}
	if added.Text != "client-side date picker rollback — tracker#813" {
		t.Errorf("round-tripped text = %q", added.Text)
	}
}

func TestAddOnAPageWithNoItemsYet(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "ship 640 fix", "the first one", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if base := filepath.Base(l.Path); base != "2026-08-28T1631 ship-640-fix.md" {
		t.Errorf("wrote %q, want the datetime-prefixed name the folder sorts by", base)
	}
	if _, err := Add(l.Path, Entry{Text: "open PR into dev"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	reread, err := Load(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.Items) != 1 || reread.Title != "ship 640 fix" || reread.About != "the first one" {
		t.Errorf("page after the first add: %+v", reread)
	}
	// A second page with the same title in the same minute is a retry, and
	// merging the two pieces of work would be worse than refusing.
	if _, err := New(dir, "ship 640 fix", "", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC)); err == nil {
		t.Error("New over an existing page succeeded, want a refusal")
	}
}

// Ensure is what stops a resumed flow writing a second copy of every step.
func TestEnsureAddsOnceAndThenFindsWhatIsThere(t *testing.T) {
	path := page(t, sample)
	it, added, err := Ensure(path, Entry{Text: "verify on QA"})
	if err != nil || !added || it.Index != 6 {
		t.Fatalf("first ensure: item=%d added=%v err=%v", it.Index, added, err)
	}
	again, added, err := Ensure(path, Entry{Text: "Verify on QA"})
	if err != nil || added {
		t.Fatalf("second ensure: added=%v err=%v", added, err)
	}
	if again.Index != it.Index {
		t.Errorf("second ensure found item %d, want the one already there (%d)", again.Index, it.Index)
	}
}

func TestCountsAreTheWholeAnswerInOneLine(t *testing.T) {
	l, err := Load(page(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := l.Counts().Line(), "2/5 done, 2 blocked on operator"; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}

	// Several blockers are named rather than collapsed: "3 blocked" with no
	// names is a line that cannot be acted on.
	two, err := Load(page(t, `# two blockers

- [ ] a — BLOCKED: operator
- [ ] b — BLOCKED: ci
- [x] c — BLOCKED: operator
`))
	if err != nil {
		t.Fatal(err)
	}
	// The ticked item is not counted as blocked: it was not, in the end,
	// waiting on anybody.
	if got, want := two.Counts().Line(), "1/3 done, 2 blocked (ci, operator)"; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

func TestResolveDefaultsToTheEnvironmentThenTheMostRecent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(Env, "")
	old, err := New(dir, "older work", "", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	recent, err := New(dir, "ship 640 fix", "", time.Date(2026, 8, 28, 16, 31, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	if l, err := Resolve(dir, ""); err != nil || l.Name != recent.Name {
		t.Errorf("no query resolved to %q (%v), want the most recent", l.Name, err)
	}
	// $BERMUDA_CHECK is what a flow step is given, and what it holds is a path.
	t.Setenv(Env, old.Path)
	if l, err := Resolve(dir, ""); err != nil || l.Name != old.Name {
		t.Errorf("$%s resolved to %q (%v), want %q", Env, l.Name, err, old.Name)
	}
	// An explicit query still wins over the environment.
	if l, err := Resolve(dir, "ship-640"); err != nil || l.Name != recent.Name {
		t.Errorf("prefix resolved to %q (%v)", l.Name, err)
	}
	// And so does the title, for whoever remembers the words and not the slug.
	if l, err := Resolve(dir, "older"); err != nil || l.Name != old.Name {
		t.Errorf("title match resolved to %q (%v)", l.Name, err)
	}
	if _, err := Resolve(dir, "nothing like this"); err == nil {
		t.Error("a query matching nothing resolved to something")
	}
}

func TestResolveRefusesAnAmbiguousQueryRatherThanPickingOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(Env, "")
	for i, title := range []string{"ship the fix", "ship the docs"} {
		if _, err := New(dir, title, "", time.Date(2026, 8, 28, 16, 30+i, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Resolve(dir, "ship")
	if err == nil || !strings.Contains(err.Error(), "2 checklists match") {
		t.Errorf("an ambiguous query returned %v, want a refusal naming both", err)
	}
	// An exact slug is not ambiguous just because it is also a prefix of
	// another page's name.
	if l, err := Resolve(dir, "ship-the-fix"); err != nil || l.Slug() != "ship-the-fix" {
		t.Errorf("exact slug resolved to %q (%v)", l.Slug(), err)
	}
}

func TestResolveSaysWhereToStartWhenThereAreNoneAtAll(t *testing.T) {
	t.Setenv(Env, "")
	_, err := Resolve(filepath.Join(t.TempDir(), "checklists"), "")
	if err == nil || !strings.Contains(err.Error(), "check new") {
		t.Errorf("resolving in an empty folder returned %v, want the command that starts one", err)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"ship 640 fix":            "ship-640-fix",
		"  Ship #640 -- the fix!": "ship-640-the-fix",
		"...":                     "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
