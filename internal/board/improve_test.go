package board

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bon5co/bermuda/v3/internal/alog"
	"github.com/bon5co/bermuda/v3/internal/improve"
)

func TestImproveIsSecondAndEmptyStateIncludesKind(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "1")
	m.pressSpecial(t, tea.KeyTab)
	if m.focus != focusImprove {
		t.Fatal("IMPROVE is not immediately after A.LOG")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"IMPROVE", "No lessons yet", "unlimited body", "--kind discovery", "--repo", "--branch", "--topic", "--body"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty IMPROVE misses %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "IMPROVE.") || strings.Contains(view, "50 words maximum") {
		t.Fatal("IMPROVE name or word policy is wrong")
	}
}

func TestImproveRefreshPreservesKindOrderAndCreationWhileALogStaysSeparate(t *testing.T) {
	m := newTestModel(t)
	state := t.TempDir()
	m.deps.ALogDir, m.deps.ImproveDir = alog.Dir(state), improve.Dir(state)
	a, err := alog.Write(m.deps.ALogDir, alog.Entry{Repo: "acme/widget", Branch: "feat/cards", Topic: "Progress only", Body: "Working on cards."})
	if err != nil {
		t.Fatal(err)
	}
	first, err := improve.Write(m.deps.ImproveDir, improve.Entry{Repo: "acme/widget", Branch: "feat/lessons", Kind: "mistake", Topic: "First lesson", Body: strings.Repeat("evidence ", 100) + "firsttail"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := improve.Write(m.deps.ImproveDir, improve.Entry{Repo: "acme/widget", Branch: "feat/lessons", Kind: "recovery", Topic: "Second lesson", Body: "Verified the correction."})
	if err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if len(m.alogEntries) != 1 || m.alogEntries[0].ID != a.ID || len(m.improveEntries) != 2 || m.improveEntries[0].ID != second.ID || m.improveEntries[1].ID != first.ID {
		t.Fatal("feed ordering or isolation failed")
	}
	first.Topic, first.Kind, first.Body = "Corrected lesson", "discovery", strings.Repeat("verified ", 100)+"editedtail"
	if _, err := improve.Edit(m.deps.ImproveDir, first.ID, first); err != nil {
		t.Fatal(err)
	}
	m.selectTab(focusImprove)
	m.scroll = 3
	m.apply(t, m.load()())
	if m.scroll != 3 || m.improveEntries[1].Kind != "discovery" || !m.improveEntries[1].Created.Equal(first.Created) || m.improveEntries[1].Topic != "Corrected lesson" {
		t.Fatal("refresh lost edit, kind, original order/time or scroll")
	}
	file, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second.Path, []byte(strings.Replace(string(file), "Verified the correction.", "Filesystem correction.", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.deps.ImproveDir, "broken.md"), []byte("bad file"), 0600); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	body := ansi.Strip(m.improvePane(pane{}).body)
	for _, want := range []string{"broken.md", "Filesystem correction.", "DISCOVERY", "RECOVERY", "editedtail"} {
		if !strings.Contains(body, want) {
			t.Fatalf("refresh misses %q", want)
		}
	}
	m.selectTab(focusALog)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Progress only") || strings.Contains(view, "broken.md") || strings.Contains(view, "Corrected lesson") {
		t.Fatal("IMPROVE error or entries leaked into A.LOG")
	}
}

func TestImproveLongBodyKindSearchAndScrollingReachTheEnd(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusImprove)
	m.width, m.height = 25, 24
	m.improveEntries = []alog.Entry{{ID: "lesson-1", Repo: "acme/widget", Branch: "feat/lessons", Kind: "discovery", Topic: "A measured result", Body: strings.Repeat("word ", 5000) + "tailproof", Created: time.Now(), TimeSource: "birthtime"}}
	card := ansi.Strip(m.alogCard(m.improveEntries[0]))
	if !strings.Contains(card, "tailproof") || !strings.Contains(card, "DISCOVERY") {
		t.Fatal("long lesson was truncated or kind hidden")
	}
	for _, line := range strings.Split(card, "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("long card overflows: %q", line)
		}
	}
	m.View()
	m.press(t, "j")
	m.View()
	if m.scroll != 1 || m.cursor != 0 {
		t.Fatal("lesson down key did not scroll")
	}
	m.wheelBy(t, true)
	m.View()
	if m.scroll != 4 {
		t.Fatal("lesson wheel did not scroll")
	}
	m.press(t, "G")
	if view := ansi.Strip(m.View()); !strings.Contains(view, "tailproof") || !strings.Contains(view, "IMPROVE") || !strings.Contains(strings.Join(strings.Fields(view), " "), "2 latest") {
		t.Fatalf("end of long lesson or pinned chrome missing:\n%s", view)
	}
	m.press(t, "2")
	if m.scroll != 0 {
		t.Fatal("2 does not jump back to latest lesson")
	}
	for _, q := range []string{"DISCOVERY", "ACME", "LESSONS", "MEASURED", "TAILPROOF"} {
		m.query = q
		if len(m.visibleImprove()) != 1 {
			t.Fatalf("IMPROVE search misses %q", q)
		}
	}
	m.query = "not found"
	if !strings.Contains(m.improvePane(pane{}).body, "No entries match") {
		t.Fatal("no-match state is missing")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if len(m.visibleImprove()) != 1 {
		t.Fatal("escape did not clear lesson search")
	}
}

func TestImproveDoesNotTriggerJobActionsOrLeakALogErrors(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusImprove)
	m.alogErr = errors.New("A.LOG-only error")
	for _, key := range []string{"n", "e", "D", "R", "p", "f", "P", "i", "enter", "l"} {
		m.press(t, key)
	}
	if m.editor != nil || m.detail != nil || m.runDetail != nil || m.compose != nil || m.prune != nil || len(m.testRuns) != 0 {
		t.Fatal("IMPROVE keys reached job or thread actions")
	}
	if strings.Contains(m.View(), "A.LOG-only error") {
		t.Fatal("A.LOG error leaked into IMPROVE")
	}
}

func TestEveryTabKeyWorksFromImproveAndThreads(t *testing.T) {
	m := newTestModel(t)
	for _, from := range []focus{focusImprove, focusThread} {
		for i, want := range tabOrder {
			m.selectTab(from)
			m.press(t, itoa(i+1))
			if m.focus != want {
				t.Fatalf("key %d from focus %d opened %d, want %d", i+1, from, m.focus, want)
			}
		}
	}
}
