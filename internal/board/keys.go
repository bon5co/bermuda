package board

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Key handling. One switch per view, dispatched deepest-first from handleKey:
// the editor owns the keyboard while open, then a run detail, then a job detail,
// then the list. Horizontal keys mean depth and Tab means lists.

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Deepest view first: the editor owns the keyboard while it is open.
	if m.editor != nil {
		return m.handleEditorKey(msg)
	}
	if m.runDetail != nil {
		return m.handleRunDetailKey(msg)
	}
	if m.detail != nil {
		return m.handleDetailKey(msg)
	}
	// The flow input box owns the keyboard while it is open: `q` is a letter in
	// what a flow is being called with before it is a command.
	if m.flowInput != nil {
		return m.handleFlowInputKey(msg)
	}
	// While typing a search, keys belong to the query.
	if m.searching {
		return m.handleSearchKey(msg)
	}
	// The thread is a conversation, not a list of jobs, so it owns its own keys:
	// the switch below acts on a selected job, and the thread has no selection for
	// it to act on.
	if m.focus == focusThread {
		return m.handleThreadKey(msg)
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "/":
		m.searching, m.queryDraft = true, m.query
		return m, nil
	case "esc":
		// Clear an active search; otherwise nothing to leave.
		if m.query != "" {
			m.query, m.cursor, m.scroll = "", 0, 0
		}
		return m, nil
	case "]", "ctrl+f":
		m.movePage(1)
		return m, nil
	case "[", "ctrl+b":
		m.movePage(-1)
		return m, nil
	case " ", "space":
		// Open the selected flow run's steps in place. A run that is one
		// agent has no steps, and space does nothing.
		return m, m.toggleSteps()
	case "e":
		return m, m.openEditor()
	case "n":
		return m, m.openNewJob()
	case "D":
		return m, m.deleteSelected()
	// Tab cycles the lists and nothing else. Horizontal keys are reserved for
	// depth: right descends, left ascends.
	case "tab":
		m.stepTab(1)
		return m, nil
	case "shift+tab":
		// Backwards, so a reader who overshoots the tab they wanted does not
		// have to walk all the way round to come back to it.
		m.stepTab(-1)
		return m, nil
	// The number keys duplicate Tab because `herdr pane send-keys tab` is not
	// delivered as a Tab press, and the board is meant to be drivable
	// remotely, not only by hand. They count the tabs as drawn, so 1 is the
	// leftmost one.
	case "1", "2", "3", "4":
		m.selectTab(tabOrder[int(msg.String()[0]-'1')])
		return m, nil
	case "l", "right":
		return m, m.descend()
	case "h", "left":
		// Already at the top level; nothing to ascend to.
		return m, nil
	case "j", "down":
		m.cursor++
		m.clampCursor()
		return m, nil
	case "k", "up":
		m.cursor--
		m.clampCursor()
		return m, nil
	case "ctrl+d", "pgdown":
		m.scrollBy(m.pageSize())
		return m, nil
	case "ctrl+u", "pgup":
		m.scrollBy(-m.pageSize())
		return m, nil
	case "r":
		return m, m.load()
	case "a":
		return m, m.focusRun()
	case "enter":
		// Enter on a flow calls it. It is the one list where the selected thing
		// is something to start rather than somewhere to go.
		if m.focus == focusFlows {
			return m, m.launchSelectedFlow()
		}
		return m, m.descend()
	case "u":
		// Unparking only means something to a flow: a run row has no flow
		// identity to resume against, and a job is a schedule rather than a
		// sequence that stopped halfway.
		if m.focus == focusFlows {
			return m, m.unparkSelectedFlow()
		}
		return m, nil
	case "R":
		return m, m.runSelected()
	case "p":
		return m, m.togglePause()
	case "f":
		return m, m.toggleFavorite()
	case "F":
		// Finished one-shots into view and back out again. The cursor is reset
		// because the rows under it change.
		m.showFinished = !m.showFinished
		m.cursor, m.scroll = 0, 0
		return m, nil
	}
	return m, nil
}

// descend opens whatever the cursor is sitting on: a job opens its detail, a
// run goes to the agent that is running it.

// descend opens whatever the cursor is sitting on: a job opens its detail, a
// run goes to the agent that is running it.
func (m *Model) descend() tea.Cmd {
	switch m.focus {
	case focusJobs:
		return m.openDetail()
	case focusFlows:
		// Nothing lives under a flow: it is a file, and its runs are the RUNS
		// tab. `l` deliberately does not launch it either — a horizontal key
		// that started agents would spend money on a mistyped navigation.
		return nil
	}
	return m.openRunDetail()
}

// handleSearchKey edits the live query.
//
// Filtering happens on every keystroke rather than on submit, so the list
// narrows as it is typed and the reader can stop as soon as they see what they
// want.

// handleSearchKey edits the live query.
//
// Filtering happens on every keystroke rather than on submit, so the list
// narrows as it is typed and the reader can stop as soon as they see what they
// want.
func (m *Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Abandon the edit and restore whatever filter was active before.
		m.searching = false
		m.cursor, m.scroll = 0, 0
		return m, nil
	case "enter":
		m.searching = false
		return m, nil
	case "ctrl+u":
		m.queryDraft = ""
	case "backspace":
		r := []rune(m.queryDraft)
		if len(r) == 0 {
			// Erasing past the start dismisses the search rather than leaving
			// an empty prompt sitting there with nothing to erase.
			m.searching, m.query, m.queryDraft = false, "", ""
			m.cursor, m.scroll = 0, 0
			return m, nil
		}
		m.queryDraft = string(r[:len(r)-1])
	default:
		if r := msg.Runes; len(r) > 0 {
			m.queryDraft += string(r)
		}
	}
	m.query = m.queryDraft
	m.cursor, m.scroll = 0, 0
	return m, nil
}

// handleDetailKey drives the job detail view.

// handleDetailKey drives the job detail view.
func (m *Model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc", "backspace", "h", "left":
		m.detail, m.detailRuns, m.cursor = nil, nil, 0
		return m, m.load()
	case "l", "right":
		// One level deeper from a job is the agent running the selected run.
		return m, m.focusRun()
	case "e":
		return m, m.openEditor()
	case "D":
		return m, m.deleteSelected()
	case "ctrl+d", "pgdown":
		m.scrollBy(m.pageSize())
		return m, nil
	case "ctrl+u", "pgup":
		m.scrollBy(-m.pageSize())
		return m, nil
	case "j", "down":
		m.cursor++
		m.clampCursor()
		return m, nil
	case "k", "up":
		m.cursor--
		m.clampCursor()
		return m, nil
	case "enter", "a":
		return m, m.focusRun()
	case "R":
		return m, m.runSelected()
	case "p":
		return m, m.togglePause()
	case "r":
		return m, m.load()
	}
	return m, nil
}

// openDetail loads the selected job's full record.
