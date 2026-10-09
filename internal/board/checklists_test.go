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

func TestChecklistsTreeNewestAndSpaceOwnsItemsOnly(t *testing.T) {
	m := seedBoardChecks(t, 2, 2)
	view := ansi.Strip(m.View())
	for _, want := range []string{"CHK.L", "CHECKLISTS", "acme/widget · feat/work-1", "item-01"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "PREVIEW") || strings.Contains(view, "CHECKLISTS / BRANCHES") {
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

func TestChecklistTreeArrowsExpandOnlySelectedParent(t *testing.T) {
	m := seedBoardChecks(t, 3, 2)
	initial, _ := m.currentChecklist()
	m.pressSpecial(t, tea.KeyDown)
	if !m.checkItemsFocus || m.checkPosition(initial.Path).cursor != 0 {
		t.Fatal("down from folder did not enter its first item")
	}
	m.pressSpecial(t, tea.KeyDown)
	if m.checkPosition(initial.Path).cursor != 1 {
		t.Fatal("down skipped a child")
	}
	m.pressSpecial(t, tea.KeyDown)
	next, _ := m.currentChecklist()
	if m.checkItemsFocus || next.Path == initial.Path {
		t.Fatal("down from last child did not select next folder")
	}
	for _, row := range m.checkTreeRows() {
		if row.item >= 0 && row.folder != m.checkCursor {
			t.Fatal("previous folder did not collapse")
		}
	}
	m.pressSpecial(t, tea.KeyUp)
	current, _ := m.currentChecklist()
	if current.Path != initial.Path || m.checkItemsFocus {
		t.Fatal("up did not select and reopen previous collapsed folder")
	}
	m.pressSpecial(t, tea.KeyDown)
	if !m.checkItemsFocus {
		t.Fatal("down did not reenter reopened folder")
	}
	m.pressSpecial(t, tea.KeyUp)
	if m.checkItemsFocus {
		t.Fatal("up from first child did not select parent")
	}
}

func TestChecklistTreeNavigationSearchAndFullWidthDates(t *testing.T) {
	m := seedBoardChecks(t, 12, 30)
	m.press(t, "j")
	m.press(t, "G")
	if m.checkCursor != 11 || m.checkItemsFocus {
		t.Fatal("End did not reach last visible folder")
	}
	m.View()        // collapsed folders fit before the selected folder's children
	m.press(t, "G") // the selected last folder has now expanded
	l, _ := m.currentChecklist()
	p := m.checkPosition(l.Path)
	if !m.checkItemsFocus || p.cursor != 29 {
		t.Fatal("End did not reach last visible child")
	}
	m.View()
	if m.checkScroll == 0 {
		t.Fatal("tree did not scroll to selected item")
	}
	m.press(t, "g")
	m.View()
	if m.checkCursor != 0 || m.checkItemsFocus || m.checkScroll != 0 {
		t.Fatal("Home did not return to first folder")
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
		t.Fatal("search did not filter tree children")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if len(m.visibleCheckItems(l)) != 30 {
		t.Fatal("Esc did not clear search")
	}
	for _, width := range []int{25, 60, 80, 160, 240} {
		m.width = width
		view := ansi.Strip(m.View())
		if len(strings.Split(view, "\n")) > m.height {
			t.Fatal("tree too tall")
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("tree too wide at%d: %q", width, line)
			}
		}
		if width >= 80 {
			sawFolder := false
			for _, line := range strings.Split(view, "\n") {
				if strings.Contains(line, "📁") {
					sawFolder = true
					if lipgloss.Width(line) != width || !strings.HasSuffix(line, checkStamp(l.Updated)) {
						t.Fatalf("date not at full %d-column edge: %q", width, line)
					}
				}
			}
			if !sawFolder {
				t.Fatal("no folder rendered")
			}
		}
	}
}

func TestChecklistsMouseRowsAndOtherActionsStayInTree(t *testing.T) {
	m := seedBoardChecks(t, 3, 3)
	m.View()
	m.click(m.width-1, m.hitTop+2)
	if !m.checkItemsFocus {
		t.Fatal("full-width item click did not select item")
	}
	l, _ := m.currentChecklist()
	if m.checkPosition(l.Path).cursor != 1 {
		t.Fatal("item click wrong row")
	}
	m.View()
	m.click(1, m.hitTop+4)
	if m.checkItemsFocus || m.checkCursor != 1 {
		t.Fatal("next folder click wrong row")
	}
	m.View()
	m.click(1, m.hitTop+2)
	if !m.checkItemsFocus || m.checkPosition(m.checkPath).cursor != 0 {
		t.Fatal("mouse hit map did not follow collapsed previous folder")
	}
	for _, key := range []string{"n", "e", "D", "R", "p", "f", "P", "i"} {
		m.press(t, key)
	}
	if m.editor != nil || m.prune != nil || m.compose != nil || len(m.testRuns) > 0 {
		t.Fatal("tree keys reached job/thread actions")
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

func TestChecklistStaleIdentitySurvivesClampedMovementAndRecoversOnReselect(t *testing.T) {
	m := seedBoardChecks(t, 1, 2)
	m.press(t, "G")
	l, _ := m.currentChecklist()
	p := m.checkPosition(l.Path)
	p.invalid = true
	m.press(t, "G")
	if !p.invalid {
		t.Fatal("clamped End silently recovered invalid selection")
	}
	_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil {
		t.Fatal("invalid row allowed write")
	}
	m.press(t, "k")
	if p.invalid {
		t.Fatal("actual movement did not recover deliberate selection")
	}
}

func TestChecklistTreeScrolledMouseSelectsRenderedItem(t *testing.T) {
	m := seedBoardChecks(t, 2, 30)
	m.press(t, "G")
	m.press(t, "G")
	m.View()
	if m.checkScroll == 0 {
		t.Fatal("fixture did not scroll")
	}
	hit, ok := m.hits[3]
	if !ok || hit.kind != hitCheckItem {
		t.Fatal("fixture must show item at body row3")
	}
	m.click(m.width-1, m.hitTop+3)
	l, _ := m.currentChecklist()
	if !m.checkItemsFocus || m.checkPosition(l.Path).cursor != hit.index {
		t.Fatal("scrolled click selected unscrolled ordinal")
	}
}

func TestChecklistTreeEmptyFolderTraversalAndDeletedParent(t *testing.T) {
	m := seedBoardChecks(t, 3, 0)
	m.pressSpecial(t, tea.KeyDown)
	if m.checkCursor != 1 || m.checkItemsFocus {
		t.Fatal("empty hint became a selectable item")
	}
	m.pressSpecial(t, tea.KeyUp)
	if m.checkCursor != 0 {
		t.Fatal("up did not skip empty hint")
	}
	l, _ := m.currentChecklist()
	if _, err := checklist.Add(l.Path, checklist.Entry{Text: "child"}); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	m.pressSpecial(t, tea.KeyDown)
	if !m.checkItemsFocus {
		t.Fatal("fixture must select child")
	}
	if err := os.Remove(l.Path); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if m.checkPath == l.Path || m.checkItemsFocus {
		t.Fatal("deleted parent inherited a different checklist's item selection")
	}
	_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil {
		t.Fatal("deleted parent allowed unrelated item write")
	}
}

func TestChecklistTreeUnicodeLabelKeepsDateAtRightEdge(t *testing.T) {
	m := seedBoardChecks(t, 1, 1)
	m.width = 80
	m.checklists[0].Branch = strings.Repeat("日本語", 40)
	line := ansi.Strip(m.checklistPane(pane{}).body)
	first := strings.Split(line, "\n")[0]
	if lipgloss.Width(first) != 80 || !strings.HasSuffix(first, checkStamp(m.checklists[0].Updated)) || !strings.Contains(first, "…") {
		t.Fatalf("Unicode date alignment: %q", first)
	}
}

func TestChecklistTreeLineClipsBelowTimestampWidth(t *testing.T) {
	line := ansi.Strip(checkTreeLine("▸ 📁 acme/widget · feat/work", "2026-10-09 12:34", 10, checkFolderStyle))
	if lipgloss.Width(line) != 10 || !strings.Contains(line, "📁") {
		t.Fatalf("narrow tree line: %q", line)
	}
}
