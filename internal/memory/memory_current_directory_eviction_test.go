package memory

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/filecache"
)

// A relative current-directory override is accepted by Dir and ReadCached.
// Visible stats alone cannot reveal retained deleted cache entries.
func TestReviewMemoryCurrentDirectoryEvictsDeletedNotes(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BERMUDA_MEMORY_DIR", ".")
	dir := Dir(t.TempDir())
	if dir != "." {
		t.Fatalf("override resolved to %q; want .", dir)
	}
	cache := filecache.New[int]()
	for i := 0; i < 16; i++ {
		path := fmt.Sprintf("note-%02d.md", i)
		if err := os.WriteFile(path, []byte("see [[related-note]]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if got := ReadCached(dir, cache); !got.Present || got.Notes != 1 || got.Links != 1 {
			t.Fatalf("new note missing: %+v", got)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if got := ReadCached(dir, cache); !got.Present || got.Notes != 0 || got.Links != 0 {
			t.Fatalf("deleted note visible: %+v", got)
		}
	}
	if kept := reflect.ValueOf(cache).Elem().FieldByName("files").Len(); kept != 0 {
		t.Fatalf("BERMUDA_MEMORY_DIR=. retains %d deleted notes after 16 cycles; want 0", kept)
	}
}
