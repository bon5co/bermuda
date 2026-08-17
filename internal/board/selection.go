package board

import (
	"github.com/bon5co/bermuda/v2/internal/store"
)

// Resolving the cursor to the thing it points at. Every one of these works on
// the *filtered* list, so a key can never act on a row the reader cannot see.

// selectedJob resolves the cursor to a job in either view.
func (m *Model) selectedJob() (store.Job, bool) {
	if m.detail != nil {
		return *m.detail, true
	}
	jobs := m.visibleJobs()
	if m.focus == focusJobs && m.cursor < len(jobs) {
		return jobs[m.cursor], true
	}
	// From the runs list, act on the job that run belongs to.
	runs := m.visibleRuns()
	if m.focus == focusRuns && m.cursor < len(runs) {
		for _, j := range m.jobs {
			if j.ID == runs[m.cursor].JobID {
				return j, true
			}
		}
	}
	return store.Job{}, false
}

// runSelected launches the selected job now.

// selectedRun resolves the cursor to a run, whichever view is showing.
func (m *Model) selectedRun() (store.Run, bool) {
	if m.detail != nil {
		if m.cursor < len(m.detailRuns) {
			return m.detailRuns[m.cursor], true
		}
		return store.Run{}, false
	}
	if m.focus == focusRuns {
		runs := m.visibleRuns()
		if m.cursor < len(runs) {
			return runs[m.cursor], true
		}
		return store.Run{}, false
	}
	// Only the jobs list resolves a run from a cursor. Anything else — the thread,
	// which has no selection — would otherwise fall through and return the last
	// run of whichever job happened to be first.
	if m.focus != focusJobs {
		return store.Run{}, false
	}
	jobs := m.visibleJobs()
	if m.cursor < len(jobs) {
		r, ok := m.last[jobs[m.cursor].ID]
		return r, ok
	}
	return store.Run{}, false
}

func (m *Model) clampCursor() {
	n := len(m.visibleJobs())
	switch {
	case m.detail != nil:
		n = len(m.detailRuns)
	case m.focus == focusRuns:
		n = len(m.visibleRuns())
	case m.focus == focusFlows:
		n = len(m.visibleFlows())
	case m.focus == focusThread:
		// The thread has no selectable rows; it is scrolled, not stepped
		// through.
		n = 0
	case m.focus == focusForum:
		// The forum tab shows a fixed line, nothing to select.
		n = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// View renders the board, windowed to the pane height.
