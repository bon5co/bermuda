package board

import "strings"

// defaultHeight is used before the first WindowSizeMsg arrives.
const defaultHeight = 24

// window trims a whole view to the pane height, scrolling to keep the selected
// row visible.
//
// This is for the views that are one thing from top to bottom — the job detail,
// the run detail, the edit form. The list views split themselves into pinned
// chrome and a scrolling body and call windowBody through renderPane instead,
// because their tab bar has to survive being scrolled past.
func (m *Model) window(content string) string {
	out := m.windowBody(content, m.paneHeight())
	// A whole-view render has no chrome above it, so its first line is the top
	// of the pane. See mouse.go.
	m.recordWindow(0, m.scroll)
	return out
}

// windowBody trims content to avail rows, scrolling to keep the selected row
// visible.
//
// The selected row is found by its cursor marker rather than by line number, so
// scrolling follows the selection in every view that has one without any of
// them tracking where their rows ended up after wrapping and styling.
//
// avail is the body's share of the pane, not the pane: the caller has already
// spent rows on chrome, and a body that measured itself against the whole pane
// would push that chrome off the bottom.
func (m *Model) windowBody(content string, avail int) string {
	if avail < 1 {
		avail = 1
	}
	lines := strings.Split(content, "\n")

	if len(lines) <= avail {
		m.scroll = 0
		// Everything fits, so the newest message is already on screen.
		m.threadFollow = true
		return content
	}

	// One of the rows goes to the scroll hint, since there is now something to
	// hint at. On a body of a single row it does not: what the caller asked for
	// is the ceiling, and a hint that pushed the body to two rows would push a
	// row of pinned chrome — the compose box being typed into, usually — off the
	// bottom of the pane.
	view := avail
	hinted := avail > 1
	if hinted {
		view = avail - 1
	}

	// Follow the selection: find the marked row and pull the window to it.
	cursorLine := -1
	for i, l := range lines {
		if strings.Contains(l, cursorMark) {
			cursorLine = i
			break
		}
	}
	if cursorLine >= 0 {
		if cursorLine < m.scroll+1 {
			m.scroll = cursorLine - 1
		}
		if cursorLine > m.scroll+view-2 {
			m.scroll = cursorLine - view + 2
		}
	}

	maxScroll := len(lines) - view
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	// A thread follows its newest line while the reader is at the bottom, and
	// holds position the moment they scroll up — the behaviour of every log
	// viewer, and the reason a board left open shows what just happened rather
	// than what happened first.
	m.threadFollow = m.scroll >= maxScroll

	end := m.scroll + view
	if end > len(lines) {
		end = len(lines)
	}
	out := strings.Join(lines[m.scroll:end], "\n")

	above, below := m.scroll, len(lines)-end
	if hinted && (above > 0 || below > 0) {
		out += "\n" + dimStyle.Render(scrollHint(above, below))
	}
	return out
}

func scrollHint(above, below int) string {
	switch {
	case above > 0 && below > 0:
		return "↑ " + itoa(above) + " more   ↓ " + itoa(below) + " more"
	case above > 0:
		return "↑ " + itoa(above) + " more"
	default:
		return "↓ " + itoa(below) + " more"
	}
}

// scrollBy moves the window manually, for keys that page through content that
// has no selectable rows.
func (m *Model) scrollBy(delta int) {
	m.scroll += delta
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// scrollUp moves the window back and stops the thread following its newest
// message.
//
// Following has to be given up here rather than inferred from the resulting
// position: the view pins a following thread to the bottom before the window
// runs, so a reader who scrolled up would be dragged straight back down again.
// Scrolling down needs no such call — reaching the bottom is what the window
// reads as "following" again.
func (m *Model) scrollUp(delta int) {
	m.threadFollow = false
	m.scrollBy(-delta)
}

// pageSize is how far the paging keys move.
func (m *Model) pageSize() int {
	h := m.paneHeight()
	if h > 4 {
		return h - 3
	}
	return 1
}

// cursorMark is the glyph every view uses to mark its selected row. Windowing
// finds the selection by searching for it, so it must be unique to that role.
const cursorMark = "▸"
