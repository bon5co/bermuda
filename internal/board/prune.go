package board

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The board's half of `bermuda job prune`: clearing the finished one-shots that
// the F rule is already hiding, without leaving the pane.
//
// It is the only board action that deletes more than one thing at a time, so
// unlike D it is two keystrokes and not one. The keystroke opens a box naming
// every job it would delete; nothing is written until y is pressed against that
// list. A single key that removed several jobs would eventually be pressed by
// accident, and there is no undo — deleting a job keeps its runs, but the
// schedule that produced them is gone.

// prunePrompt is the open "delete these?" box.
type prunePrompt struct {
	// jobs is exactly what will be deleted if the answer is yes, captured when
	// the box opened. It is not recomputed on confirm: the reader agreed to the
	// list they were shown, and a tick landing in between must not enlarge it.
	jobs []store.Job
}

// openPrune asks about every finished one-shot currently on the board.
//
// The set is taken from the whole job list rather than the visible page, since
// the point of the command is to clear what is hidden — but it respects an
// active search, so pruning while filtered removes what the filter describes
// and not what is off screen behind it.
func (m *Model) openPrune() tea.Cmd {
	var doomed []store.Job
	for _, j := range m.jobs {
		if matchesJob(j, m.query) && m.jobFinished(j) {
			doomed = append(doomed, j)
		}
	}
	if len(doomed) == 0 {
		m.status = "nothing to prune"
		return nil
	}
	m.prune = &prunePrompt{jobs: doomed}
	return nil
}

// handlePruneKey drives the box. Only y deletes; every other key cancels.
//
// The default is deliberately "no": a reader who opened this by accident, or
// who reached for j to scroll the list of names, gets out without writing
// anything.
func (m *Model) handlePruneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "y":
		p := m.prune
		m.prune = nil
		return m, m.confirmPrune(p.jobs)
	}
	m.prune = nil
	m.status = "prune cancelled"
	return m, nil
}

// confirmPrune deletes the jobs that were named in the box.
//
// Runs are left alone: deleting a job already keeps its history, so pruning
// loses the schedule and not the record of what happened.
func (m *Model) confirmPrune(jobs []store.Job) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n := 0
		for _, j := range jobs {
			if err := m.store.DeleteJob(ctx, j.ID); err != nil {
				// Report what did go, so a failure halfway through does not
				// read as though nothing happened.
				return jobsPrunedMsg{n: n, err: err}
			}
			n++
		}
		return jobsPrunedMsg{n: n}
	}
}

type jobsPrunedMsg struct {
	n   int
	err error
}

// renderPrune draws the box under the table it was opened from.
func (m *Model) renderPrune() string {
	p := m.prune
	var b strings.Builder
	b.WriteString("\n" + headerStyle.Render(
		"prune "+itoa(len(p.jobs))+" finished one-shot(s)") + "\n")
	for _, j := range p.jobs {
		name := j.ID
		if j.Name != "" {
			name += "  " + j.Name
		}
		b.WriteString("  " + dimStyle.Render(truncate(name, m.contentWidth()-2)) + "\n")
	}
	b.WriteString("  " + dimStyle.Render(
		"y delete · any other key cancels · runs are kept") + "\n")
	return b.String()
}
