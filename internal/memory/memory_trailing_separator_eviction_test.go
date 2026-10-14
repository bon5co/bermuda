package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/filecache"
)

// Exercise the real board reader with the unnormalised path returned by Dir.
// Reflection only measures the cache's retained entry count; content assertions
// use the public Stats fields.
func TestReviewMemoryOverrideEvictsDeletedNotes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BERMUDA_MEMORY_DIR", root+string(filepath.Separator))
	dir := Dir(t.TempDir())
	cache := filecache.New[int]()
	for i := 0; i < 64; i++ {
		path := filepath.Join(root, fmt.Sprintf("note-%03d.md", i))
		if err := os.WriteFile(path, []byte("see [[related-note]]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if got := ReadCached(dir, cache); got.Notes != 1 || got.Links != 1 {
			t.Fatalf("new note missing: %+v", got)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if got := ReadCached(dir, cache); got.Notes != 0 || got.Links != 0 {
			t.Fatalf("deleted note remains visible: %+v", got)
		}
	}
	if kept := reflect.ValueOf(cache).Elem().FieldByName("files").Len(); kept != 0 {
		t.Fatalf("memory cache retains %d deleted notes after 64 cycles with BERMUDA_MEMORY_DIR ending in a separator; want 0", kept)
	}
}
