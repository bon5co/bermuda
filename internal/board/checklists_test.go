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

func TestChecklistsTreeNumberedAndSpaceOwnsItemsOnly(t *testing.T) {
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
	if items[0].Index != 1 || items[1].Index != 2 {
		t.Fatalf("display not in item-number order: %+v", items)
	}
	_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil || !m.checkItemsFocus {
		t.Fatal("folder space must enter items without a write")
	}
	if unchanged, err := checklist.Load(l.Path); err != nil || unchanged.Counts().Done != 0 {
		t.Fatal("folder space changed persistence")
	}
	m.press(t, "l")
	m.pressSpecial(t, tea.KeySpace)
	m.apply(t, m.load()())
	after, _ := checklist.Load(l.Path)
	if !after.Items[0].Done || after.Items[1].Done {
		t.Fatal("toggle hit wrong item")
	}
	if m.checkPath != l.Path || !m.checkItemsFocus || m.checkPosition(l.Path).index != 1 {
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

func TestChecklistTreeFolderAndItemNavigationModes(t *testing.T) {
	m := seedBoardChecks(t, 3, 2)
	initial, _ := m.currentChecklist()
	m.pressSpecial(t, tea.KeyDown)
	next, _ := m.currentChecklist()
	if m.checkItemsFocus || next.Path == initial.Path {
		t.Fatal("folder down did not skip expanded children")
	}
	for _, row := range m.checkTreeRows() {
		if row.item >= 0 && row.folder != m.checkCursor {
			t.Fatal("previous folder did not collapse")
		}
	}
	m.pressSpecial(t, tea.KeyUp)
	current, _ := m.currentChecklist()
	if current.Path != initial.Path || m.checkItemsFocus {
		t.Fatal("folder up did not select and reopen previous folder")
	}
	m.pressSpecial(t, tea.KeyEnter)
	if !m.checkItemsFocus || m.checkPosition(initial.Path).cursor != 0 {
		t.Fatal("folder Enter did not enter first item")
	}
	m.pressSpecial(t, tea.KeyUp)
	if !m.checkItemsFocus || m.checkPosition(initial.Path).cursor != 0 {
		t.Fatal("item Up left its parent or failed to clamp")
	}
	m.pressSpecial(t, tea.KeyDown)
	if m.checkPosition(initial.Path).cursor != 1 {
		t.Fatal("item Down skipped child")
	}
	m.pressSpecial(t, tea.KeyDown)
	if !m.checkItemsFocus || m.checkPath != initial.Path || m.checkPosition(initial.Path).cursor != 1 {
		t.Fatal("item Down left its checklist")
	}
	m.pressSpecial(t, tea.KeyEnter)
	if !m.checkItemsFocus || m.checkPosition(initial.Path).cursor != 1 {
		t.Fatal("Enter on item navigated or wrote")
	}
	m.press(t, "h")
	if m.checkItemsFocus || m.checkPath != initial.Path {
		t.Fatal("left did not return to parent")
	}
}

func TestChecklistTreeNavigationSearchAndFullWidthDates(t *testing.T) {
	m := seedBoardChecks(t, 12, 30)
	m.press(t, "j")
	m.press(t, "G")
	if m.checkCursor != 11 || m.checkItemsFocus {
		t.Fatal("End did not reach last visible folder")
	}
	m.View() // collapsed folders fit before the selected folder's children
	m.pressSpecial(t, tea.KeyEnter)
	m.press(t, "G") // item End selects the last child of this checklist
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
	if !m.checkItemsFocus || m.checkCursor != 11 || p.cursor != 0 {
		t.Fatal("item Home did not stay in current checklist")
	}
	m.press(t, "h")
	m.press(t, "g")
	m.View()
	if m.checkCursor != 0 || m.checkItemsFocus || m.checkScroll != 0 {
		t.Fatal("folder Home did not return to first folder")
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
	m.press(t, "l")
	m.press(t, "j") // second duplicate
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
	m.pressSpecial(t, tea.KeyEnter)
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
	m.pressSpecial(t, tea.KeyEnter)
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
	m.pressSpecial(t, tea.KeyEnter)
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

func TestChecklistModePagingWheelAndBoundaries(t *testing.T) {
	m := seedBoardChecks(t, 4, 3)
	initial, _ := m.currentChecklist()
	m.wheel(1)
	if m.checkCursor != 1 || m.checkItemsFocus {
		t.Fatal("folder wheel did not skip children")
	}
	m.press(t, "]")
	if m.checkCursor != 3 || m.checkItemsFocus {
		t.Fatal("folder page did not reach last checklist")
	}
	m.press(t, "g")
	if m.checkPath != initial.Path || m.checkItemsFocus {
		t.Fatal("folder Home did not select first checklist")
	}
	m.pressSpecial(t, tea.KeyEnter)
	p := m.checkPosition(initial.Path)
	m.wheel(30)
	if p.cursor != 2 || m.checkPath != initial.Path || !m.checkItemsFocus {
		t.Fatal("item wheel escaped its parent")
	}
	for _, key := range []string{"j", "]", "G"} {
		m.press(t, key)
		if p.cursor != 2 || m.checkPath != initial.Path || !m.checkItemsFocus {
			t.Fatalf("item boundary escaped on %q", key)
		}
	}
	m.press(t, "[")
	if p.cursor != 0 || m.checkPath != initial.Path || !m.checkItemsFocus {
		t.Fatal("item page-up escaped its parent")
	}
	for _, key := range []string{"k", "[", "g"} {
		m.press(t, key)
		if p.cursor != 0 || m.checkPath != initial.Path || !m.checkItemsFocus {
			t.Fatalf("item top boundary escaped on %q", key)
		}
	}
	m.wheel(-30)
	if p.cursor != 0 || !m.checkItemsFocus {
		t.Fatal("item wheel top boundary escaped")
	}
}

func TestChecklistEntryAndEnterItemDoNotWrite(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyEnter, tea.KeySpace} {
		m := seedBoardChecks(t, 1, 2)
		l, _ := m.currentChecklist()
		before, err := os.ReadFile(l.Path)
		if err != nil {
			t.Fatal(err)
		}
		_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: key})
		if cmd != nil || !m.checkItemsFocus {
			t.Fatal("folder entry did not enter safely")
		}
		_, cmd = m.handleChecklistKey(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil {
			t.Fatal("item Enter returned a write command")
		}
		after, err := os.ReadFile(l.Path)
		if err != nil || string(after) != string(before) {
			t.Fatal("folder entry or item Enter changed Markdown bytes")
		}
	}
}

func TestChecklistSearchStartsInFolderModeAndEscReturnsParentFirst(t *testing.T) {
	m := seedBoardChecks(t, 2, 3)
	m.pressSpecial(t, tea.KeyEnter)
	m.press(t, "/")
	for _, r := range "item-01" {
		m.press(t, string(r))
	}
	m.pressSpecial(t, tea.KeyEnter)
	if m.checkItemsFocus {
		t.Fatal("search left an arbitrary matched item armed for toggle")
	}
	l, _ := m.currentChecklist()
	before, _ := os.ReadFile(l.Path)
	_, cmd := m.handleChecklistKey(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil || !m.checkItemsFocus {
		t.Fatal("Space after search did not enter items safely")
	}
	after, _ := os.ReadFile(l.Path)
	if string(before) != string(after) {
		t.Fatal("Space after search toggled arbitrary match")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if m.checkItemsFocus || m.query != "item-01" {
		t.Fatal("first Esc did not return parent preserving filter")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if m.query != "" || m.checkItemsFocus {
		t.Fatal("second Esc did not clear filter in folder mode")
	}
	for _, end := range []tea.KeyType{tea.KeyEnter, tea.KeyEsc} {
		m.pressSpecial(t, tea.KeyEnter)
		m.press(t, "/")
		m.pressSpecial(t, end)
		if m.checkItemsFocus {
			t.Fatal("empty search dismissal left item mode active")
		}
	}
	m.pressSpecial(t, tea.KeyEnter)
	m.press(t, "/")
	m.pressSpecial(t, tea.KeyBackspace)
	if m.checkItemsFocus || m.searching {
		t.Fatal("erase-past-empty search did not safely return folder mode")
	}
}
