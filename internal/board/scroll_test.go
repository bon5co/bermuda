package board

import (
	"strconv"
	"strings"
	"testing"
)

func lines(n int, cursorAt int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = "line " + strconv.Itoa(i)
		if i == cursorAt {
			out[i] = cursorMark + " " + out[i]
		}
	}
	return strings.Join(out, "\n")
}

func TestShortContentIsNotWindowed(t *testing.T) {
	m := &Model{height: 20}
	content := lines(5, 0)
	if got := m.window(content); got != content {
		t.Errorf("short content was altered:\n%q", got)
	}
}

func TestLongContentIsTrimmedToHeight(t *testing.T) {
	m := &Model{height: 10}
	got := m.window(lines(100, 0))
	if n := strings.Count(got, "\n") + 1; n > 10 {
		t.Errorf("windowed output has %d lines, want at most 10", n)
	}
	if !strings.Contains(got, "more") {
		t.Error("expected a scroll indicator when content is truncated")
	}
}

// The window must follow the selection, or moving the cursor off-screen would
// leave the user steering something they cannot see.
func TestWindowFollowsCursor(t *testing.T) {
	m := &Model{height: 10}
	got := m.window(lines(100, 80))
	if !strings.Contains(got, "line 80") {
		t.Errorf("selected line not visible; got:\n%s", got)
	}

	// And back up again.
	got = m.window(lines(100, 2))
	if !strings.Contains(got, "line 2") {
		t.Errorf("selected line not visible after scrolling back; got:\n%s", got)
	}
}

func TestScrollIsClampedToContent(t *testing.T) {
	m := &Model{height: 10}
	m.scrollBy(1000)
	got := m.window(lines(30, -1))
	if !strings.Contains(got, "line 29") {
		t.Errorf("over-scroll should clamp to the last line; got:\n%s", got)
	}
	m.scrollBy(-1000)
	if m.scroll != 0 {
		t.Errorf("under-scroll should clamp to 0, got %d", m.scroll)
	}
}
