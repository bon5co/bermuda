package board

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The bug this guards: a schedule like "once 2026-07-26 09:00" is longer than
// its column and used to run into the next one.
func TestLongCellDoesNotOverflowItsColumn(t *testing.T) {
	widths := []int{10, 8, 6}
	got := row([]string{"a-very-long-job-name", "once 2026-07-26 09:00", "x"}, widths)

	// Each cell is fitted independently, so both over-long values are cut and
	// the row is exactly as wide as the columns plus their separators.
	if n := strings.Count(got, "…"); n != 2 {
		t.Errorf("want both long cells truncated, found %d ellipses in %q", n, got)
	}
	// Trailing padding is trimmed, so the row ends after the last value: the
	// first two columns still occupy exactly their widths.
	if w := lipgloss.Width(got); w != 10+1+8+1+1 {
		t.Errorf("row is %d display columns, want 21: %q", w, got)
	}
	// The neighbouring value must not have been pushed sideways.
	if !strings.Contains(got, " once ") {
		t.Errorf("second column did not start at its own boundary: %q", got)
	}
}

// Width is measured in display columns, not bytes: a multi-byte glyph counts
// once, or every column after it shifts.
func TestWidthIsDisplayColumnsNotBytes(t *testing.T) {
	s := "▸ ▲ ◉" // 5 display columns, many more bytes
	if got := lipgloss.Width(pad(s, 10)); got != 10 {
		t.Errorf("pad produced %d display columns, want 10", got)
	}
	if got := lipgloss.Width(truncate("日本語のジョブ", 6)); got > 6 {
		t.Errorf("truncate produced %d display columns, want at most 6", got)
	}
}

func TestFlexColumnAbsorbsSpareWidth(t *testing.T) {
	cols := []column{
		{title: "JOB", flex: true, width: 10},
		{title: "SCHEDULE", width: 21},
		{title: "LAST", width: 9},
	}
	widths := layout(cols, 80)
	total := widths[0] + widths[1] + widths[2] + 2
	if total != 80 {
		t.Errorf("columns total %d, want the full 80", total)
	}
	if widths[0] <= 10 {
		t.Errorf("flex column did not grow: %d", widths[0])
	}
}

// A narrow pane must still produce sane rows rather than negative widths.
func TestNarrowPaneStaysSane(t *testing.T) {
	cols := []column{
		{title: "JOB", flex: true, width: 22},
		{title: "SCHEDULE", width: 21},
		{title: "LAST", width: 9},
		{title: "WHEN", width: 9},
	}
	widths := layout(cols, 30)
	for i, w := range widths {
		if w < 1 {
			t.Errorf("column %d has width %d", i, w)
		}
	}
	got := row([]string{"job", "0 7 * * *", "done", "2m ago"}, widths)
	if strings.Contains(got, "\n") {
		t.Error("a row must never wrap")
	}
}

// A wide pane should spend its spare width on more information, and a narrow
// one should not pretend to have room it lacks.
func TestOptionalColumnsAppearOnlyWhenTheyFit(t *testing.T) {
	wide := &Model{width: 200}
	narrow := &Model{width: 100}

	if len(wide.jobColumns()) <= len(narrow.jobColumns()) {
		t.Errorf("wide pane got %d columns, narrow got %d: extra width bought nothing",
			len(wide.jobColumns()), len(narrow.jobColumns()))
	}
	titles := func(m *Model) string {
		var b strings.Builder
		for _, c := range m.jobColumns() {
			b.WriteString(c.title + " ")
		}
		return b.String()
	}
	if !strings.Contains(titles(wide), "NEXT") {
		t.Error("wide pane should show the NEXT column")
	}
	if strings.Contains(titles(narrow), "NEXT") {
		t.Error("narrow pane must not add a column it cannot fit")
	}
}

// An extra column must never cost the inspector its place.
func TestInspectorSurvivesOptionalColumns(t *testing.T) {
	for w := 100; w <= 260; w += 7 {
		m := &Model{width: w}
		if iw := m.inspectorWidth(); iw > 0 && iw < inspectorMin {
			t.Fatalf("width %d gave the inspector %d columns, below the minimum %d",
				w, iw, inspectorMin)
		}
		// The table plus gap plus inspector must fit the content width.
		if iw := m.inspectorWidth(); iw > 0 {
			if total := m.tableWidth() + inspectorGap + iw; total > m.contentWidth() {
				t.Fatalf("width %d overflows: table %d + gap + inspector %d = %d > %d",
					w, m.tableWidth(), iw, total, m.contentWidth())
			}
		}
	}
}
