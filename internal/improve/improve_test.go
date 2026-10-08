package improve

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/alog"
)

func lesson(kind string) Entry {
	return Entry{Repo: "acme/widget", Branch: "feat/learning", Topic: "Verified recovery", Kind: kind, Body: "Trigger: the cache was stale. Recovery: clear the derived cache and verify the source. Limit: source data remains authoritative."}
}

func TestUnlimitedBodiesAndActivityFeedIsolation(t *testing.T) {
	state := t.TempDir()
	dir := Dir(state)
	for _, words := range []int{51, 5000} {
		e := lesson(Discovery)
		e.Body = strings.TrimSpace(strings.Repeat("evidence ", words))
		saved, err := Write(dir, e)
		if err != nil {
			t.Fatalf("%d words rejected: %v", words, err)
		}
		read, err := Read(dir, saved.ID)
		if err != nil || read.Body != e.Body {
			t.Fatalf("%d word body changed: %v", words, err)
		}
		if _, err := alog.Write(alog.Dir(state), e); err == nil {
			t.Fatalf("A.LOG accepted %d words", words)
		}
	}
	// There is no separate byte limit for a single very long word either.
	e := lesson(Discovery)
	e.Body = strings.Repeat("x", 2*1024*1024)
	saved, err := Write(dir, e)
	if err != nil {
		t.Fatalf("large single-word body rejected: %v", err)
	}
	read, err := Read(dir, saved.ID)
	if err != nil || read.Body != e.Body {
		t.Fatalf("large body truncated: %v", err)
	}
	activity := lesson("")
	activity.Body = "Implementation ready. Independent review next."
	if _, err := alog.Write(alog.Dir(state), activity); err != nil {
		t.Fatal(err)
	}
	activityEntries, err := alog.List(alog.Dir(state))
	if err != nil || len(activityEntries) != 1 {
		t.Fatalf("activity mixed feeds: %#v %v", activityEntries, err)
	}
	entries, err := List(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("learning mixed feeds: %d %v", len(entries), err)
	}
	if _, err := alog.Read(alog.Dir(state), saved.ID); !os.IsNotExist(err) {
		t.Fatalf("learning id visible in activity feed: %v", err)
	}
}

func TestKindsPersistAndEditsPreserveCreationOrder(t *testing.T) {
	dir := Dir(t.TempDir())
	first, err := Write(dir, lesson(Discovery))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{Mistake, Recovery} {
		time.Sleep(10 * time.Millisecond)
		saved, err := Write(dir, lesson(kind))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(saved.Path)
		if err != nil || !strings.Contains(string(b), "kind: "+kind) {
			t.Fatalf("category not stored: %s %v", b, err)
		}
		read, err := Read(dir, saved.ID)
		if err != nil || read.Kind != kind {
			t.Fatalf("kind changed: %#v %v", read, err)
		}
	}
	changed, err := Update(dir, first.ID, func(e *Entry) error {
		e.Kind = Recovery
		e.Body = "Root cause confirmed. Recovery independently verified."
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !changed.Created.Equal(first.Created) || changed.Kind != Recovery {
		t.Fatal("edit changed creation identity or lost category")
	}
	if changed.Topic != first.Topic || changed.Repo != first.Repo || changed.Branch != first.Branch {
		t.Fatal("partial edit replaced metadata")
	}
	entries, err := List(dir)
	if err != nil || len(entries) != 3 || entries[2].ID != first.ID {
		t.Fatalf("edit changed newest-first order: %#v %v", entries, err)
	}
	replacement := lesson(Mistake)
	replacement.Body = "Original interpretation was wrong. Evidence disproved it."
	if _, err := Edit(dir, first.ID, replacement); err != nil {
		t.Fatal(err)
	}
}

func TestKindValidationIncludesHandEditedMarkdown(t *testing.T) {
	dir := Dir(t.TempDir())
	for _, kind := range []string{"", "unknown", "Recovery", "discovery\nmistake"} {
		if _, err := Write(dir, lesson(kind)); err == nil {
			t.Errorf("accepted kind %q", kind)
		}
	}
	good, err := Write(dir, lesson(Discovery))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update(dir, good.ID, func(e *Entry) error { e.Kind = "invalid"; return nil }); err == nil {
		t.Fatal("invalid partial edit accepted")
	}
	kept, err := Read(dir, good.ID)
	if err != nil || kept.Kind != Discovery {
		t.Fatalf("invalid edit changed file: %#v %v", kept, err)
	}
	for _, name := range []string{"missing", "invalid"} {
		meta := ""
		if name == "invalid" {
			meta = "kind: unknown\n"
		}
		text := "---\nrepo: acme/widget\nbranch: main\ntopic: Hand edited\n" + meta + "---\n\nSome learning.\n"
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir, name); err == nil || !strings.Contains(err.Error(), "kind must be one of") {
			t.Fatalf("hand-edited kind accepted: %s %v", name, err)
		}
	}
	entries, err := List(dir)
	if err == nil || !strings.Contains(err.Error(), "missing.md") || !strings.Contains(err.Error(), "invalid.md") {
		t.Fatalf("missing named errors: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != good.ID {
		t.Fatalf("bad file hid good entry: %#v", entries)
	}
}

func TestConcurrentLearningUpdatesAndWrites(t *testing.T) {
	dir := Dir(t.TempDir())
	e, err := Write(dir, lesson(Mistake))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, change := range []func(*Entry) error{
		func(e *Entry) error { e.Kind = Recovery; return nil },
		func(e *Entry) error { e.Body = strings.TrimSpace(strings.Repeat("verified ", 100)); return nil },
	} {
		wg.Add(1)
		go func(change func(*Entry) error) {
			defer wg.Done()
			if _, err := Update(dir, e.ID, change); err != nil {
				t.Error(err)
			}
		}(change)
	}
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Write(dir, lesson(Discovery)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	read, err := Read(dir, e.ID)
	if err != nil || read.Kind != Recovery || len(strings.Fields(read.Body)) != 100 {
		t.Fatalf("concurrent update lost: %#v %v", read, err)
	}
	entries, err := List(dir)
	if err != nil || len(entries) != 6 {
		t.Fatalf("concurrent writes lost: %d %v", len(entries), err)
	}
}

func TestUnsafeLearningIDsAndEmptyBodyAreRefused(t *testing.T) {
	dir := Dir(t.TempDir())
	e := lesson(Discovery)
	e.Body = " \n\t"
	if _, err := Write(dir, e); err == nil {
		t.Fatal("empty body accepted")
	}
	for _, id := range []string{"../outside", "/absolute", "nested/file", "..", ""} {
		if _, err := Read(dir, id); err == nil {
			t.Errorf("unsafe read id accepted: %q", id)
		}
		if _, err := Edit(dir, id, lesson(Recovery)); err == nil {
			t.Errorf("unsafe edit id accepted: %q", id)
		}
	}
}
