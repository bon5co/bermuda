package board

import (
	"strings"
	"time"
)

// Composing the screen: which view is showing, and how the list, inspector, and
// page counter are stacked inside the pane.
//
// The list views are built as a pane — pinned chrome above and below, one
// scrolling body between — rather than as a single string that is windowed
// whole. What the reader is looking at is the body; what tells them where they
// are looking is the chrome, and the chrome is worth most at exactly the moment
// scrolling used to take it away.

// View renders the board, windowed to the pane height.
func (m *Model) View() string {
	// The previous frame's clickable rows go with the previous frame. Every
	// view that has any records its own below; one that records none is a view
	// where a click does nothing, which is the correct answer for the editor
	// and the conversation.
	m.resetHits()
	if m.editor != nil {
		return m.window(m.renderEditor())
	}
	if m.runDetail != nil {
		return m.window(m.renderRunDetail())
	}
	if m.detail != nil {
		return m.window(m.renderDetail())
	}
	if m.focus == focusThread && m.threadFollow {
		// The thread reads downwards, so the interesting end is the bottom. Asking
		// for an impossible scroll is how the window is told to sit at the end
		// without this view having to know how tall the rendered thread is.
		m.scroll = maxInt
	}
	return m.renderPane(m.listPane())
}

// maxInt is "as far down as the content goes", clamped by the window.
const maxInt = int(^uint(0) >> 1)

// listPane splits the list view into what stays put and what scrolls.
func (m *Model) listPane() pane {
	// The brand line and the tabs are pinned in every list view: the first says
	// whether the scheduler is alive, the second says which list this is, and a
	// reader who has scrolled into history needs both more than usual.
	brand := m.renderBrand()
	// Where the tab labels land: under the brand line, and under the tabs' own
	// top border. The brand is measured rather than counted as one row, so a
	// line added above the tabs later moves the click target with it instead of
	// leaving clicks landing a row off.
	m.tabRow = blockRows(brand) + 1
	p := pane{top: brand + "\n" + m.renderTabs("")}
	if m.focus == focusThread {
		return m.threadPane(p)
	}

	// One list at a time. Showing both halved the space for each and made the
	// jobs list — the thing this board is for — compete with history.
	var total, shown int
	switch m.focus {
	case focusJobs:
		shown, total = len(m.visibleJobs()), len(m.jobs)
	case focusFlows:
		shown, total = len(m.visibleFlows()), len(m.flowRows())
	default:
		shown, total = len(m.visibleRuns()), len(m.runs)
	}
	start, end, pageNum, pages := m.page(shown)

	var body strings.Builder
	switch m.focus {
	case focusJobs:
		body.WriteString(beside(m.renderJobs(start, end), m.inspector(m.renderInspector)))
	case focusFlows:
		body.WriteString(beside(m.renderFlows(start, end), m.inspector(m.renderFlowInspector)))
	default:
		body.WriteString(m.renderRuns(start, end))
	}
	p.body = strings.TrimSuffix(body.String(), "\n")

	var bottom strings.Builder
	// The page counter sits under the table it describes, where the eye already
	// is after reading the last row — and it is pinned, because a counter that
	// scrolled away would leave the reader unable to tell a short page from a
	// windowed one.
	if pl := m.pageLabel(shown, total, pageNum, pages); pl != "" {
		bottom.WriteString(dimStyle.Render("  "+pl) + "\n")
	}
	if m.flowInput != nil {
		// The box sits under the table it was opened from, and pinned, because
		// a box you are typing into that has scrolled off the pane is worse
		// than no box.
		bottom.WriteString(m.renderFlowInput())
	}
	bottom.WriteString(m.renderFooter())
	// Each list gets its own help line rather than one long shared one: space
	// is only meaningful on runs and enter only launches on flows, and a help
	// line that grows past the pane width wraps, which costs a row the
	// arithmetic above did not budget for.
	help := "tab threads/jobs/runs/flows · / search · [ ] page · j/k move · l/→ open · R run · f fav · F finished · p pause · n new · M mouse · q quit"
	switch m.focus {
	case focusRuns:
		help = "tab threads/jobs/runs/flows · / search · [ ] page · j/k move · space steps · l/→ open · a attach · M mouse · q quit"
	case focusFlows:
		help = "tab threads/jobs/runs/flows · / search · [ ] page · j/k move · enter run · u unpark · r reload · M mouse · q quit"
	}
	bottom.WriteString("\n" + helpStyle.Render(help))
	p.bottom = bottom.String()
	return p
}

// threadPane is the list pane for the conversation.
func (m *Model) threadPane(p pane) pane {
	// Which conversation this is, and which others there are, is the one thing
	// every other line on screen means something different without.
	p.top += "\n" + m.renderThreadRow()

	if m.picker != nil {
		// The picker replaces the thread rather than sitting beside it: the
		// list under it is the thing being left behind, and half a conversation
		// under a chooser is neither readable nor choosable.
		p.body = strings.TrimSuffix(m.renderPicker(), "\n")
		p.bottom = m.renderFooter()
		return p
	}

	// The holds block is pinned with the rest of the chrome. It is the only part
	// of this view that is *currently true* — who holds the browser and for how
	// much longer — and hunting for that in a scrolling log is the work the
	// thread exists to remove.
	// The block ends with a blank row and keeps it: the chrome and the
	// conversation are two different things, and without the gap the first
	// bubble reads as another hold.
	p.top += "\n" + m.renderHolds(time.Now())
	// The thread is rendered whole and scrolled, not paged: a conversation is
	// followed downwards, and a page boundary in the middle of one is an
	// interruption with no meaning behind it.
	p.body = strings.TrimSuffix(m.renderThreadBody(), "\n")

	var bottom strings.Builder
	if m.query != "" {
		shown, total := len(m.visibleThread()), len(m.thread)
		bottom.WriteString(dimStyle.Render("  "+itoa(shown)+" of "+itoa(total)+" match") + "\n")
	}
	if m.compose != nil {
		// The box sits at the live end of the thread, under the last message, so
		// writing reads as adding to the conversation — and pinned, because a box
		// that has scrolled off the pane is worse than no box.
		bottom.WriteString(m.renderCompose())
		bottom.WriteString(m.renderFooter())
		p.bottom = bottom.String()
		return p
	}
	bottom.WriteString(m.renderFooter())
	bottom.WriteString("\n" + helpStyle.Render(
		"tab threads/jobs/runs/flows · < > thread · t pick · i say · / search · j/k scroll · 1 live · M mouse · q quit"))
	p.bottom = bottom.String()
	return p
}

// renderFooter is the part every list view ends with: what went wrong, what
// just happened, and what is being searched for. It is shared so a view added
// later cannot quietly lose the error line.
func (m *Model) renderFooter() string {
	var b strings.Builder
	if m.err != nil {
		b.WriteString("\n" + outcomeStyles["failed"].Render("error: "+m.err.Error()) + "\n")
	} else if m.status != "" {
		b.WriteString("\n" + dimStyle.Render(m.status) + "\n")
	}
	if m.searching {
		b.WriteString("\n" + selectedStyle.Render("/"+m.queryDraft+"▏") +
			dimStyle.Render("  enter keep · esc clear") + "\n")
	} else if m.query != "" {
		b.WriteString("\n" + dimStyle.Render("filter: "+m.query+"  (esc clears)") + "\n")
	}
	return b.String()
}
