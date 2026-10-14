package filecache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Join removes the current-directory prefix; Listing must still evict the
// resulting bare relative keys, including keys in removed subdirectories.
func TestReviewCurrentDirectoryEvictsDeletedFiles(t *testing.T) {
	for _, dir := range []string{".", "./", "", "child/.."} {
		t.Run(fmt.Sprintf("dir=%q", dir), func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.Mkdir("child", 0700); err != nil {
				t.Fatal(err)
			}
			cache := New[string]()
			list := func() {
				t.Helper()
				entries, err := os.ReadDir(".")
				if err != nil {
					t.Fatal(err)
				}
				cache.Listing(dir, entries)
			}
			for i := 0; i < 16; i++ {
				path := filepath.Join(dir, fmt.Sprintf("note-%02d.md", i))
				if err := os.WriteFile(path, []byte("parsed value"), 0600); err != nil {
					t.Fatal(err)
				}
				list()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := cache.Get(path, info, func() (string, error) {
					b, err := os.ReadFile(path)
					return string(b), err
				}); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				list()
			}
			cache.mu.Lock()
			kept := len(cache.files)
			cache.mu.Unlock()
			if kept != 0 {
				t.Fatalf("Listing(%q) retains %d deleted parsed values after 16 cycles; want 0", dir, kept)
			}
		})
	}
}
