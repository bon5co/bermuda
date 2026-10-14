package alog

import (
	"os"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/filecache"
)

// The board tells the reader to fix the file and press r. Restoring read
// permission without editing its contents must make that refresh succeed.
func TestReviewCachedFeedRecoversAfterPermissionRepair(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the read-permission fixture")
	}
	dir := t.TempDir()
	e, err := Write(dir, Entry{Repo: "acme/widget", Branch: "feat/review", Topic: "Review", Body: "Readable after repair."})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.Path, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.Path, 0600) })
	cache := filecache.New[Entry]()
	if _, err := ListCached(dir, cache); err == nil {
		t.Fatal("fixture should initially be unreadable")
	}
	if err := os.Chmod(e.Path, 0600); err != nil {
		t.Fatal(err)
	}
	if es, err := List(dir); err != nil || len(es) != 1 {
		t.Fatalf("repaired file unreadable without cache: %v, %v", es, err)
	}
	for refresh := 0; refresh < 3; refresh++ {
		es, err := ListCached(dir, cache)
		if err != nil || len(es) != 1 || es[0].Body != e.Body {
			t.Fatalf("refresh %d after permission repair: entries=%v err=%v; want repaired card", refresh+1, es, err)
		}
	}
}
