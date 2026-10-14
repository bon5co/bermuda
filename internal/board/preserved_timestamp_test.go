package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/alog"
	"github.com/bon5co/bermuda/v3/internal/checklist"
	"github.com/bon5co/bermuda/v3/internal/improve"
	"github.com/bon5co/bermuda/v3/internal/memory"
)

// Timestamp-preserving sync/restore and coarse-mtime filesystems must not
// leave an open board showing a superseded file forever. Each uncached reader
// verifies that the edited file is valid before the board is refreshed.
func TestReviewBoardRefreshAfterTimestampPreservingEdit(t *testing.T) {
	for _, kind := range []string{"alog", "improve", "checklist", "memory"} {
		t.Run(kind, func(t *testing.T) {
			m := newTestModel(t)
			root := t.TempDir()
			m.deps.ALogDir = filepath.Join(root, "alog")
			m.deps.ImproveDir = filepath.Join(root, "improve")
			m.deps.CheckDir = filepath.Join(root, "checklists")
			m.deps.MemoryDir = filepath.Join(root, "memory")
			var path, before, after string
			switch kind {
			case "alog":
				e, err := alog.Write(m.deps.ALogDir, alog.Entry{Repo: "acme/widget", Branch: "feat/review", Topic: "Review", Body: "Alpha body."})
				if err != nil {
					t.Fatal(err)
				}
				path, before, after = e.Path, "Alpha body.", "Bravo body."
			case "improve":
				e, err := improve.Write(m.deps.ImproveDir, improve.Entry{Kind: improve.Discovery, Repo: "acme/widget", Branch: "feat/review", Topic: "Review", Body: "Alpha body."})
				if err != nil {
					t.Fatal(err)
				}
				path, before, after = e.Path, "Alpha body.", "Bravo body."
			case "checklist":
				if err := os.MkdirAll(m.deps.CheckDir, 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(m.deps.CheckDir, "2026-10-15T0010 review.md")
				if err := os.WriteFile(path, []byte("# Review\n\n- [ ] ship fix\n"), 0600); err != nil {
					t.Fatal(err)
				}
				before, after = "[ ]", "[x]"
			case "memory":
				if err := os.MkdirAll(m.deps.MemoryDir, 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(m.deps.MemoryDir, "fact.md")
				if err := os.WriteFile(path, []byte("[[alpha]]\n"), 0600); err != nil {
					t.Fatal(err)
				}
				before, after = "[[alpha]]", "no links!"
			}
			stamp := time.Unix(1800000000, 0)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			m.apply(t, m.load()())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(data), before, after, 1)
			if changed == string(data) || len(changed) != len(data) {
				t.Fatal("invalid equal-size edit fixture")
			}
			if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "alog":
				es, err := alog.List(m.deps.ALogDir)
				if err != nil || len(es) != 1 || es[0].Body != after {
					t.Fatalf("uncached oracle: %v, %v", es, err)
				}
			case "improve":
				es, err := improve.List(m.deps.ImproveDir)
				if err != nil || len(es) != 1 || es[0].Body != after {
					t.Fatalf("uncached oracle: %v, %v", es, err)
				}
			case "checklist":
				ls, errs := checklist.All(m.deps.CheckDir)
				if len(errs) != 0 || len(ls) != 1 || !ls[0].Items[0].Done {
					t.Fatalf("uncached oracle: %v, %v", ls, errs)
				}
			case "memory":
				if got := memory.Read(m.deps.MemoryDir).Links; got != 0 {
					t.Fatalf("uncached oracle: links=%d", got)
				}
			}
			for refresh := 0; refresh < 3; refresh++ {
				m.apply(t, m.load()())
			}
			switch kind {
			case "alog":
				if len(m.alogEntries) != 1 || m.alogEntries[0].Body != after {
					t.Fatalf("board still shows pre-edit A.LOG: %+v", m.alogEntries)
				}
			case "improve":
				if len(m.improveEntries) != 1 || m.improveEntries[0].Body != after {
					t.Fatalf("board still shows pre-edit IMPROVE: %+v", m.improveEntries)
				}
			case "checklist":
				if len(m.checklists) != 1 || !m.checklists[0].Items[0].Done {
					t.Fatalf("board still shows unchecked item after disk was ticked")
				}
			case "memory":
				if m.memory.Links != 0 {
					t.Fatalf("board links=%d, want 0 after file edit", m.memory.Links)
				}
			}
		})
	}
}
