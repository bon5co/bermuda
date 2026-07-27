package board

import (
	"strings"
	"testing"
)

// wrap has a line budget because it shares a pane with the job table: text
// that runs past it does not scroll, it pushes jobs off the board.
//
// The budget was never enforced — the `break` meant to stop the loop broke the
// switch it sat in — and the two guards after the loop were dead as a result,
// so a long note both overran its pane and silently lost its tail.
func TestWrapRespectsMaxLines(t *testing.T) {
	long := "aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj kkkk llll"

	got := wrap(long, 10, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("wrap returned %d lines, want 3:\n%s", len(lines), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated text must end in an ellipsis, got %q", lines[len(lines)-1])
	}
	for i, l := range lines {
		if w := len([]rune(l)); w > 10 {
			t.Errorf("line %d is %d wide, want <= 10: %q", i, w, l)
		}
	}
}

// Text that fits keeps every word, and gains no ellipsis for fitting.
func TestWrapKeepsTextThatFits(t *testing.T) {
	got := wrap("aaaa bbbb cccc", 10, 3)
	if got != "aaaa bbbb\ncccc" {
		t.Errorf("wrap = %q, want %q", got, "aaaa bbbb\ncccc")
	}
	if strings.Contains(got, "…") {
		t.Error("nothing was dropped, so nothing should say it was")
	}
}
