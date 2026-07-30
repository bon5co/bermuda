package board

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/v2/internal/mention"
)

// Showing who a message was addressed to.
//
// A mention is the one part of a message that did something — it reached a live
// agent — and in a wall of bubbles it is otherwise four characters of prose. It
// is coloured so a reader scrolling the thread can see that a message was
// addressed to somebody, and to whom, without reading it.

// mentionStyle marks an @name. Cyan is not used by any message kind, so a
// mention stands out inside a bubble whatever colour the bubble's frame is.
var mentionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))

// highlightMentions colours every @name in a line that has already been fitted
// to its column.
//
// After fitting, not before, and that ordering is load-bearing: fit and
// truncate measure in runes and would count an escape sequence as text, so a
// styled string handed to them comes back cut through the middle of a colour
// code. This is the same rule the tables follow, where cells are styled after
// they are sized.
func highlightMentions(line string) string {
	found := mention.Find(line)
	if len(found) == 0 {
		return line
	}
	var b strings.Builder
	last := 0
	for _, m := range found {
		b.WriteString(line[last:m.Start])
		b.WriteString(mentionStyle.Render(line[m.Start:m.End]))
		last = m.End
	}
	b.WriteString(line[last:])
	return b.String()
}
