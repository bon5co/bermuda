package board

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The row of thread names under the tabs.
//
//	‹ global · game-server · webapp · bermuda ›
//
// Names and nothing else. A count, an unread badge or a last-activity stamp
// beside each name were all considered and all rejected for the board: this row
// is a way of moving between conversations, and every extra field on it is
// something the eye has to skip past to find the name it wants. `bermuda thread
// list` still carries the counts and the timestamps, because that is a report
// somebody asked for rather than a thing sitting on screen all day.
//
// Activity order is what makes it usable without those numbers: the thread
// somebody just posted in is near the front, so the row itself says where the
// noise is.

// threadRow geometry.
const (
	// threadRowSep separates two names. It is a middle dot rather than a pipe
	// or a slash because the names are a list, not a path or a choice.
	threadRowSep = " · "
	// threadRowLead and threadRowTail are the markers that hold the row. They
	// carry "there is more of this list" when it has been truncated, which is
	// why the row can be cut short instead of wrapping.
	threadRowLead = "‹ "
	threadRowTail = " ›"
)

// threadOrder is every conversation in the order the board lists them, and is
// the one order the board uses — the row, the picker, and the keys that step
// between threads all read it, so a thread is in the same place wherever the
// reader looks.
//
// Global is first and stays first: it is where every unqualified command writes,
// so it is the one name that must be findable without reading the row. The rest
// follow by most recent activity, newest first, which is what makes a row of
// bare names useful — the conversation somebody is having right now moves to the
// front of it.
func (m *Model) threadOrder() []store.Thread {
	out := append([]store.Thread(nil), m.threads...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ag, bg := a.ID == store.GlobalThread, b.ID == store.GlobalThread; ag != bg {
			return ag
		}
		if !a.LastAt.Equal(b.LastAt) {
			return a.LastAt.After(b.LastAt)
		}
		// A thread nobody has posted in has no activity to sort by, and two of
		// them would otherwise swap places between renders.
		return a.ID < b.ID
	})
	return out
}

// threadIndex is where a thread sits in that order, or -1.
func threadIndex(threads []store.Thread, id string) int {
	for i, t := range threads {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// stepThread moves to the next or previous conversation in the row's order.
//
// The ends do not wrap. The row is a line with two ends and the reader can see
// both of them, so a `>` on the last name that jumped back to global would move
// them somewhere they were not pointing at — and a switch re-reads the store,
// which is not a thing to do by accident.
func (m *Model) stepThread(delta int) tea.Cmd {
	order := m.threadOrder()
	if len(order) < 2 {
		return nil
	}
	i := threadIndex(order, m.currentThread())
	if i < 0 {
		// The thread on screen is not in the list, which the next read will fix
		// by falling back to global. Until then, step from the front.
		i = 0
	}
	next := i + delta
	if next < 0 || next >= len(order) {
		return nil
	}
	return m.switchThread(order[next].ID)
}

// switchThread puts the board on another conversation.
//
// A search belongs to the conversation it was typed in. Carried across, a filter
// matching nothing here would make a thread full of messages look empty, and the
// reader has no reason to suspect the filter — they were looking at the list
// they wanted a moment ago.
func (m *Model) switchThread(id string) tea.Cmd {
	if id == "" || id == m.currentThread() {
		return nil
	}
	m.threadID = id
	m.query, m.scroll, m.threadFollow = "", 0, true
	m.status = "reading " + id
	// The messages are loaded per thread, so switching has to re-read rather
	// than re-filter what is already in hand.
	return m.load()
}

// renderThreadRow draws the row: every thread that fits, the current one lit.
//
// It is exactly one line, always. A row that wrapped would cost a second line
// whenever somebody added a thread, and every piece of height arithmetic under
// it would be wrong by one — which is how a board starts scrolling the terminal
// and smearing itself.
func (m *Model) renderThreadRow() string {
	order := m.threadOrder()
	margin := strings.Repeat(" ", threadMargin)
	if len(order) == 0 {
		// Before the first read lands. Naming the current thread is still the
		// answer to "what am I looking at", which is the row's whole job.
		return margin + dimStyle.Render(threadRowLead) +
			selectedStyle.Render(m.currentThread()) + dimStyle.Render(threadRowTail)
	}

	current := threadIndex(order, m.currentThread())
	if current < 0 {
		current = 0
	}
	first, last := threadRowWindow(order, current, m.threadRowBudget())

	var b strings.Builder
	b.WriteString(margin + dimStyle.Render(threadRowLead))
	for i := first; i <= last; i++ {
		if i > first {
			b.WriteString(dimStyle.Render(threadRowSep))
		}
		name := truncate(order[i].ID, m.threadRowBudget())
		if i == current {
			b.WriteString(selectedStyle.Render(name))
			continue
		}
		b.WriteString(dimStyle.Render(name))
	}
	b.WriteString(dimStyle.Render(threadRowTail))

	// The description of the thread being read goes after the closing marker
	// rather than beside its name in the list: it is about one thread, and one
	// thread's prose set among the names would read as another name. It is the
	// first thing dropped when the row is tight, since the row still works
	// without it and does not work truncated.
	if about := m.threadAbout(m.currentThread()); about != "" {
		if room := m.threadRowWidth() - lipgloss.Width(b.String()) - 2; room > 8 {
			b.WriteString(dimStyle.Render("  " + truncate(about, room)))
		}
	}
	return b.String()
}

// threadRowWindow is the run of names to show: as many as fit, always
// including the one being read.
//
// It grows outwards from the current thread rather than starting at global,
// because the name that must never be cut is the one saying where the reader
// is. What falls off either end is carried by the ‹ › markers.
func threadRowWindow(order []store.Thread, current, budget int) (first, last int) {
	first, last = current, current
	used := lipgloss.Width(order[current].ID)
	for {
		grew := false
		if last+1 < len(order) {
			if w := used + lipgloss.Width(threadRowSep) + lipgloss.Width(order[last+1].ID); w <= budget {
				used, last, grew = w, last+1, true
			}
		}
		if first > 0 {
			if w := used + lipgloss.Width(threadRowSep) + lipgloss.Width(order[first-1].ID); w <= budget {
				used, first, grew = w, first-1, true
			}
		}
		if !grew {
			return first, last
		}
	}
}

// threadRowWidth is how many columns the row may take. It measures the pane
// directly rather than trusting contentWidth, which has a floor of 24 that a
// genuinely narrow split is under — and a row past the edge wraps, which is the
// one thing this row must not do.
func (m *Model) threadRowWidth() int {
	w := m.contentWidth()
	if m.width > 0 && m.width-threadMargin < w {
		w = m.width - threadMargin
	}
	return max(w, 1)
}

// threadRowBudget is what is left for names once the markers have taken theirs.
func (m *Model) threadRowBudget() int {
	return max(m.threadRowWidth()-lipgloss.Width(threadRowLead)-lipgloss.Width(threadRowTail), 1)
}
