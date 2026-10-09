package board

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bon5co/bermuda/v3/internal/checklist"
)

// Each folder remembers its item selection and scroll. CLI numbers refer to
// page order; display order is independent and can change after a toggle.
type checkPosition struct {
	index, cursor, scroll int
	identity, snapshot    string
	identityCount         int
	invalid               bool
}

func (m *Model) visibleChecklists() []checklist.List {
	var lists []checklist.List
	for _, l := range m.checklists {
		if m.query == "" || checkMatches(l.Repo+"\n"+l.Branch+"\n"+l.Title+"\n"+l.Name, m.query) {
			lists = append(lists, l)
			continue
		}
		for _, it := range l.Items {
			if checkMatches(it.String(), m.query) {
				lists = append(lists, l)
				break
			}
		}
	}
	return lists
}
func checkMatches(text, query string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(query))
}
func (m *Model) currentChecklist() (checklist.List, bool) {
	lists := m.visibleChecklists()
	if m.checkCursor < 0 || m.checkCursor >= len(lists) {
		return checklist.List{}, false
	}
	return lists[m.checkCursor], true
}
func (m *Model) visibleCheckItems(l checklist.List) []checklist.Item {
	items := l.NewestItems()
	if m.query == "" || checkMatches(l.Repo+"\n"+l.Branch+"\n"+l.Title+"\n"+l.Name, m.query) {
		return items
	}
	var out []checklist.Item
	for _, it := range items {
		if checkMatches(it.String(), m.query) {
			out = append(out, it)
		}
	}
	return out
}
func (m *Model) checkPosition(path string) *checkPosition {
	if m.checkPositions == nil {
		m.checkPositions = map[string]*checkPosition{}
	}
	if m.checkPositions[path] == nil {
		m.checkPositions[path] = &checkPosition{}
	}
	return m.checkPositions[path]
}
func clampCheck(n, size int) int {
	if size == 0 {
		return 0
	}
	return min(max(n, 0), size-1)
}

// A refresh may reorder both columns; keep the selected folder's path and the
// selected item's original page number rather than whichever row moved there.
func (m *Model) syncChecks() {
	lists := m.visibleChecklists()
	for i, l := range lists {
		if l.Path == m.checkPath {
			m.checkCursor = i
			break
		}
	}
	m.checkCursor = clampCheck(m.checkCursor, len(lists))
	if len(lists) == 0 {
		m.checkPath = ""
		return
	}
	l := lists[m.checkCursor]
	m.checkPath = l.Path
	p := m.checkPosition(l.Path)
	items := m.visibleCheckItems(l)
	var parts []string
	for _, it := range l.Items {
		parts = append(parts, fmt.Sprintf("%d:%s", it.Index, it.String()))
	}
	snapshot := strings.Join(parts, "\n")
	if p.identity != "" && snapshot != p.snapshot && p.identityCount > 1 {
		p.invalid = true
	}
	if p.identity != "" && !p.invalid {
		matches, found := 0, -1
		for i, it := range items {
			if snapshot == p.snapshot {
				if it.Index == p.index {
					matches, found = 1, i
					break
				}
			} else if it.String() == p.identity {
				matches++
				found = i
			}
		}
		if matches == 1 {
			p.cursor = found
		} else {
			p.invalid = true
		}
	}
	p.cursor = clampCheck(p.cursor, len(items))
	if len(items) > 0 && !p.invalid {
		p.index, p.identity = items[p.cursor].Index, items[p.cursor].String()
		p.identityCount = checkIdentityCount(l, p.identity)
	}
	p.snapshot = snapshot
}
func (m *Model) resetCheckSearch() {
	m.checkCursor, m.checkScroll, m.checkPath = 0, 0, ""
	for _, p := range m.checkPositions {
		*p = checkPosition{}
	}
	m.syncChecks()
}
func (m *Model) moveCheck(delta int) {
	if m.checkItemsFocus {
		if l, ok := m.currentChecklist(); ok {
			items := m.visibleCheckItems(l)
			p := m.checkPosition(l.Path)
			p.cursor = checkMove(p.cursor, delta, len(items))
			if len(items) > 0 {
				p.index, p.identity, p.invalid = items[p.cursor].Index, items[p.cursor].String(), false
				p.identityCount = checkIdentityCount(l, p.identity)
			}
		}
	} else {
		lists := m.visibleChecklists()
		m.checkCursor = checkMove(m.checkCursor, delta, len(lists))
		if len(lists) > 0 {
			m.checkPath = lists[m.checkCursor].Path
		}
		m.syncChecks()
	}
}

func checkIdentityCount(l checklist.List, identity string) int {
	n := 0
	for _, it := range l.Items {
		if it.String() == identity {
			n++
		}
	}
	return n
}

func checkMove(cursor, delta, size int) int {
	if delta >= size {
		return max(0, size-1)
	}
	if delta <= -size {
		return 0
	}
	return clampCheck(cursor+delta, size)
}
func (m *Model) handleChecklistKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.stepTab(1)
	case "shift+tab":
		m.stepTab(-1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.selectTab(tabOrder[int(msg.String()[0]-'1')])
	case "l", "right", "enter":
		m.checkItemsFocus = true
	case "h", "left":
		m.checkItemsFocus = false
	case "j", "down":
		m.moveCheck(1)
	case "k", "up":
		m.moveCheck(-1)
	case "]", "ctrl+f", "ctrl+d", "pgdown":
		m.moveCheck(m.checkRows())
	case "[", "ctrl+b", "ctrl+u", "pgup":
		m.moveCheck(-m.checkRows())
	case "g", "home":
		m.moveCheck(-maxInt)
	case "G", "end":
		m.moveCheck(maxInt)
	case "/":
		m.searching, m.queryDraft = true, m.query
	case "esc":
		if m.query != "" {
			m.query = ""
			m.resetCheckSearch()
		} else {
			m.checkItemsFocus = false
		}
	case " ", "space":
		if !m.checkItemsFocus {
			return m, nil
		}
		l, ok := m.currentChecklist()
		if !ok {
			return m, nil
		}
		items := m.visibleCheckItems(l)
		p := m.checkPosition(l.Path)
		if p.invalid {
			m.err = fmt.Errorf("selected item changed; select it again with ↑/↓ before toggling")
			return m, nil
		}
		if p.cursor >= len(items) {
			return m, nil
		}
		it := items[p.cursor]
		return m, func() tea.Msg {
			changed, err := checklist.Toggle(l.Path, it, l.Revision)
			if err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{status: fmt.Sprintf("%d. %s %s", changed.Index, changed.Box(), alogPlain(changed.Text))}
		}
	case "r":
		return m, m.load()
	}
	return m, nil
}

func (m *Model) checkWidths() (int, int) {
	w := m.alogWidth()
	left := max(1, (w-3)*2/5)
	return left, max(1, w-left-3)
}
func (m *Model) checkBottom() string {
	var b strings.Builder
	if l, ok := m.currentChecklist(); ok && m.checkPosition(l.Path).invalid {
		b.WriteString(m.alogText("Selected item changed. Select it again with ↑/↓ before toggling.") + "\n")
	}
	if len(m.checkErrs) > 0 {
		b.WriteString(outcomeStyles["failed"].Render(m.alogText("Cannot read checklist: "+m.checkErrs[0].Error()+". Fix the file or directory, then press r.")) + "\n")
		if len(m.checkErrs) > 1 {
			b.WriteString(m.alogText(fmt.Sprintf("%d more checklist errors", len(m.checkErrs)-1)) + "\n")
		}
	}
	b.WriteString(m.renderFooter())
	b.WriteString("\n" + helpStyle.Render(m.alogText("tab lists · / search · ↑↓/j/k select · ←→/h/l pane · space toggle item · [ ] page · r refresh · M mouse · q quit")))
	return b.String()
}
func (m *Model) checkRows() int {
	// Brand + folder tab chrome + column heading, then pinned feedback/help.
	return max(1, m.paneHeight()-5-blockRows(m.checkBottom()))
}
func checkWindow(cursor, size, rows int, scroll *int) {
	if cursor < *scroll {
		*scroll = cursor
	}
	if cursor >= *scroll+rows {
		*scroll = cursor - rows + 1
	}
	*scroll = min(max(*scroll, 0), max(0, size-rows))
}
func checkStamp(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("01-02 15:04")
}

func checkFolderRows(rows int) int { return min(2, rows) }

func checkRepoLabel(repo string) string {
	if filepath.IsAbs(repo) {
		return filepath.Base(repo)
	}
	return repo
}

// A row remains one terminal line, including Unicode and hostile file text.
func checkFit(text string, width int) string {
	text = strings.ReplaceAll(strings.ReplaceAll(alogPlain(text), "\n", " "), "\t", " ")
	return fit(ansi.Truncate(text, width, "…"), width)
}
func (m *Model) checklistPane(p pane) pane {
	m.syncChecks()
	leftWidth, rightWidth := m.checkWidths()
	leftHeading, rightHeading := "CHECKLISTS / BRANCHES", "ITEMS"
	if !m.checkItemsFocus {
		leftHeading = cursorMark + " " + leftHeading
	} else {
		rightHeading = cursorMark + " " + rightHeading
	}
	p.top += "\n" + headerStyle.Render(checkFit(leftHeading, leftWidth)+" │ "+checkFit(rightHeading, rightWidth))
	p.bottom = m.checkBottom()
	rows := max(1, m.paneHeight()-blockRows(p.top)-blockRows(p.bottom))
	lists := m.visibleChecklists()
	folderRows := checkFolderRows(rows)
	checkWindow(m.checkCursor, len(lists), max(1, rows/folderRows), &m.checkScroll)
	var items []checklist.Item
	var position *checkPosition
	l, hasList := m.currentChecklist()
	if hasList {
		items = m.visibleCheckItems(l)
		position = m.checkPosition(l.Path)
		checkWindow(position.cursor, len(items), rows, &position.scroll)
	}
	left := make([]string, rows)
	right := make([]string, rows)
	for row := 0; row < rows; row++ {
		i := row/folderRows + m.checkScroll
		if i < len(lists) {
			folder := lists[i]
			label := folder.Branch
			if label == "" {
				label = "legacy: " + folder.Title
			}
			mark := "  "
			if i == m.checkCursor {
				mark = cursorMark + " "
			}
			text := mark + "📁 " + label
			if folderRows == 1 {
				text = checkTimedRow(text, checkStamp(folder.Updated), leftWidth)
			}
			if row%folderRows == 1 {
				text = checkTimedRow("    "+checkRepoLabel(folder.Repo), checkStamp(folder.Updated), leftWidth)
			}
			left[row] = checkFit(text, leftWidth)
			if i == m.checkCursor {
				if !m.checkItemsFocus {
					left[row] = rowSelected.Render(left[row])
				} else {
					left[row] = titleStyle.Render(left[row])
				}
			}
		}
		if position != nil {
			i := row + position.scroll
			if i < len(items) {
				it := items[i]
				mark := "  "
				if i == position.cursor && m.checkItemsFocus && !position.invalid {
					mark = cursorMark + " "
				}
				text := fmt.Sprintf("%s%s %d. %s", mark, it.Box(), it.Index, it.String())
				stamp := checkStamp(it.Updated)
				if rightWidth >= len(stamp)+24 {
					text = checkTimedRow(text, stamp, rightWidth)
				}
				right[row] = checkFit(text, rightWidth)
				if i == position.cursor && m.checkItemsFocus && !position.invalid {
					right[row] = rowSelected.Render(right[row])
				}
			}
		}
	}
	if len(lists) == 0 {
		message := "No checklists. Run: bermuda check new \"<title>\""
		if m.query != "" {
			message = "No matches. Esc clears search."
		}
		for i, line := range wrapText(message, leftWidth) {
			if i < rows {
				left[i] = checkFit(line, leftWidth)
			}
		}
	} else if len(items) == 0 {
		message := "No items. Run: bermuda check add \"" + l.Name + "\" \"<item>\""
		if m.query != "" {
			message = "No items match. Esc clears search."
		}
		for i, line := range wrapText(message, rightWidth) {
			if i < rows {
				right[i] = checkFit(line, rightWidth)
			}
		}
	}
	var body []string
	for i := 0; i < rows; i++ {
		body = append(body, checkFitStyled(left[i], leftWidth)+dimStyle.Render(" │ ")+checkFitStyled(right[i], rightWidth))
	}
	p.body = strings.Join(body, "\n")
	// Both columns are already independently windowed. The shared pane window
	// must not follow one column's marker and shift the other.
	m.scroll = 0
	return p
}
func checkFitStyled(text string, width int) string {
	return text + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}
func checkTimedRow(text, stamp string, width int) string {
	// At narrow widths keep the actual work readable; timestamps reappear once
	// there is room for a useful filename beside them.
	if width < len(stamp)+12 {
		return text
	}
	return checkFit(text, width-len(stamp)-1) + " " + stamp
}
