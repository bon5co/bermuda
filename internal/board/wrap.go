package board

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Wrapping text to a width, in display columns.
//
// The tables truncate — a cell is a field, and a field that does not fit is
// still recognisable from its first few characters. The thread does not: a
// message is prose, and prose cut at twenty columns says nothing at all. So the
// thread wraps, and this is the only place that knows how.

// wrapText breaks text into lines no wider than w display columns.
//
// Newlines already in the text are kept as hard breaks. A message written as
// several lines was written that way deliberately, and flattening it is how a
// multi-part note ends up reading as one run-on sentence.
func wrapText(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	for _, para := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, wrapLine(para, w)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// wrapLine wraps one newline-free line on word boundaries.
func wrapLine(s string, w int) []string {
	var lines []string
	cur := ""
	flush := func() {
		lines = append(lines, cur)
		cur = ""
	}
	for _, word := range strings.Fields(s) {
		// A word wider than the whole line is broken rather than left to
		// overflow the bubble: paths, URLs and commit hashes are the common
		// case here and there is nowhere polite to break them.
		for lipgloss.Width(word) > w {
			space := w - lipgloss.Width(cur)
			if cur != "" {
				space-- // the space that would separate it
			}
			if space < 1 {
				flush()
				continue
			}
			head, rest := cutWidth(word, space)
			if cur != "" {
				cur += " "
			}
			cur += head
			word = rest
			flush()
		}
		if word == "" {
			continue
		}
		add := lipgloss.Width(word)
		if cur != "" {
			add++
		}
		if lipgloss.Width(cur)+add > w {
			flush()
		}
		if cur != "" {
			cur += " "
		}
		cur += word
	}
	if cur != "" {
		flush()
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// cutWidth splits s at w display columns, returning the head and the rest.
//
// It always takes at least one rune. Without that a wide rune against a
// one-column budget would return an empty head and leave the caller looping on
// the same word forever.
func cutWidth(s string, w int) (string, string) {
	used := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w && i > 0 {
			return s[:i], s[i:]
		}
		used += rw
	}
	return s, ""
}
