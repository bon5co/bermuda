package board

import (
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/internal/store"
)

// Choosing which conversation the board is reading.
//
// The board shows one thread at a time for the same reason the CLI reads one:
// two agents on unrelated projects should not be shouting into each other's
// context. That makes "which one am I looking at" a question the view has to
// answer at all times — an unnamed thread would make a quiet conversation and
// the wrong conversation look identical.

// openingThread is which conversation the board starts on.
//
// $BERMUDA_THREAD is honoured because a shell that exported it means every
// bermuda command in it, and a board opened from that shell showing something
// else would be the odd one out. It is not validated here: an id that does not
// exist falls back on the first load, with the reason on screen, which is a
// better answer than refusing to open the board at all.
func openingThread() string {
	if id := strings.TrimSpace(os.Getenv("BERMUDA_THREAD")); id != "" {
		return id
	}
	return store.GlobalThread
}

// currentThread is the conversation being read, and is never empty: an empty
// filter would load every thread at once, which is the thing the board is
// meant to stop doing.
func (m *Model) currentThread() string {
	if id := strings.TrimSpace(m.threadID); id != "" {
		return id
	}
	return store.GlobalThread
}

// fallBackFromAMissingThread puts the board back on global when the thread it
// was reading is gone, and reports whether it had to.
//
// Two ways that happens: $BERMUDA_THREAD naming a thread nobody created, and
// `bermuda thread rm` deleting the one on screen while the board is open.
// Neither is an error worth stopping for, and both look identical to an empty
// conversation — so the board moves and says why.
//
// The caller needs the answer because the messages that arrived with this read
// are about the thread that has just been left behind, and re-reading is the
// only way to fill the view with the one it moved to.
func (m *Model) fallBackFromAMissingThread() bool {
	if len(m.threads) == 0 {
		// Nothing loaded yet; the store always has global, so an empty list
		// means the read has not happened rather than that global is gone.
		return false
	}
	for _, t := range m.threads {
		if t.ID == m.currentThread() {
			return false
		}
	}
	m.status = "no thread " + m.currentThread() + " — showing " + store.GlobalThread
	m.threadID = store.GlobalThread
	return true
}

// threadAbout is the one-line description of a thread, or "" when it has none.
func (m *Model) threadAbout(id string) string {
	for _, t := range m.threads {
		if t.ID == id {
			return t.About
		}
	}
	return ""
}

// threadPicker is the open chooser. It holds only a cursor: the list it moves
// through is threadOrder, which the tick keeps current, so a thread created
// from a shell appears in an open picker rather than after it is reopened.
//
// It is the same order as the row under the tabs — global, then by activity —
// so a thread is in the same place whichever of the two the reader uses.
type threadPicker struct {
	cursor int
}

// openPicker opens the chooser on the thread being read, so the first thing
// under the cursor is where the reader already is.
func (m *Model) openPicker() {
	p := &threadPicker{}
	if i := threadIndex(m.threadOrder(), m.currentThread()); i > 0 {
		p.cursor = i
	}
	m.picker = p
}

// handlePickerKey drives the chooser.
//
// It owns the keyboard while open, and deliberately handles no thread keys: `i`
// here would open a compose box behind a list covering it.
func (m *Model) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "t":
		m.picker = nil
		return m, nil
	case "j", "down":
		m.picker.cursor++
		m.clampPicker()
		return m, nil
	case "k", "up":
		m.picker.cursor--
		m.clampPicker()
		return m, nil
	case "enter", "l", "right":
		return m, m.chooseThread()
	}
	return m, nil
}

func (m *Model) clampPicker() {
	if m.picker.cursor >= len(m.threads) {
		m.picker.cursor = len(m.threads) - 1
	}
	if m.picker.cursor < 0 {
		m.picker.cursor = 0
	}
}

// chooseThread switches the view to the thread under the cursor.
func (m *Model) chooseThread() tea.Cmd {
	order := m.threadOrder()
	if len(order) == 0 {
		m.picker = nil
		return nil
	}
	m.clampPicker()
	chosen := order[m.picker.cursor].ID
	m.picker = nil
	return m.switchThread(chosen)
}

// renderPicker draws the chooser: every thread, how much is in it, and what it
// is for.
//
// The message count is the load-bearing column. Choosing between conversations
// by name alone gives no clue which one is live, and the thread that has not
// been written in for a week is usually not the one being looked for.
func (m *Model) renderPicker() string {
	var b strings.Builder
	order := m.threadOrder()
	b.WriteString(headerStyle.Render("  THREADS") + "\n")
	if len(order) == 0 {
		b.WriteString(dimStyle.Render("  no threads yet — `bermuda thread new <id>`") + "\n")
		return b.String()
	}
	// The names are padded to one width so the counts line up in a column. A
	// ragged count is one the eye has to hunt for on every row, which is the
	// column being read to decide which conversation is the live one.
	label := func(t store.Thread) string {
		if t.ID == m.currentThread() {
			// Which one is being read is different from which one the cursor is
			// on, and after moving the cursor there would otherwise be nothing
			// left saying where the board came from.
			return t.ID + " (reading)"
		}
		return t.ID
	}
	width := 0
	for _, t := range order {
		if n := len(label(t)); n > width {
			width = n
		}
	}
	for i, t := range order {
		text := label(t)
		pad := strings.Repeat(" ", width-len(text))
		marker, name := "  ", dimStyle.Render(text)
		if i == m.picker.cursor {
			marker, name = "▸ ", selectedStyle.Render(text)
		}
		line := marker + name + pad + dimStyle.Render("  "+itoa(t.Messages)+" msg")
		if t.About != "" {
			line += dimStyle.Render("  " + t.About)
		}
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n" + helpStyle.Render(
		"  j/k move · enter read · esc cancel") + "\n")
	return b.String()
}
