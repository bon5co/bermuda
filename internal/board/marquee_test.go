package board

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A cell must always occupy exactly its column, at every frame of the scroll —
// otherwise the animation would shift every column to its right.
func TestMarqueeKeepsColumnWidth(t *testing.T) {
	for _, s := range []string{"short", "a name far too long for its column", "日本語のとても長いジョブ名"} {
		for _, w := range []int{1, 6, 12, 20} {
			for frame := 0; frame < 40; frame++ {
				got := fitMarquee(s, w, frame)
				if lipgloss.Width(got) != w {
					t.Fatalf("fitMarquee(%q, %d, %d) is %d columns, want %d: %q",
						s, w, frame, lipgloss.Width(got), w, got)
				}
			}
		}
	}
}

func TestMarqueeLeavesFittingTextAlone(t *testing.T) {
	for frame := 0; frame < 20; frame++ {
		if got := fitMarquee("ok", 10, frame); strings.TrimRight(got, " ") != "ok" {
			t.Fatalf("frame %d moved text that already fits: %q", frame, got)
		}
	}
}

// It holds at the start before scrolling, so a short overflow can be read
// without waiting for a full cycle.
func TestMarqueeHoldsThenScrolls(t *testing.T) {
	s := "abcdefghijklmnop"
	first := fitMarquee(s, 8, 0)
	for frame := 1; frame < marqueeHold; frame++ {
		if fitMarquee(s, 8, frame) != first {
			t.Fatalf("frame %d moved during the hold", frame)
		}
	}
	if fitMarquee(s, 8, marqueeHold+1) == first {
		t.Error("text never scrolled after the hold")
	}
}

// The scroll wraps, so the text streams continuously rather than blanking at
// the end of the string.
func TestMarqueeWrapsAround(t *testing.T) {
	s := "abcdefghijklmnop"
	seen := map[string]bool{}
	for frame := 0; frame < 200; frame++ {
		got := fitMarquee(s, 8, frame)
		if strings.TrimSpace(got) == "" {
			t.Fatalf("frame %d rendered blank", frame)
		}
		seen[got] = true
	}
	if fitMarquee(s, 8, 0) != fitMarquee(s, 8, len([]rune(s+marqueeGap))+marqueeHold) {
		t.Error("the cycle does not return to its start")
	}
}
