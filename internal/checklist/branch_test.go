package checklist

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBranchCreationReusesIdentityAndLeavesLegacyAlone(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	legacy, err := New(dir, "legacy work", "keep this page", now)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(legacy.Path)
	first, err := NewBranch(dir, "branch work", "about", "acme/widget", "feat/status", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(first.Path, Entry{Text: "merge", BlockedOn: "operator", Why: "review"}); err != nil {
		t.Fatal(err)
	}
	same, err := NewBranch(dir, "a different title", "other about", "acme/widget", "feat/status", now.Add(time.Hour))
	if err != nil || same.Path != first.Path || same.Title != "branch work" || len(same.Items) != 1 {
		t.Fatalf("retry lost branch page: %+v %v", same, err)
	}
	if same.Items[0].BlockedOn != "operator" || same.Items[0].Why != "review" || same.Items[0].Updated.IsZero() {
		t.Fatalf("item metadata broke blocker: %+v", same.Items[0])
	}
	other, err := NewBranch(dir, "branch work", "", "acme/other", "feat/status", now)
	if err != nil || other.Path == first.Path {
		t.Fatalf("repository collision: %+v %v", other, err)
	}
	after, _ := os.ReadFile(legacy.Path)
	if string(before) != string(after) {
		t.Fatal("legacy page migrated")
	}
	if _, err := ForBranch(dir, "acme/widget", "missing"); !errors.Is(err, ErrBranchMissing) {
		t.Fatalf("missing branch: %v", err)
	}
}

func TestBranchToggleStampsOnlySelectedItemAndKeepsNumbers(t *testing.T) {
	l, err := NewBranch(t.TempDir(), "work", "", "acme/widget", "feat/work", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second"} {
		if _, err := Add(l.Path, Entry{Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	l, _ = Load(l.Path)
	before, _ := os.ReadFile(l.Path)
	changed, err := Toggle(l.Path, l.Items[0], l.Revision)
	if err != nil || !changed.Done {
		t.Fatalf("toggle: %+v %v", changed, err)
	}
	after, _ := Load(l.Path)
	if after.Items[1].Updated != l.Items[1].Updated || after.Items[1].Done {
		t.Fatal("toggle touched other item")
	}
	if got := after.NumberedItems()[0].Index; got != 1 {
		t.Fatalf("newest display index=%d;want original item1", got)
	}
	if after.Items[0].Text != "first" || after.Items[1].Text != "second" {
		t.Fatal("physical item order changed")
	}
	unchanged, _ := os.ReadFile(l.Path)
	if _, changed, err := Set(l.Path, "1", true); err != nil || changed {
		t.Fatalf("idempotent Set: %v %v", changed, err)
	}
	still, _ := os.ReadFile(l.Path)
	if string(still) != string(unchanged) {
		t.Fatal("no-op changed timestamp")
	}
	if _, err := Toggle(l.Path, l.Items[0], l.Revision); err == nil {
		t.Fatal("stale snapshot accepted")
	}
	// Inserting an identical row cannot pass merely because ordinal/text match.
	body := strings.Replace(string(before), "- [ ] first", "- [ ] first\n- [ ] first", 1)
	if err := os.WriteFile(l.Path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Toggle(l.Path, l.Items[0], l.Revision); err == nil {
		t.Fatal("identical insertion accepted stale ordinal")
	}
}

func TestLegacyTogglePreservesAllOtherBytes(t *testing.T) {
	path := page(t, "# legacy\n\n- [ ] first\n- [ ] second\n\nnotes\n")
	l, _ := Load(path)
	before, _ := os.ReadFile(path)
	if _, err := Toggle(path, l.Items[1], l.Revision); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	expected := append([]byte{}, before...)
	expected[l.Items[1].Offset] = 'x'
	if string(after) != string(expected) {
		t.Fatal("legacy toggle added metadata or rewrote prose")
	}
}

func TestBranchMetadataRefusesMalformedAndDuplicateRecords(t *testing.T) {
	for _, metadata := range []string{
		`<!-- bermuda-check: invalid -->`,
		`<!-- bermuda-check: {"repo":"acme/widget"} -->`,
		"<!-- bermuda-check: {\"repo\":\"acme/widget\",\"branch\":\"feat/work\"} -->\n<!-- bermuda-check: {\"repo\":\"acme/widget\",\"branch\":\"feat/other\"} -->",
	} {
		path := page(t, "# work\n"+metadata+"\n\n- [ ] item\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted bad metadata: %s", metadata)
		}
	}
}

func TestBranchSetRefusesRereadWithShortenedPage(t *testing.T) {
	l, err := NewBranch(t.TempDir(), "work", "", "acme/widget", "feat/work", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(l.Path, Entry{Text: "selected"}); err != nil {
		t.Fatal(err)
	}
	l, _ = Load(l.Path)
	if err := os.WriteFile(l.Path, []byte("# short\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := setBranchItem(l, l.Items[0], true, time.Now()); err == nil {
		t.Fatal("stale offsets were used after file shortened")
	}
	after, _ := os.ReadFile(l.Path)
	if string(after) != "# short\n" {
		t.Fatal("stale action changed shortened page")
	}
}
