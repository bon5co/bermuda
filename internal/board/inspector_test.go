package board

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
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

// A job that starts a flow must be distinguishable from one that sends a
// prompt. They behave nothing alike, and before this the board drew them
// identically — the difference was visible in `bermuda job list` and nowhere
// on screen.
func TestAFlowJobIsIdentifiableOnTheJobsTab(t *testing.T) {
	m := newTestModel(t)
	m.jobs = append(m.jobs, store.Job{ID: "nightly", Name: "Nightly", Enabled: true,
		Model: "sonnet", Flow: "triage", Input: "anything filed since yesterday",
		Schedule: store.ScheduleManual})
	m.width, m.height = 200, 40
	m.focus = focusJobs
	m.cursor = len(m.jobs) - 1

	out := m.View()
	if !strings.Contains(out, "FLOW") {
		t.Fatalf("no FLOW column at 200 columns:\n%s", out)
	}
	if !strings.Contains(out, "triage") {
		t.Errorf("the flow a job starts is not shown:\n%s", out)
	}
	// The input is the argument nothing else on the screen reveals, and a
	// scheduled flow is called with a fixed one.
	if !strings.Contains(out, "anything filed since yesterday") {
		t.Errorf("the scheduled flow's input is not shown:\n%s", out)
	}
}

// A prompt-only job says so rather than leaving a blank, which in a table reads
// as missing data instead of as an answer.
func TestAPromptJobShowsADashInTheFlowColumn(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 200, 40
	m.focus = focusJobs
	if !strings.Contains(m.View(), "—") {
		t.Errorf("a prompt-only job leaves the FLOW cell blank:\n%s", m.View())
	}
}

// The optional columns are filled by name, so NEXT's value cannot land under
// FLOW at the one terminal width where both appear.
func TestOptionalJobColumnsLineUpWithTheirHeadings(t *testing.T) {
	m := newTestModel(t)
	m.jobs = []store.Job{{ID: "nightly", Name: "Nightly", Enabled: true,
		Model: "sonnet", Flow: "triage", Schedule: store.ScheduleManual}}
	m.width, m.height = 220, 40
	m.focus = focusJobs

	lines := strings.Split(m.View(), "\n")
	var header, row string
	for i, l := range lines {
		if strings.Contains(l, "FLOW") && strings.Contains(l, "SCHEDULE") {
			header, row = l, lines[i+1]
			break
		}
	}
	if header == "" {
		t.Fatalf("no header with both optional columns:\n%s", m.View())
	}
	if strings.Index(row, "triage") < strings.Index(header, "FLOW") {
		t.Errorf("the flow id starts before the FLOW heading:\nheader %q\nrow    %q", header, row)
	}
}
