package board

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/checklist"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func seedBoardChecks(t *testing.T, folders, items int) *Model {
	t.Helper()
	m := newTestModel(t)
	m.width, m.height = 110, 24
	m.deps.CheckDir = t.TempDir()
	for folder := 0; folder < folders; folder++ {
		l, err := checklist.NewBranch(m.deps.CheckDir, fmt.Sprintf("Work %d", folder), "", "acme/widget", fmt.Sprintf("feat/work-%d", folder), time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for item := 0; item < items; item++ {
			if _, err := checklist.Add(l.Path, checklist.Entry{Text: fmt.Sprintf("item-%02d", item)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	m.press(t, "2")
	m.apply(t, m.load()())
	m.syncChecks()
	return m
}

func TestChecklistsTwoPanesNewestAndSpaceOwnsItemsOnly(t *testing.T) {
	m := seedBoardChecks(t, 2, 2)
	view := ansi.Strip(m.View())
	for _, want := range []string{"CHK.L", "CHECKLISTS / BRANCHES", "ITEMS", "feat/work-1", "item-01"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "PREVIEW") {
		t.Fatal("unexpected preview pane")
	}
	l, _ := m.currentChecklist()
	items := m.visibleCheckItems(l)
	if items[0].Index != 2 {
		t.Fatalf("display not newest first: %+v", items)
	}
	_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil {
		t.Fatal("folder space toggled")
	}
	m.press(t, "l")
	m.pressSpecial(t, tea.KeySpace)
	m.apply(t, m.load()())
	after, _ := checklist.Load(l.Path)
	if !after.Items[1].Done || after.Items[0].Done {
		t.Fatal("toggle used display ordinal instead of original number")
	}
	if m.checkPath != l.Path || !m.checkItemsFocus || m.checkPosition(l.Path).index != 2 {
		t.Fatal("toggle refresh lost selection/focus")
	}
}

func TestChecklistSelectionSurvivesRefreshAndManualInsertion(t *testing.T) {
	m := seedBoardChecks(t, 2, 3)
	m.press(t, "l")
	m.press(t, "j")
	l, _ := m.currentChecklist()
	p := m.checkPosition(l.Path)
	selected := p.identity
	data, _ := os.ReadFile(l.Path)
	at := strings.Index(string(data), "- [ ]")
	body := string(data[:at]) + "- [ ] inserted above\n" + string(data[at:])
	if err := os.WriteFile(l.Path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	l, _ = m.currentChecklist()
	p = m.checkPosition(l.Path)
	if p.invalid || p.identity != selected || m.visibleCheckItems(l)[p.cursor].String() != selected {
		t.Fatalf("refresh rebound to another item: %+v", p)
	}
	m.pressSpecial(t, tea.KeySpace)
	m.apply(t, m.load()())
	after, _ := checklist.Load(l.Path)
	for _, it := range after.Items {
		if it.Done && it.String() != selected {
			t.Fatalf("toggled wrong work: %+v", it)
		}
	}
	// Removing the selected item is a visible stale state, never another row.
	var lines []string
	data, _ = os.ReadFile(l.Path)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, selected) {
			lines = append(lines, line)
		}
	}
	if err := os.WriteFile(l.Path, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if !m.checkPosition(l.Path).invalid {
		t.Fatal("deleted item silently rebound")
	}
	_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil || m.err == nil {
		t.Fatal("deleted selection mutated another item")
	}
}

func TestChecklistNavigationSearchAndIndependentScrolling(t *testing.T) {
	m := seedBoardChecks(t, 12, 30)
	m.press(t, "j")
	m.press(t, "G")
	if m.checkCursor != 11 {
		t.Fatal("End overflowed from nonzero folder cursor")
	}
	m.View()
	if m.checkScroll == 0 {
		t.Fatal("folder list did not scroll")
	}
	m.press(t, "l")
	m.press(t, "j")
	m.press(t, "G")
	l, _ := m.currentChecklist()
	p := m.checkPosition(l.Path)
	if p.cursor != 29 {
		t.Fatal("End overflowed from nonzero item cursor")
	}
	m.View()
	if p.scroll == 0 {
		t.Fatal("item list did not scroll")
	}
	folderScroll := m.checkScroll
	m.press(t, "g")
	m.View()
	if p.scroll != 0 || m.checkScroll != folderScroll {
		t.Fatal("item scroll moved folder window")
	}
	m.press(t, "/")
	for _, r := range "item-17" {
		m.press(t, string(r))
	}
	m.pressSpecial(t, tea.KeyEnter)
	if len(m.visibleChecklists()) != 12 {
		t.Fatal("item search hid matching folder")
	}
	l, _ = m.currentChecklist()
	if len(m.visibleCheckItems(l)) != 1 || m.visibleCheckItems(l)[0].Text != "item-17" {
		t.Fatal("search did not filter items")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if len(m.visibleCheckItems(l)) != 30 {
		t.Fatal("Esc did not clear search")
	}
	for _, width := range []int{25, 60, 110} {
		m.width = width
		view := ansi.Strip(m.View())
		if len(strings.Split(view, "\n")) > m.height {
			t.Fatal("pane too tall")
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("pane too wide at%d: %q", width, line)
			}
		}
	}
}

func TestChecklistsMouseAndOtherActionsStayInTheirPane(t *testing.T) {
	m := seedBoardChecks(t, 3, 3)
	m.View()
	left, _ := m.checkWidths()
	m.click(left+4, m.hitTop+1)
	if !m.checkItemsFocus {
		t.Fatal("item click did not focus items")
	}
	l, _ := m.currentChecklist()
	if m.checkPosition(l.Path).cursor != 1 {
		t.Fatal("item click wrong row")
	}
	m.click(1, m.hitTop+2)
	if m.checkItemsFocus || m.checkCursor != 1 {
		t.Fatal("folder click wrong row")
	}
	for _, key := range []string{"n", "e", "D", "R", "p", "f", "P", "i"} {
		m.press(t, key)
	}
	if m.editor != nil || m.prune != nil || m.compose != nil || len(m.testRuns) > 0 {
		t.Fatal("checklist keys reached job/thread actions")
	}
}

func TestChecklistDuplicateDeletionInvalidatesSelectedIdentity(t *testing.T) {
	m := seedBoardChecks(t, 1, 0)
	l, _ := m.currentChecklist()
	for range 2 {
		if _, err := checklist.Add(l.Path, checklist.Entry{Text: "duplicate"}); err != nil {
			t.Fatal(err)
		}
	}
	m.apply(t, m.load()())
	m.press(t, "l") // newest is the second duplicate
	l, _ = m.currentChecklist()
	if m.checkPosition(l.Path).index != 2 {
		t.Fatal("fixture must select second duplicate")
	}
	data, _ := os.ReadFile(l.Path)
	lines := strings.Split(string(data), "\n")
	found := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "- [ ] duplicate") {
			found++
			if found == 2 {
				lines = append(lines[:i], lines[i+1:]...)
				break
			}
		}
	}
	if err := os.WriteFile(l.Path, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if !m.checkPosition(l.Path).invalid {
		t.Fatal("deleting selected duplicate substituted its survivor")
	}
	_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil {
		t.Fatal("stale duplicate allowed toggle")
	}
}
