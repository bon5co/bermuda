package board

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v3/internal/memory"
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
	if m.focus == focusForum {
		return m.forumPane(p)
	}
	if m.focus == focusMemory {
		return m.memoryPane(p)
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
	if m.prune != nil {
		// Pinned for the same reason, and more so: this box is the only place
		// the reader can see what a yes would delete.
		bottom.WriteString(m.renderPrune())
	}
	bottom.WriteString(m.renderFooter())
	// Each list gets its own help line rather than one long shared one: space
	// is only meaningful on runs and enter only launches on flows, and a help
	// line that grows past the pane width wraps, which costs a row the
	// arithmetic above did not budget for.
	help := "tab lists · / search · [ ] page · j/k move · l/→ open · R run · f fav · F finished · P prune · p pause · n new · M mouse · q quit"
	switch m.focus {
	case focusRuns:
		help = "tab lists · / search · [ ] page · j/k move · space steps · l/→ open · a attach · M mouse · q quit"
	case focusFlows:
		help = "tab lists · / search · [ ] page · j/k move · enter run · u unpark · r reload · M mouse · q quit"
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
		"tab lists · < > thread · t pick · i say · / search · j/k scroll · 1 live · M mouse · q quit"))
	p.bottom = bottom.String()
	return p
}

// forumAddr is where `bermuda forum serve` listens by default. This tab only
// names it; it does not start or watch the server — `--addr` on that command
// is the source of truth, and this is just what a reader gets with no flags.
const forumAddr = "http://127.0.0.1:8422"

// forumPane is the list pane for the FORUM tab: one line, no table, because
// there is nothing here to select — a human opens the URL in a browser.
func (m *Model) forumPane(p pane) pane {
	p.body = dimStyle.Render("  read-only web view, once running:") + "\n\n" +
		"  " + selectedStyle.Render(forumAddr) + "\n\n" +
		dimStyle.Render("  start it with: ") + "bermuda forum serve"
	var bottom strings.Builder
	bottom.WriteString(m.renderFooter())
	bottom.WriteString("\n" + helpStyle.Render("tab lists · M mouse · q quit"))
	p.bottom = bottom.String()
	return p
}

// summaryWidth is how wide the tab rule runs over a pane that has no table
// under it. Wide enough for the longest line these panes draw, narrow enough
// that the rule does not stretch across an empty pane pretending a table ended
// there.
const summaryWidth = 64

// memoryPane is the list pane for the MEMORY tab: a summary, not a list.
//
// Nothing here is selectable on purpose. The notes are Obsidian Markdown and
// the place to read one is Obsidian, or an editor — a second, worse reader
// built into the board would be a promise it cannot keep. What the board can
// usefully answer is the pair of questions a human actually has about the
// memory layer: where is it wired, and is anything accumulating in it.
func (m *Model) memoryPane(p pane) pane {
	st := m.memory
	var b strings.Builder

	where := st.Dir
	if where == "" {
		where = "not configured"
	}
	b.WriteString(dimStyle.Render("  notes live in") + "\n")
	b.WriteString("  " + selectedStyle.Render(where) + "\n")
	if st.LinkedTo != "" {
		// The symlink is the normal wiring, not an oddity: `memory init
		// --vault` makes it, and a reader who sees only the link path would
		// not know which vault their agents are actually writing into.
		b.WriteString(dimStyle.Render("  → "+st.LinkedTo) + "\n")
	}
	b.WriteString("\n")

	switch {
	case !st.Present:
		b.WriteString(dimStyle.Render("  nothing here yet — wire it up with: ") +
			"bermuda memory init")
	case st.Notes == 0 && !st.HasIndex:
		b.WriteString(dimStyle.Render("  initialised, no notes and no index yet"))
	default:
		b.WriteString(summaryLine("notes", strconv.Itoa(st.Notes)))
		b.WriteString(summaryLine("archived", strconv.Itoa(st.Archived)))
		b.WriteString(summaryLine("links", strconv.Itoa(st.Links)))
		b.WriteString(summaryLine("size", humanBytes(st.Bytes)))
		if st.HasIndex {
			b.WriteString(summaryLine("index", fmt.Sprintf("%s, %d lines",
				memory.IndexName, st.Entries)))
		} else {
			// An index-less memory still works for a human and is useless to
			// an agent, which loads the index and nothing else. Worth naming.
			b.WriteString(summaryLine("index", "missing — agents load "+
				memory.IndexName+" and will find nothing"))
		}
		if st.Newest != "" {
			b.WriteString(summaryLine("newest", st.Newest+
				dimStyle.Render("  "+ago(st.Written))))
		}
		b.WriteString(m.searchLines())
	}
	p.body = strings.TrimSuffix(b.String(), "\n")

	var bottom strings.Builder
	bottom.WriteString(m.renderFooter())
	bottom.WriteString("\n" + helpStyle.Render("tab lists · r refresh · M mouse · q quit"))
	p.bottom = bottom.String()
	return p
}

// searchLines is what the MEMORY tab says about searching the notes.
//
// Three questions, in the order a reader has them: can I search this at all,
// is the index being kept up, and how long did that cost. The last one is
// there because it is the number that decides whether the sweep on the
// daemon's timer was a good idea — it has been tens of milliseconds on a
// vault of hundreds of notes, and the day it is not, this line is where that
// shows up rather than in a mystery about a busy machine.
//
// Everything rendered here was recorded by the sweep. The board does not hash
// a vault to draw a pane.
func (m *Model) searchLines() string {
	g := m.glance
	switch {
	case !g.Present || g.Files == 0:
		return summaryLine("search", dimStyle.Render("not indexed — ")+
			"bermuda memory index")
	default:
		var b strings.Builder
		b.WriteString(summaryLine("search", fmt.Sprintf("%s chunk(s) from %s note(s)",
			thousands(g.Chunks), thousands(g.Files))))
		if !g.Swept.IsZero() {
			b.WriteString(summaryLine("swept", took(g.Took)+
				dimStyle.Render("  "+ago(g.Swept))))
		}
		if !g.Wrote.IsZero() {
			b.WriteString(summaryLine("indexed", fmt.Sprintf("%d note(s), %d chunk(s) in %s",
				g.WroteNotes, g.WroteChunks, took(g.WroteTook))+
				dimStyle.Render("  "+ago(g.Wrote))))
		}
		return b.String()
	}
}

// took renders a duration the way a person reads one: milliseconds while it is
// small enough to be a rounding error, seconds once it is not.
func took(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

// thousands groups a count so five figures can be read at a glance. Chunk
// counts run into the thousands on any real vault, and "5056" is a number the
// eye has to parse where "5,056" is one it recognises.
func thousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// summaryLine is one label-and-value row, labels aligned so the values read as
// a column rather than as prose.
func summaryLine(label, value string) string {
	return "  " + dimStyle.Render(fmt.Sprintf("%-9s", label)) + value + "\n"
}

// humanBytes keeps the size to three characters and a unit. Memory is measured
// in kilobytes for a long time and the exact byte count answers nothing.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
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
