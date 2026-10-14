package board

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Text that does not fit its column scrolls, the way a station board scrolls a
// destination too long for its display, instead of being cut off with an
// ellipsis. An ellipsis tells you something was hidden; scrolling tells you
// what it was.
//
// The animation is driven by its own fast tick rather than the data refresh:
// re-reading the store every 350ms to move some text would be absurd, and
// refreshing data every 350ms would make the board flicker.

const (
	// marqueeInterval is how often the scroll advances.
	marqueeInterval = 320 * time.Millisecond
	// marqueeHold is how many frames the text rests at its start before
	// scrolling, so short overflows can be read without waiting for a cycle.
	marqueeHold = 6
	// marqueeGap separates the end of the text from its repeat, so the loop is
	// visible rather than reading as one run-on string.
	marqueeGap = "   ·   "
)

type marqueeTickMsg struct{}

func marqueeTick() tea.Cmd {
	return tea.Tick(marqueeInterval, func(time.Time) tea.Msg { return marqueeTickMsg{} })
}

// fitMarquee sizes a string to a width, scrolling it when it does not fit.
//
// frame advances once per tick. Every overflowing cell shares the same frame,
// so the whole board scrolls in step rather than each cell drifting on its own
// phase, which is what makes a wall of scrolling text readable.
func fitMarquee(s string, w, frame int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return pad(s, w)
	}

	loop := s + marqueeGap
	runes := []rune(loop)

	// Hold at the start, then advance one rune per frame, then hold again at
	// the top of the next cycle.
	cycle := len(runes) + marqueeHold
	pos := frame % cycle
	offset := pos - marqueeHold
	if offset < 0 {
		offset = 0
	}

	// Read w columns from offset, wrapping around the loop so the text streams
	// continuously instead of blanking at the end.
	var b strings.Builder
	used := 0
	for i := 0; used < w; i++ {
		r := runes[(offset+i)%len(runes)]
		rw := lipgloss.Width(string(r))
		if used+rw > w {
			// A wide rune straddling the edge would overflow the column, so
			// pad instead of drawing half of it.
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return pad(b.String(), w)
}

// marquee fits a cell the way fitMarquee does, and notes when the cell is one
// that scrolls.
//
// The note is what lets the tick stop. Every frame the tick asks for is a full
// render of the board, and on a board where everything fits each of those
// renders draws the same picture as the last — three times a second, for as
// long as the pane is open. So the tick only runs while the last frame had
// something in it that moves.
func (m *Model) marquee(s string, w int) string {
	if w > 0 && lipgloss.Width(s) > w {
		m.scrolling = true
	}
	return fitMarquee(s, w, m.frame)
}

// armMarquee restarts the tick after a frame that found text to scroll while
// the tick was stopped.
//
// It runs at the end of View, which cannot return a command, so the tick is
// delivered to the program from outside the event loop instead. Without that
// the first scroll after, say, the selection moving onto a long name would
// wait for whatever message happened to come next — up to a whole refresh.
func (m *Model) armMarquee() {
	if !m.scrolling || m.marqueeOn || m.send == nil {
		return
	}
	m.marqueeOn = true
	send := m.send
	time.AfterFunc(marqueeInterval, func() { send(marqueeTickMsg{}) })
}

// onMarqueeTick advances the scroll, or lets the tick lapse when the last
// frame had nothing that scrolls.
func (m *Model) onMarqueeTick() tea.Cmd {
	if !m.scrolling {
		m.marqueeOn = false
		return nil
	}
	m.frame++
	return marqueeTick()
}
