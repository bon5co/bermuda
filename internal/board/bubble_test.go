package board

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A box whose lines are not all the same width is not a box: the right border
// walks in and out as the text changes, which is the single most obvious way a
// TUI reads as broken.

func boxLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func plainBox(header, footer string, lines []string, textW, indent int) []string {
	return boxLines(bubbleBox{
		header: header, footer: footer, lines: lines, textW: textW,
		border: lipgloss.NewStyle(), head: lipgloss.NewStyle(), indent: indent,
	}.render())
}

func TestBubbleIsSquareAndCarriesItsHeaderAndFooter(t *testing.T) {
	lines := plainBox("ada · note", "05:36",
		[]string{"first line", "second"}, 30, 0)

	if len(lines) != 4 {
		t.Fatalf("box has %d rows, want a border, two lines and a border", len(lines))
	}
	want := lipgloss.Width(lines[0])
	for i, l := range lines {
		if got := lipgloss.Width(l); got != want {
			t.Errorf("row %d is %d columns, want %d — the border is not straight", i, got, want)
		}
	}
	if !strings.Contains(lines[0], "ada · note") {
		t.Error("the header is not set into the top border")
	}
	if !strings.Contains(lines[len(lines)-1], "05:36") {
		t.Error("the time is not set into the bottom border")
	}
}

// The header is written into the border, so a header longer than the box would
// push the corner off the end of the row.
func TestBubbleGrowsForItsHeaderThenTruncatesIt(t *testing.T) {
	header := "a-very-long-agent-name · claim · some-long-resource-name"
	lines := plainBox(header, "05:36", []string{"hi"}, 8, 0)
	for i, l := range lines {
		if got, want := lipgloss.Width(l), lipgloss.Width(lines[0]); got != want {
			t.Errorf("row %d is %d columns, want %d", i, got, want)
		}
	}
	// It grew to fit rather than overflowing.
	if !strings.Contains(lines[0], "a-very-long-agent-name") {
		t.Error("the box did not grow to carry its own header")
	}

	// Capped: the caller's width wins once it is the larger of the two, and
	// what does not fit is elided rather than dropped silently.
	narrow := plainBox(header, "05:36", []string{"hi"}, 8, 0)
	wide := plainBox("short · note", "05:36", []string{"hi"}, 60, 0)
	if lipgloss.Width(narrow[0]) >= lipgloss.Width(wide[0]) {
		t.Error("a long header is not being bounded against an explicit width")
	}
}

func TestBubbleIndentShiftsEveryRow(t *testing.T) {
	lines := plainBox("handler", "05:44", []string{"one", "two"}, 20, 6)
	for i, l := range lines {
		if !strings.HasPrefix(l, strings.Repeat(" ", 6)) {
			t.Errorf("row %d is not indented: %q", i, l)
		}
	}
}
