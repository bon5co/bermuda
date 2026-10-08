package alog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func example() Entry {
	return Entry{Repo: "acme/widget", Branch: "feat/log", Topic: "Build ready", Body: "Implementation complete. Targeted checks passed; integrated review next."}
}

func TestActivityMarkdownAndJSONKeepLegacyFormat(t *testing.T) {
	dir := Dir(t.TempDir())
	e := example()
	saved, err := Write(dir, e)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nrepo: acme/widget\nbranch: feat/log\ntopic: Build ready\n---\n\n" + e.Body + "\n"
	if string(b) != want {
		t.Fatalf("legacy Markdown changed: %s", b)
	}
	encoded, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"kind"`) {
		t.Fatalf("legacy JSON gained category: %s", encoded)
	}
}

func TestMarkdownRoundTripAndEditPreservesCreationOrder(t *testing.T) {
	dir := Dir(t.TempDir())
	first, err := Write(dir, example())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "repo: acme/widget") || strings.Contains(string(text), "created:") {
		t.Fatalf("not expected Markdown: %s", text)
	}
	time.Sleep(10 * time.Millisecond)
	second, err := Write(dir, example())
	if err != nil {
		t.Fatal(err)
	}
	change := example()
	change.Topic = "Verified: review"
	change.Body = "Done."
	edited, err := Edit(dir, first.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !first.Created.Equal(edited.Created) || first.TimeSource != edited.TimeSource {
		t.Fatal("edit changed inode or creation timestamp")
	}
	if edited.Topic != change.Topic || edited.Body != "Done." {
		t.Fatalf("edit not persisted: %#v", edited)
	}
	// A changed mtime must not move an old update above a new one.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(first.Path, future, future); err != nil {
		t.Fatal(err)
	}
	entries, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != second.ID || entries[1].ID != first.ID {
		t.Fatalf("order = %#v", entries)
	}
	read, err := Read(dir, first.ID+".md")
	if err != nil || read.Body != "Done." {
		t.Fatalf("read = %#v, %v", read, err)
	}
}

func TestWordLimitAndMissingFields(t *testing.T) {
	dir := Dir(t.TempDir())
	e := example()
	e.Body = strings.TrimSpace(strings.Repeat("word\t", 50))
	if _, err := Write(dir, e); err != nil {
		t.Fatalf("50 words rejected: %v", err)
	}
	e.Body += "\u3000overflow"
	if _, err := Write(dir, e); err == nil {
		t.Fatal("51 words accepted")
	}
	for _, invalid := range []Entry{
		{Repo: "acme/widget", Branch: "main", Topic: "Ready", Body: "  "},
		{Repo: "", Branch: "main", Topic: "Ready", Body: "Done."},
		{Repo: "acme/widget", Branch: "", Topic: "Ready", Body: "Done."},
		{Repo: "acme/widget", Branch: "main", Topic: "bad\nheader", Body: "Done."},
	} {
		if _, err := Write(dir, invalid); err == nil {
			t.Fatalf("accepted invalid entry: %#v", invalid)
		}
	}
	entries, err := List(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("rejected writes left files: %#v %v", entries, err)
	}
}

func TestUnsafeIDsAndSymlinksAreRefused(t *testing.T) {
	dir := Dir(t.TempDir())
	e, err := Write(dir, example())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../outside", "/absolute", "nested/file", "..", ""} {
		if _, err := Read(dir, id); err == nil {
			t.Errorf("read accepted unsafe id %q", id)
		}
		if _, err := Edit(dir, id, example()); err == nil {
			t.Errorf("edit accepted unsafe id %q", id)
		}
	}
	if err := os.Symlink(e.Path, filepath.Join(dir, "linked.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Read(dir, "linked"); err == nil {
		t.Fatal("followed symlink")
	}
}

func TestMalformedFileDoesNotHideValidUpdates(t *testing.T) {
	dir := Dir(t.TempDir())
	e, err := Write(dir, example())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("not frontmatter"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := List(dir)
	if err == nil || !strings.Contains(err.Error(), "broken.md") {
		t.Fatalf("missing named problem: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != e.ID {
		t.Fatalf("valid entries hidden: %#v", entries)
	}
}

func TestConcurrentUpdatesPreserveDifferentFields(t *testing.T) {
	dir := Dir(t.TempDir())
	e, err := Write(dir, example())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, change := range []func(*Entry) error{
		func(e *Entry) error { e.Topic = "Reviewed"; return nil },
		func(e *Entry) error { e.Body = "Independent review passed."; return nil },
	} {
		wg.Add(1)
		go func(change func(*Entry) error) {
			defer wg.Done()
			if _, err := Update(dir, e.ID, change); err != nil {
				t.Error(err)
			}
		}(change)
	}
	wg.Wait()
	got, err := Read(dir, e.ID)
	if err != nil || got.Topic != "Reviewed" || got.Body != "Independent review passed." {
		t.Fatalf("lost update: %#v %v", got, err)
	}
}

func TestConcurrentWritesHaveUniqueIDs(t *testing.T) {
	dir := Dir(t.TempDir())
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Write(dir, example()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := List(dir)
	if err != nil || len(entries) != 8 {
		t.Fatalf("writes lost: %d %v", len(entries), err)
	}
}

func TestEmptyListDoesNotCreateDirectory(t *testing.T) {
	dir := Dir(t.TempDir())
	entries, err := List(dir)
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatalf("list=%#v,%v", entries, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("empty read created dir: %v", err)
	}
}
