package board

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/internal/flow"
)

// The FLOWS tab: what there is to call, and what happened last time it was
// called.
//
// A flow reached the board only as whatever its last run happened to be — one
// row in RUNS, named after a job that may not exist. So the board could show a
// parked flow and do nothing about it: resuming meant leaving the board and
// typing a run id nobody has memorised. This tab is the one place a flow is a
// flow, which is what makes `enter` and `u` possible at all.
//
// It reads files rather than rows. That is the same decision the flow package
// made and it costs a directory listing per tick, which is the price of a flow
// somebody just wrote appearing without the board being reopened.

// flowRow is one line of the tab: a flow that parsed, or the file that did not.
//
// A broken flow is a row rather than a footnote because it is invisible
// everywhere else — it simply does not appear, which reads as "I never wrote
// that one" — and it is the flow most in need of being opened.
type flowRow struct {
	flow flow.Flow
	// err is why the file would not parse, set only on a broken row.
	err error
}

// readFlows lists the flows on disk.
//
// Called from the load command's goroutine, alongside the store reads, because
// it touches the filesystem on every tick.
func (m *Model) readFlows() ([]flow.Flow, []error) {
	dir := strings.TrimSpace(m.deps.FlowDir)
	if dir == "" {
		// Reading "" would be reported by the filesystem as a directory that
		// does not exist, which flow.List treats as an installation with no
		// flows — so a board wired without a flow directory would say "no flows
		// yet" to somebody who has ten.
		return nil, []error{errors.New("this board was given no flow directory to read")}
	}
	return flow.List(dir)
}

// flowRows is every flow the tab has to show, broken files first.
//
// They lead for the same reason `flow list` names them before it lists
// anything: a flow that will not parse is the one thing on this tab that is
// currently wrong, and a reader who has to page to find it will not.
func (m *Model) flowRows() []flowRow {
	rows := make([]flowRow, 0, len(m.flowErrs)+len(m.flows))
	for _, err := range m.flowErrs {
		rows = append(rows, flowRow{err: err})
	}
	for _, f := range m.flows {
		rows = append(rows, flowRow{flow: f})
	}
	return rows
}

// visibleFlows is the tab after the active search.
func (m *Model) visibleFlows() []flowRow {
	rows := m.flowRows()
	if m.query == "" {
		return rows
	}
	var out []flowRow
	for _, r := range rows {
		if matchesFlow(r, m.query) {
			out = append(out, r)
		}
	}
	return out
}

// matchesFlow reports whether a row matches the query.
//
// A broken row is matched on its error, which is the only text it has — and
// which contains the path, so searching for a filename still finds the file
// that will not load.
func matchesFlow(r flowRow, query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	haystack := []string{strings.ToLower(r.flow.ID),
		strings.ToLower(r.flow.About), strings.ToLower(r.flow.Input)}
	if r.err != nil {
		haystack = []string{strings.ToLower(r.err.Error())}
	}
	for _, h := range haystack {
		if strings.Contains(h, q) {
			return true
		}
	}
	return false
}

// selectedFlow resolves the cursor to a row of this tab, and only of this tab:
// every other list's keys act on a job or a run, and answering them from here
// would act on a row the reader is not looking at.
func (m *Model) selectedFlow() (flowRow, bool) {
	if m.focus != focusFlows {
		return flowRow{}, false
	}
	rows := m.visibleFlows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return flowRow{}, false
	}
	return rows[m.cursor], true
}

// flowColumns is the tab's shape: what it is, what it costs, what it needs, and
// how it went.
func (m *Model) flowColumns() []column {
	return []column{
		{title: "FLOW", width: 18},
		{title: "STEPS", width: 5},
		{title: "INPUT", flex: true, width: 24, max: 46},
		{title: "LAST RUN", width: 9},
		{title: "STATE", width: 8},
	}
}

// blankCell is what a column with nothing true to say shows. An empty cell is
// indistinguishable from a column that failed to render.
const blankCell = "—"

func (m *Model) renderFlows(start, end int) string {
	var b strings.Builder
	rows := m.visibleFlows()
	if len(rows) == 0 {
		if m.query != "" {
			b.WriteString(dimStyle.Render("  no flows match "+m.query) + "\n")
		} else {
			b.WriteString(dimStyle.Render("  no flows yet — `bermuda flow new <id>` writes one") + "\n")
		}
		return b.String()
	}
	cols := m.flowColumns()
	widths := layout(cols, m.contentWidth())
	b.WriteString("  " + dimStyle.Render(row(titles(cols), widths)) + "\n")

	for i := start; i < end; i++ {
		fr := rows[i]
		cursor := "  "
		selected := m.focus == focusFlows && i == m.cursor
		if selected {
			cursor = selectedStyle.Render(cursorMark + " ")
		}
		if fr.err != nil {
			// A broken file has no columns to fill: it did not parse, so there
			// are no steps to count and no input to declare. What it has is the
			// path and the reason, which is what fixing it takes.
			b.WriteString(cursor + outcomeStyles["failed"].Render(
				truncate(flowErrorLine(fr.err), m.contentWidth())) + "\n")
			continue
		}

		input := fr.flow.Input
		if input == "" {
			input = blankCell
		}
		// "never" rather than a formatted zero time, which renders as 1970 and
		// reads as a bug in the store rather than as a flow nobody has called.
		when, state := "never", blankCell
		if r, ok := m.lastFlow[fr.flow.ID]; ok {
			when, state = ago(r.StartedAt), r.Outcome
		}
		cells := []string{fr.flow.ID, itoa(len(fr.flow.Steps)), input, when, state}

		// Fit first, then style: escape sequences must not be measured as
		// width, so colour is applied to the already-sized cell.
		sized := make([]string, len(cells))
		for k, c := range cells {
			sized[k] = fitMarquee(c, widths[k], m.frame)
		}
		if selected {
			// One bright style across the whole row, so it reads as a single
			// selected line rather than a row of differently coloured cells.
			for k := range sized {
				sized[k] = rowSelected.Render(sized[k])
			}
		} else {
			sized[1] = dimStyle.Render(sized[1])
			sized[2] = dimStyle.Render(sized[2])
			sized[3] = dimStyle.Render(sized[3])
			sized[4] = styleOutcome(sized[4])
		}
		b.WriteString(cursor + strings.Join(sized, " ") + "\n")
	}
	return b.String()
}

// flowErrorLine puts a parse error onto exactly one line.
//
// A YAML error arrives as a heading with the offending line under it, and a
// table row that is secretly two rows breaks the arithmetic the pane is sized
// by: the view renders one line more than it budgeted for, the terminal
// scrolls, and the whole board smears upwards.
func flowErrorLine(err error) string {
	return "! " + strings.Join(strings.Fields(err.Error()), " ")
}

// launchSelectedFlow is what enter does on this tab.
func (m *Model) launchSelectedFlow() tea.Cmd {
	fr, ok := m.selectedFlow()
	if !ok {
		return nil
	}
	if fr.err != nil {
		// There is nothing to run: the file did not parse, so it has no steps.
		// Said out loud, because a key that silently does nothing reads as a
		// broken board rather than as a broken flow.
		return func() tea.Msg { return actionMsg{err: fr.err} }
	}
	if fr.flow.TakesInput() {
		// Asked for before the run exists, not after. Every prompt saying
		// {{input}} would otherwise get a hole where its subject should be, and
		// an agent handed that will invent something to fill it.
		m.openFlowInput(fr.flow)
		return nil
	}
	return m.startFlow(fr.flow.ID, "")
}

// startFlow launches one, off the event loop.
//
// A flow is several agent turns in series and runs for minutes or hours.
// Called inline it would hold Update for all of it: the board would stop
// redrawing, stop ticking and stop answering keys, looking hung rather than
// busy. So it goes out as a command, the same way a job run does.
func (m *Model) startFlow(id, input string) tea.Cmd {
	if m.deps.RunFlow == nil {
		return func() tea.Msg {
			return actionMsg{status: "this board has no way to start a flow"}
		}
	}
	key := flowRunKey(id)
	if m.running[key] {
		return func() tea.Msg { return actionMsg{status: "flow " + id + " is already running"} }
	}
	m.running[key] = true
	run := m.deps.RunFlow
	return func() tea.Msg { return flowDoneMsg{flowID: id, err: run(id, input)} }
}

// unparkSelectedFlow resumes the selected flow's latest run.
func (m *Model) unparkSelectedFlow() tea.Cmd {
	fr, ok := m.selectedFlow()
	if !ok || fr.err != nil {
		return nil
	}
	rec, ok := m.lastFlow[fr.flow.ID]
	if !ok {
		return func() tea.Msg {
			return actionMsg{status: "flow " + fr.flow.ID + " has never run, so there is nothing to resume"}
		}
	}
	if rec.Outcome != outcomeParked {
		// Only a parked run is resumable. Reaching this key on a finished one
		// would re-run steps that already cost money, under a keystroke that
		// promised to pick one up where it stopped.
		return func() tea.Msg {
			return actionMsg{status: "flow " + fr.flow.ID + "'s last run is " +
				rec.Outcome + ", not parked"}
		}
	}
	if m.deps.ResumeFlow == nil {
		return func() tea.Msg {
			return actionMsg{status: "this board has no way to resume a flow"}
		}
	}
	key := flowRunKey(fr.flow.ID)
	if m.running[key] {
		return func() tea.Msg {
			return actionMsg{status: "flow " + fr.flow.ID + " is already running"}
		}
	}
	m.running[key] = true
	resume, id, runID := m.deps.ResumeFlow, fr.flow.ID, rec.ID
	return func() tea.Msg { return flowDoneMsg{flowID: id, err: resume(runID)} }
}

// outcomeParked is the one run outcome this tab acts on.
const outcomeParked = "parked"

// flowRunKey namespaces a flow inside the running set, which is otherwise keyed
// by job id. A job that starts a flow carries the flow's id, and without the
// prefix a running job would refuse to let its own flow be called by hand — a
// refusal with nothing on screen to explain it.
func flowRunKey(id string) string { return "flow:" + id }

// flowDoneMsg reports a launch or a resume back to the event loop.
type flowDoneMsg struct {
	flowID string
	err    error
}

// flowPrompt is the open "what is this flow called with" box.
type flowPrompt struct {
	flowID string
	// about is the flow's own sentence about what belongs here. It is shown
	// above the box because the flow file is the only thing that knows, and
	// "input" on its own tells the reader nothing about what to type.
	about string
	input textinput.Model
	// err is the last refusal, kept on screen rather than swallowed.
	err string
}

// openFlowInput asks for the input before the run starts.
func (m *Model) openFlowInput(f flow.Flow) {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 0
	in.Placeholder = f.Input
	in.Focus()
	m.flowInput = &flowPrompt{flowID: f.ID, about: f.Input, input: in}
}

// handleFlowInputKey drives the box.
//
// Enter commits, where the thread's box needs ctrl+s. A message is prose and
// runs to several lines; a flow's input is one value, and a reader who has
// finished the line has finished.
func (m *Model) handleFlowInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.flowInput = nil
		return m, nil
	case "enter":
		return m, m.submitFlowInput()
	}
	var cmd tea.Cmd
	m.flowInput.input, cmd = m.flowInput.input.Update(msg)
	return m, cmd
}

// submitFlowInput starts the flow with what was typed, or refuses to.
func (m *Model) submitFlowInput() tea.Cmd {
	p := m.flowInput
	input := strings.TrimSpace(p.input.Value())
	if input == "" {
		// Refused rather than started blank. Closing the box on an empty value
		// would look identical to launching, and the flow would run with a hole
		// where its subject should be — which is worse than not running.
		p.err = "flow " + p.flowID + " needs an input: " + p.about
		return nil
	}
	m.flowInput = nil
	return m.startFlow(p.flowID, input)
}

// flowInputWidth is how wide the box may be drawn: inside the pane, and no
// wider than a line of prose is worth reading back.
func (m *Model) flowInputWidth() int {
	return min(m.contentWidth()-4, 60)
}

// renderFlowInput draws the box under the table it was opened from.
func (m *Model) renderFlowInput() string {
	p := m.flowInput
	p.input.Width = m.flowInputWidth()
	var b strings.Builder
	b.WriteString("\n" + headerStyle.Render("run flow "+p.flowID) + "\n")
	b.WriteString("  " + dimStyle.Render(truncate(p.about, m.contentWidth()-2)) + "\n")
	b.WriteString("  " + p.input.View() + "\n")
	if p.err != "" {
		b.WriteString("  " + outcomeStyles["failed"].Render(
			truncate(p.err, m.contentWidth()-2)) + "\n")
	}
	b.WriteString("  " + dimStyle.Render("enter run · esc cancel") + "\n")
	return b.String()
}
