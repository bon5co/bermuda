package board

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v2/internal/store"
	"github.com/bon5co/bermuda/v2/internal/usage"
)

// openRunDetail inspects the selected run.
func (m *Model) openRunDetail() tea.Cmd {
	r, ok := m.selectedRun()
	if !ok {
		return nil
	}
	run := r
	return func() tea.Msg { return runDetailMsg{run: &run} }
}

type runDetailMsg struct{ run *store.Run }

// handleRunDetailKey drives the run detail view.
func (m *Model) handleRunDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "backspace", "h", "left":
		m.runDetail = nil
		return m, m.load()
	case "enter", "l", "right":
		// Deeper from a run is the job that produced it: the usual reason to
		// study a run is to change the thing that scheduled it.
		return m, m.openJobOfRun()
	case "a":
		return m, m.focusRun()
	case "r":
		return m, m.load()
	}
	return m, nil
}

// openJobOfRun jumps from a run to the job that produced it.
func (m *Model) openJobOfRun() tea.Cmd {
	if m.runDetail == nil {
		return nil
	}
	id := m.runDetail.JobID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		j, err := m.store.Job(ctx, id)
		if err != nil {
			// A run outlives its job: deleting a job keeps its history, so this
			// is an ordinary outcome rather than a failure.
			return actionMsg{status: "job " + id + " no longer exists"}
		}
		runs, err := m.store.JobRuns(ctx, id, 20)
		if err != nil {
			return actionMsg{err: err}
		}
		return jobFromRunMsg{job: j, runs: runs}
	}
}

type jobFromRunMsg struct {
	job  *store.Job
	runs []store.Run
}

// renderRunDetail shows one run: what it did, how it ended, and where its
// artifacts are.
func (m *Model) renderRunDetail() string {
	r := m.runDetail
	var b strings.Builder

	b.WriteString(titleStyle.Render(r.JobID) + "  " +
		styleOutcome(r.Outcome) + "\n")
	b.WriteString(dimStyle.Render(r.ID) + "\n\n")

	row := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString(headerStyle.Render(pad(k, 14)) + " " + v + "\n")
	}
	row("outcome", styleOutcome(r.Outcome))
	if r.ParkReason != "" {
		row("waiting on", outcomeStyles["parked"].Render(r.ParkReason))
	}
	row("trigger", r.Trigger)
	row("agent state", r.Status)
	row("started", r.StartedAt.Format("2006-01-02 15:04:05"))
	if r.EndedAt != nil {
		row("ended", r.EndedAt.Format("2006-01-02 15:04:05"))
		row("duration", r.Duration().Round(time.Second).String())
	} else {
		row("duration", "still running")
	}
	row("agent", r.AgentName)
	row("herdr tab", r.TabID)
	// A flow run's steps compared notes somewhere, and the notes below are only
	// the one line each step published. Without this the thread is invisible: the
	// board would show a finished flow and no sign that the reasoning behind it is
	// still readable.
	row("thread", r.Thread)
	// One line, counts abbreviated, so a long number cannot push the column
	// layout apart.
	row("tokens", usage.Line(usage.Usage{
		InputTokens:         r.InputTokens,
		OutputTokens:        r.OutputTokens,
		CacheReadTokens:     r.CacheReadTokens,
		CacheCreationTokens: r.CacheCreationTokens,
	}))
	row("model", r.Model)
	if r.Note != "" {
		b.WriteString("\n" + headerStyle.Render("RESULT") + "\n  " + r.Note + "\n")
	}
	if r.RunDir != "" {
		b.WriteString("\n" + headerStyle.Render("ARTIFACTS") + "\n")
		for _, f := range []string{"prompt.md", "transcript.txt", "result.json"} {
			b.WriteString("  " + dimStyle.Render(filepath.Join(r.RunDir, f)) + "\n")
		}
	}

	if m.err != nil {
		b.WriteString("\n" + outcomeStyles["failed"].Render("error: "+m.err.Error()) + "\n")
	} else if m.status != "" {
		b.WriteString("\n" + dimStyle.Render(m.status) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render(
		"h/← back · l/→ open job · a attach agent · q quit"))
	return b.String()
}
