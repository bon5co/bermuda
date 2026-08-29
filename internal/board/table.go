package board

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// column describes one table column.
//
// Widths are display columns, not bytes: multi-byte and wide runes are common
// here (the ellipsis, the cursor glyph, box characters), and measuring them as
// bytes is what makes cells overflow into their neighbours.
type column struct {
	title string
	width int
	// flex marks the column that absorbs leftover space, and that is shrunk
	// first when the pane is narrow.
	flex bool
	// max caps how wide a flex column may grow. Without it, a full-width pane
	// pours every spare column into one field, leaving a stripe of padding
	// between the first column and the rest of the table.
	max int
}

// layout resolves column widths to the available width.
//
// Every column is guaranteed at least enough room for an ellipsis, so a very
// narrow pane produces cramped output rather than negative widths and garbled
// rows.
func layout(cols []column, avail int) []int {
	widths := make([]int, len(cols))
	fixed, flexIdx := 0, -1
	for i, c := range cols {
		widths[i] = c.width
		if c.flex {
			flexIdx = i
			continue
		}
		fixed += c.width
	}
	gaps := len(cols) - 1 // one space between columns

	if flexIdx >= 0 {
		widths[flexIdx] = avail - fixed - gaps
		if m := cols[flexIdx].max; m > 0 && widths[flexIdx] > m {
			widths[flexIdx] = m
		}
		if widths[flexIdx] < 8 {
			widths[flexIdx] = 8
		}
	}

	// Still too wide: shrink from the right, since the leftmost columns carry
	// identity and the rightmost carry detail.
	total := gaps
	for _, w := range widths {
		total += w
	}
	for i := len(widths) - 1; i >= 0 && total > avail; i-- {
		over := total - avail
		room := widths[i] - 3
		if room <= 0 {
			continue
		}
		cut := min(over, room)
		widths[i] -= cut
		total -= cut
	}
	return widths
}

// row renders cells into fixed-width columns, truncating what does not fit.
//
// Styling is applied by the caller to each cell *after* fitting, because a
// styled string carries escape sequences that must not be counted as width.
func row(cells []string, widths []int) string {
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(fit(c, widths[i]))
	}
	return strings.TrimRight(b.String(), " ")
}

// fit truncates or pads a plain string to exactly w display columns.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return pad(truncate(s, w), w)
}

// pad right-pads to w display columns.
func pad(s string, w int) string {
	n := lipgloss.Width(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// truncate shortens a string to w display columns, ending with an ellipsis
// when anything was removed.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	used := 0
	for _, r := range runes {
		rw := lipgloss.Width(string(r))
		if used+rw > w-1 {
			break
		}
		out = append(out, r)
		used += rw
	}
	return string(out) + "…"
}
