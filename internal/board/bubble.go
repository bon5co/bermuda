package board

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// A rounded box with text set into its borders.
//
// The header goes in the top border and the footer in the bottom one, which is
// what makes a bubble read as a chat message rather than as a framed table: the
// two things a reader needs to place a message — who said it and when — sit on
// the frame instead of stealing rows from what was said.
//
// It is shared by the thread and the compose box so the box you type into is
// the same shape as the message it becomes.

// bubbleBox is the geometry of one rendered box.
type bubbleBox struct {
	// header is set into the top border, footer into the bottom.
	header, footer string
	lines          []string
	// textW is the width of the text column, excluding the border and the one
	// space of padding on each side.
	textW int
	// maxW is the hard ceiling on textW. The box grows to carry a long header,
	// and without a ceiling a long agent name would push a bubble past the edge
	// of a narrow pane and wrap in the terminal.
	maxW int
	// border colours the frame; head colours the header text.
	border, head lipgloss.Style
	// decorate colours parts of a body line, and is applied after the line has
	// been fitted to the column. Nil leaves the text plain.
	decorate func(string) string
	// indent shifts the whole box right. It is how a human's messages are told
	// apart from an agent's without reading the name.
	indent int
}

// render draws the box.
func (b bubbleBox) render() string {
	textW := b.textW
	// The borders carry text, so the box cannot be narrower than what is set
	// into them: one column of rule plus a space on either side.
	if n := lipgloss.Width(b.header) + 1; n > textW {
		textW = n
	}
	if n := lipgloss.Width(b.footer) + 1; n > textW {
		textW = n
	}
	if b.maxW > 0 && textW > b.maxW {
		textW = b.maxW
	}
	if textW < 2 {
		textW = 2
	}
	header := truncate(b.header, textW-1)
	footer := truncate(b.footer, textW-1)

	inner := textW + 2 // the padding space on each side
	pad := strings.Repeat(" ", b.indent)

	var out strings.Builder
	topFill := inner - 3 - lipgloss.Width(header)
	out.WriteString(pad +
		b.border.Render("╭─ ") + b.head.Render(header) +
		b.border.Render(" "+strings.Repeat("─", max(topFill, 0))+"╮") + "\n")
	for _, l := range b.lines {
		// Fitted first, then coloured. A decorated line carries escape
		// sequences, and fit counts runes: colouring first would have the
		// padding measured against the colour codes and the right-hand border
		// would step in and out down the bubble.
		text := fit(l, textW)
		if b.decorate != nil {
			text = b.decorate(text)
		}
		out.WriteString(pad + b.border.Render("│ ") + text +
			b.border.Render(" │") + "\n")
	}
	// The bottom border spends one fewer column than the top: it opens with a
	// corner and a rule, where the top opens with a corner, a rule and a space.
	botFill := inner - 2 - lipgloss.Width(footer)
	out.WriteString(pad +
		b.border.Render("╰"+strings.Repeat("─", max(botFill, 0))+" ") +
		dimStyle.Render(footer) + b.border.Render(" ╯") + "\n")
	return out.String()
}
