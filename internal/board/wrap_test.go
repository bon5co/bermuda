package board

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func timeoutAfterASecond() <-chan time.Time { return time.After(time.Second) }

// The defect this replaces: a message narrower than its column was fine, and
// anything longer was cut to twenty columns and scrolled sideways. Nothing may
// be lost now, so every test here checks the text survives as well as that it
// fits.

func widest(lines []string) int {
	w := 0
	for _, l := range lines {
		if n := lipgloss.Width(l); n > w {
			w = n
		}
	}
	return w
}

func TestWrapKeepsEveryWordAndStaysInsideTheWidth(t *testing.T) {
	body := "correction: feat/room worktree was NOT removed, it is still there"
	lines := wrapText(body, 24)
	if got := widest(lines); got > 24 {
		t.Errorf("widest line is %d columns, want at most 24", got)
	}
	if got := strings.Join(lines, " "); got != body {
		t.Errorf("wrapping changed the text:\n got %q\nwant %q", got, body)
	}
	if len(lines) < 3 {
		t.Errorf("65 characters wrapped to %d lines at width 24", len(lines))
	}
}

func TestWrapKeepsAuthoredNewlines(t *testing.T) {
	lines := wrapText("5 stories rendered\n1 rejected", 40)
	if len(lines) != 2 {
		t.Fatalf("wrapped to %d lines, want the 2 that were written", len(lines))
	}
	if lines[0] != "5 stories rendered" || lines[1] != "1 rejected" {
		t.Errorf("lines are %q, want them unchanged", lines)
	}
}

// A path has nowhere polite to break, and letting it overflow puts text outside
// the bubble's own border.
func TestWrapBreaksAWordTooLongForTheLine(t *testing.T) {
	long := "/home/dev/Projects/bermuda_worktrees/feat_room-bubbles/internal/board"
	lines := wrapText("see "+long, 20)
	if got := widest(lines); got > 20 {
		t.Errorf("widest line is %d columns, want at most 20", got)
	}
	if got := strings.ReplaceAll(strings.Join(lines, ""), " ", ""); !strings.Contains(got, long) {
		t.Error("breaking the path lost part of it")
	}
}

// The thread carries Japanese: a line measured in runes rather than display
// columns fits on paper and overflows on screen.
func TestWrapMeasuresWideRunesAsTwoColumns(t *testing.T) {
	lines := wrapText("鏡の男 は 静かです", 8)
	if got := widest(lines); got > 8 {
		t.Errorf("widest line is %d display columns, want at most 8", got)
	}
}

func TestWrapOfNothingIsOneEmptyLine(t *testing.T) {
	if got := wrapText("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("empty text wrapped to %q, want one empty line", got)
	}
}

// A width smaller than a single wide rune must still make progress rather than
// loop forever taking nothing.
func TestWrapTerminatesOnAWidthNarrowerThanOneRune(t *testing.T) {
	done := make(chan []string, 1)
	go func() { done <- wrapText("鏡鏡鏡", 1) }()
	select {
	case lines := <-done:
		if len(lines) != 3 {
			t.Errorf("wrapped to %d lines, want one per rune", len(lines))
		}
	case <-timeoutAfterASecond():
		t.Fatal("wrapping a wide rune into a 1-column line did not terminate")
	}
}
