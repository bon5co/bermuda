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

// Each checklist remembers its item identity. CLI numbers refer to page order;
// the tree sorts display rows independently and can reorder after a toggle.
type checkPosition struct {
	index, cursor      int
	identity, snapshot string
	identityCount      int
	invalid            bool
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

// Refresh preserves the selected folder path and item identity while the tree
// sorts by update time. Only the selected checklist is expanded.
func (m *Model) syncChecks() {
	lists := m.visibleChecklists()
	foundPath := false
	for i, l := range lists {
		if l.Path == m.checkPath {
			foundPath = true
			m.checkCursor = i
			break
		}
	}
	if m.checkPath != "" && !foundPath {
		m.checkItemsFocus = false
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
	m.checkItemsFocus = false
	for _, p := range m.checkPositions {
		*p = checkPosition{}
	}
	m.syncChecks()
}

// checkTreeRows is the visible tree: every folder, and children only for the
// currently selected checklist. Empty hints are visible but not selectable.
type checkTreeRow struct{ folder, item int }

func (m *Model) checkTreeRows() []checkTreeRow {
	var rows []checkTreeRow
	for i, l := range m.visibleChecklists() {
		rows = append(rows, checkTreeRow{i, -1})
		if l.Path != m.checkPath {
			continue
		}
		items := m.visibleCheckItems(l)
		if len(items) == 0 {
			rows = append(rows, checkTreeRow{i, -2})
		}
		for j := range items {
			rows = append(rows, checkTreeRow{i, j})
		}
	}
	return rows
}
func (m *Model) selectedCheckTreeRow(rows []checkTreeRow) int {
	item := -1
	if m.checkItemsFocus {
		if l, ok := m.currentChecklist(); ok {
			item = m.checkPosition(l.Path).cursor
		}
	}
	for i, row := range rows {
		if row.folder == m.checkCursor && row.item == item {
			return i
		}
	}
	for i, row := range rows {
		if row.folder == m.checkCursor && row.item == -1 {
			return i
		}
	}
	return 0
}
func (m *Model) selectCheckTreeRow(row checkTreeRow) {
	lists := m.visibleChecklists()
	if row.folder < 0 || row.folder >= len(lists) || row.item < -1 {
		return
	}
	l := lists[row.folder]
	if row.item < 0 {
		m.checkCursor, m.checkPath, m.checkItemsFocus = row.folder, l.Path, false
		m.syncChecks()
		return
	}
	items := m.visibleCheckItems(l)
	if row.item >= len(items) {
		return
	}
	m.checkCursor, m.checkPath, m.checkItemsFocus = row.folder, l.Path, true
	p := m.checkPosition(l.Path)
	p.cursor, p.index, p.identity, p.invalid = row.item, items[row.item].Index, items[row.item].String(), false
	p.identityCount = checkIdentityCount(l, p.identity)
}
func (m *Model) moveCheck(delta int) {
	if m.checkItemsFocus {
		if l, ok := m.currentChecklist(); ok {
			p := m.checkPosition(l.Path)
			target := checkMove(p.cursor, delta, len(m.visibleCheckItems(l)))
			// A clamped key must not recover a stale item identity.
			if target != p.cursor {
				m.selectCheckTreeRow(checkTreeRow{m.checkCursor, target})
			}
		}
		return
	}
	// Folder mode skips the expanded children; item mode never leaves its parent.
	target := checkMove(m.checkCursor, delta, len(m.visibleChecklists()))
	if target != m.checkCursor {
		m.selectCheckTreeRow(checkTreeRow{target, -1})
	}
}

func (m *Model) enterCheckItems() {
	if m.checkItemsFocus {
		return
	}
	if l, ok := m.currentChecklist(); ok && len(m.visibleCheckItems(l)) > 0 {
		m.selectCheckTreeRow(checkTreeRow{m.checkCursor, 0})
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
		m.enterCheckItems()
	case "h", "left":
		if m.checkItemsFocus {
			m.selectCheckTreeRow(checkTreeRow{m.checkCursor, -1})
		}
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
		m.checkItemsFocus = false
		m.searching, m.queryDraft = true, m.query
	case "esc":
		if m.checkItemsFocus {
			m.selectCheckTreeRow(checkTreeRow{m.checkCursor, -1})
		} else if m.query != "" {
			m.query = ""
			m.resetCheckSearch()
		}
	case " ", "space":
		if !m.checkItemsFocus {
			m.enterCheckItems()
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

func (m *Model) checkBottom() string {
	var b strings.Builder
	if l, ok := m.currentChecklist(); ok && m.checkItemsFocus && m.checkPosition(l.Path).invalid {
		b.WriteString(m.alogText("Selected item changed. Select it again with ↑/↓ before toggling.") + "\n")
	}
	if len(m.checkErrs) > 0 {
		b.WriteString(outcomeStyles["failed"].Render(m.alogText("Cannot read checklist: "+m.checkErrs[0].Error()+". Fix the file or directory, then press r.")) + "\n")
		if len(m.checkErrs) > 1 {
			b.WriteString(m.alogText(fmt.Sprintf("%d more checklist errors", len(m.checkErrs)-1)) + "\n")
		}
	}
	b.WriteString(m.renderFooter())
	help := "tab lists · / search · ↑↓/j/k checklists · enter/space/→ items · [ ] page · r refresh · M mouse · q quit"
	if m.checkItemsFocus {
		help = "tab lists · / search · ↑↓/j/k items · space toggle · ←/h/esc parent · [ ] page · r refresh · M mouse · q quit"
	}
	b.WriteString("\n" + checkMutedStyle.Render(m.alogText(help)))
	return b.String()
}
func (m *Model) checkRows() int {
	// Brand, tabs and tree heading, then pinned feedback/help.
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
	return t.Local().Format("2006-01-02 15:04")
}

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

var (
	checkFolderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	// Keep muted checklist text readable on the board's dark terminal surface.
	checkMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
)

func (m *Model) checklistPane(p pane) pane {
	m.syncChecks()
	width := m.alogWidth()
	p.top += "\n" + headerStyle.Render(checkFit("CHECKLISTS · newest first", width))
	p.bottom = m.checkBottom()
	available := max(1, m.paneHeight()-blockRows(p.top)-blockRows(p.bottom))
	rows := m.checkTreeRows()
	selected := m.selectedCheckTreeRow(rows)
	checkWindow(selected, len(rows), available, &m.checkScroll)
	lists := m.visibleChecklists()
	var body []string
	for screenRow := 0; screenRow < available; screenRow++ {
		at := screenRow + m.checkScroll
		if at >= len(rows) {
			body = append(body, strings.Repeat(" ", width))
			continue
		}
		row := rows[at]
		l := lists[row.folder]
		marker := "  "
		isSelected := at == selected
		if row.item >= 0 && m.checkPosition(l.Path).invalid {
			isSelected = false
		}
		if isSelected {
			marker = cursorMark + " "
		}
		switch {
		case row.item == -1:
			label := checkRepoLabel(l.Repo) + " · " + l.Branch
			if l.Branch == "" {
				label = "legacy: " + l.Title
			}
			style := checkFolderStyle
			if isSelected {
				style = rowSelected
			}
			body = append(body, checkTreeLine(marker+"📁 "+label, checkStamp(l.Updated), width, style))
			m.mark(screenRow, hitCheckFolder, row.folder)
		case row.item == -2:
			message := "    No items. Run: bermuda check add \"" + l.Name + "\" \"<item>\""
			if m.query != "" {
				message = "    No items match. Esc clears search."
			}
			body = append(body, checkMutedStyle.Render(checkFit(message, width)))
		default:
			it := m.visibleCheckItems(l)[row.item]
			style := lipgloss.NewStyle()
			if it.Done {
				style = checkMutedStyle
			}
			if isSelected {
				style = rowSelected
			}
			text := fmt.Sprintf("%s    %s %d. %s", marker, it.Box(), it.Index, it.String())
			body = append(body, checkTreeLine(text, checkStamp(it.Updated), width, style))
			m.mark(screenRow, hitCheckItem, row.item)
		}
	}
	if len(lists) == 0 {
		message := "No checklists. Run: bermuda check new \"<title>\""
		if m.query != "" {
			message = "No matches. Esc clears search."
		}
		for i, line := range wrapText(message, width) {
			if i < available {
				body[i] = checkFit(line, width)
			}
		}
	}
	p.body = strings.Join(body, "\n")
	// The tree owns its scroll; body hit mappings refer to the rendered rows.
	m.scroll = 0
	return p
}

func checkTreeLine(text, stamp string, width int, style lipgloss.Style) string {
	// Keep a full date at the terminal's right edge whenever it can fit alongside
	// a visible cursor. Extremely narrow terminals retain the work label.
	if width < len(stamp)+3 {
		return style.Render(checkFit(text, width))
	}
	return style.Render(checkFit(text, width-len(stamp)-1)) + " " + checkMutedStyle.Render(stamp)
}
