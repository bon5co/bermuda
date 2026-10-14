package filecache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Listing must discard deleted values regardless of whether the caller's
// directory argument ends in a separator. The board memory override accepts
// this ordinary spelling; filepath.Join cleans the stored file keys.
func TestReviewListingEvictsDeletedFilesWithTrailingSeparator(t *testing.T) {
	for _, trailing := range []bool{false, true} {
		t.Run(fmt.Sprintf("trailing=%v", trailing), func(t *testing.T) {
			dir := t.TempDir()
			if trailing {
				dir += string(filepath.Separator)
			}
			cache := New[string]()
			list := func() {
				t.Helper()
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				cache.Listing(dir, entries)
			}
			for i := 0; i < 64; i++ {
				path := filepath.Join(dir, fmt.Sprintf("note-%03d.md", i))
				body := strings.Repeat("deleted note contents ", 2048)
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				list()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				got, err := cache.Get(path, info, func() (string, error) {
					data, err := os.ReadFile(path)
					return string(data), err
				})
				if err != nil || got != body {
					t.Fatalf("initial read: len=%d err=%v", len(got), err)
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
				t.Fatalf("empty directory retains %d deleted parsed values after 64 create/read/delete cycles; want 0", kept)
			}
		})
	}
}
