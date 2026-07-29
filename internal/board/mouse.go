package board

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Mouse handling: the wheel scrolls, a click selects, a second click on the
// already-selected row opens it.
//
// A terminal reports a click as a cell coordinate, and nothing in a rendered
// string says which row of which list a cell belongs to. So the answer is not
// worked out from the coordinate — it is recorded while the frame is drawn.
// Each render pass leaves behind a map from body line to the thing on it, and a
// click reads that map back.
//
// Recording beats arithmetic here because the arithmetic is wrong: the runs
// table injects step lines under an expanded flow, so the nth row of the table
// is not the nth line of the body, and a click near the bottom of a list with
// one flow expanded would act on a row several places from the one under the
// pointer. The map cannot drift, because it is written by the same loop that
// writes the lines.
//
// The map is one frame old by the time a click arrives — it is written by
// View() and read by the next Update(). That is exactly the frame the reader
// was looking at when they clicked, which is the frame the click is about.

// hitKind says what a body line points at. The zero value is "nothing", so a
// line nobody claimed is not clickable rather than being row zero of something.
type hitKind int

const (
	hitNone hitKind = iota
	hitJob
	hitRun
	hitFlow
	// hitDetailRun is a run in the job detail's history, which is indexed into
	// m.detailRuns rather than into the runs list.
	hitDetailRun
)

// hit is what one body line selects: an index into the *visible* list, so a
// click can never reach a row the search is hiding.
type hit struct {
	kind  hitKind
	index int
}

// tabHit is one folder tab's span on the tab row, half-open: [x0, x1).
type tabHit struct {
	x0, x1 int
	focus  focus
}

// wheelStep is how far one notch of the wheel moves. Three lines is what
// terminals and pagers have settled on, and a wheel that moved one row would
// need a dozen turns to cross a pane.
const wheelStep = 3

// mark records that the body line at index line selects k[index].
//
// Called from the render loops as they write, so the map is built from the same
// counter that produces the lines.
func (m *Model) mark(line int, k hitKind, index int) {
	if m.hits == nil {
		m.hits = map[int]hit{}
	}
	m.hits[line] = hit{kind: k, index: index}
}

// resetHits clears the previous frame's map. Called at the top of View, so a
// view that records nothing — the thread, the editor — leaves no stale rows
// behind for a click to land on.
func (m *Model) resetHits() {
	m.hits = map[int]hit{}
	m.tabRow = -1
	m.tabHits = nil
}

// recordWindow stores where the body ended up on screen: how many rows of
// chrome sit above it, and how far it is scrolled. Both are known only after
// the window has run, which is why this is called from there rather than
// computed in the handler.
func (m *Model) recordWindow(top, scroll int) {
	m.hitTop, m.hitScroll = top, scroll
}

// toggleMouse hands the mouse back to the terminal, and takes it again.
//
// A program that asks for mouse reporting takes the terminal's own drag-select
// with it, and the board is a thing left open in a split all day — the run id or
// the argv line on the detail page is there to be copied. Shift-drag overrides
// the grab in most emulators, but not in all of them, so there is a key that
// gives it back rather than a promise that the modifier will do.
func (m *Model) toggleMouse() tea.Cmd {
	m.mouseOff = !m.mouseOff
	if m.mouseOff {
		m.status = "mouse released — the terminal's own selection is back (M takes it again)"
		return func() tea.Msg { return tea.DisableMouse() }
	}
	m.status = "mouse on"
	return func() tea.Msg { return tea.EnableMouseCellMotion() }
}

// handleMouse routes a mouse event.
func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mouseOff {
		// The terminal has been told to stop reporting, but events already in
		// flight when it was told still arrive.
		return m, nil
	}
	// Only presses act. With cell motion enabled a drag arrives as a stream of
	// motion events, and treating those as clicks would drag the selection
	// across every row the pointer crossed.
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return m.wheel(-wheelStep)
	case tea.MouseButtonWheelDown:
		return m.wheel(wheelStep)
	case tea.MouseButtonLeft:
		return m.click(msg.X, msg.Y)
	}
	return m, nil
}

// wheel moves by delta rows, in whichever sense the current view moves.
//
// A list is paged from its cursor rather than scrolled, so the wheel moves the
// cursor there and lets the page follow it; the thread and the detail views
// have no selection, so the wheel moves the window itself. Doing the same thing
// to both would leave the wheel dead in exactly one of them.
func (m *Model) wheel(delta int) (tea.Model, tea.Cmd) {
	if m.editor != nil {
		// The editor is a form being typed into, not something to scroll past.
		return m, nil
	}
	if m.selectable() {
		m.cursor += delta
		m.clampCursor()
		return m, nil
	}
	if delta < 0 {
		// Scrolling up in a thread also stops it following its newest message;
		// scrollUp is what says so.
		m.scrollUp(-delta)
		return m, nil
	}
	m.scrollBy(delta)
	return m, nil
}

// selectable reports whether the current view has rows the cursor steps
// through.
func (m *Model) selectable() bool {
	switch {
	case m.editor != nil, m.runDetail != nil:
		return false
	case m.detail != nil:
		return len(m.detailRuns) > 0
	case m.picker != nil:
		// The picker is driven by its own keys and draws its own selection.
		return false
	}
	return m.focus != focusThread
}

// click selects whatever is under the pointer.
func (m *Model) click(x, y int) (tea.Model, tea.Cmd) {
	if m.editor != nil || m.flowInput != nil || m.compose != nil {
		// A box that is being typed into owns the input. A click that moved the
		// selection under an open compose box would post the message to a
		// different thread than the one it was written in.
		return m, nil
	}
	if cmd, ok := m.clickTab(x, y); ok {
		return m, cmd
	}
	h, ok := m.hits[y-m.hitTop+m.hitScroll]
	if !ok || h.kind == hitNone {
		return m, nil
	}
	// The inspector sits to the right of the jobs and flows tables and describes
	// the selected row; clicking it must not choose a different one. The gutter
	// the cursor mark occupies is part of the row, so it counts as table. The
	// other two lists have nothing beside them, so nothing to guard against.
	if h.kind == hitJob || h.kind == hitFlow {
		if x >= m.tableWidth()+2 {
			return m, nil
		}
	}
	if h.index == m.cursor {
		return m, m.activate(h.kind)
	}
	m.cursor = h.index
	m.clampCursor()
	return m, nil
}

// activate is the second click on an already-selected row.
//
// It does what `l` does, deliberately, and not what `enter` does: `l` navigates
// and `enter` on a flow launches it. A stray double click that started an agent
// would spend money on a slipped pointer, and there is no undo for that.
func (m *Model) activate(k hitKind) tea.Cmd {
	if k == hitDetailRun {
		// In the job detail `l` attaches to the running agent, which takes over
		// the terminal. That is too much for a click, so a row there is only
		// ever selected.
		return nil
	}
	return m.descend()
}

// clickTab resolves a click on the folder tabs, reporting whether it was one.
func (m *Model) clickTab(x, y int) (tea.Cmd, bool) {
	// tabRow is -1 unless the frame that was drawn actually had tabs on it, so
	// a click at that height in the job detail cannot switch lists.
	if m.tabRow < 0 || y != m.tabRow {
		return nil, false
	}
	for _, t := range m.tabHits {
		if x >= t.x0 && x < t.x1 {
			m.selectTab(t.focus)
			return nil, true
		}
	}
	return nil, false
}
