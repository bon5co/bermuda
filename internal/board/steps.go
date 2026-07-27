package board

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/internal/store"
)

// Workflow runs on the runs list.
//
// A workflow is one run made of several steps, so it stays one row: the run
// list is a list of things that were launched, and a workflow was launched
// once. What the row says is where the sequence got to — `2/4 · verify` — since
// the note of whichever agent spoke last is about one step, not about the run.
// The steps themselves are a nested block under the row, opened with space,
// because "which step is it stuck on and for how long" is a question asked
// about one run at a time and not about the whole list at once.

// stepProgress is a workflow run in one cell: how many steps are done, of how
// many, and which one it is on.
func stepProgress(steps []store.RunStep) string {
	done := 0
	current := ""
	for _, s := range steps {
		if s.Outcome == store.StepDone {
			done++
			continue
		}
		if current == "" {
			current = s.StepID
		}
	}
	label := itoa(done) + "/" + itoa(len(steps))
	if current == "" {
		return label
	}
	return label + " · " + current
}

// renderStepLines is the block that appears under an expanded workflow run: one
// line per step, saying what happened and how long it took.
func (m *Model) renderStepLines(steps []store.RunStep) string {
	var b strings.Builder
	for _, s := range steps {
		line := "      " + stepMark(s.Outcome) + " " +
			pad(truncate(s.StepID, 16), 16) + " " +
			pad(truncate(s.Kind, 5), 5) + " " +
			pad(stepOutcomeLabel(s), 16) + " " +
			pad(stepDuration(s), 8) + " " + s.Note
		// The block is detail under a row, so it is clipped to the same width
		// the table uses rather than being allowed to wrap the pane.
		b.WriteString(dimStyle.Render(truncate(line, m.contentWidth())) + "\n")
	}
	return b.String()
}

// stepMark is the one glyph that says how a step went, so a reader can find the
// step that stopped the workflow without reading the column.
func stepMark(outcome string) string {
	switch outcome {
	case store.StepDone:
		return "✓"
	case store.StepFailed:
		return "✗"
	case store.StepParked:
		return "⏸"
	case store.StepRunning:
		return "▶"
	default:
		return "·"
	}
}

// stepOutcomeLabel says why a step parked as well as that it did: "parked" on
// its own leaves the reader to open the run to learn whether the agent was
// waiting or simply never wrote a result.
func stepOutcomeLabel(s store.RunStep) string {
	if s.ParkReason != "" {
		return s.Outcome + " (" + s.ParkReason + ")"
	}
	return s.Outcome
}

func stepDuration(s store.RunStep) string {
	if s.EndedAt != nil {
		// A finished step always shows a duration, even 0s. Blank is what a
		// step that has not run looks like, and a step that took under a second
		// is not that.
		return s.Duration().Round(time.Second).String()
	}
	if s.Outcome == store.StepRunning && !s.StartedAt.IsZero() {
		// A running step is timed from its start, so a step that has been going
		// for an hour looks like one.
		return time.Since(s.StartedAt).Round(time.Second).String()
	}
	return ""
}

// toggleSteps opens or closes the nested step block of the selected run.
func (m *Model) toggleSteps() tea.Cmd {
	r, ok := m.selectedRun()
	if !ok || len(m.steps[r.ID]) == 0 {
		// Nothing to expand: an ordinary run is one agent and has no steps.
		return nil
	}
	if m.expanded == nil {
		m.expanded = map[string]bool{}
	}
	m.expanded[r.ID] = !m.expanded[r.ID]
	return nil
}
