package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A prompt is user text in whatever script the user writes in, and `job list`
// cuts it to fit a column. Cutting by bytes split the last character in half
// and printed a replacement glyph; the board's own truncate had always done
// this by width, and this one call site had not.
func TestFirstLineCutsByRunesNotBytes(t *testing.T) {
	long := strings.Repeat("日", 80)
	got := firstLine(long)

	if !utf8.ValidString(got) {
		t.Fatalf("firstLine produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 60 {
		t.Errorf("kept %d runes, want 60 (59 plus the ellipsis)", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut prompt should say it was cut: %q", got)
	}
}

// Short prompts are left exactly as they are.
func TestFirstLineLeavesShortPromptsAlone(t *testing.T) {
	if got := firstLine("  summarize the day  "); got != "summarize the day" {
		t.Errorf("firstLine = %q, want the trimmed prompt", got)
	}
}

// A multi-line prompt is announced as one, rather than silently showing only
// its opening line.
func TestFirstLineMarksATruncatedFirstLine(t *testing.T) {
	got := firstLine("do the thing\nthen the other thing")
	if got != "do the thing …" {
		t.Errorf("firstLine = %q, want the first line marked as partial", got)
	}
}
