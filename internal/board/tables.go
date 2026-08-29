package board

import (
	"fmt"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The two tables. Columns are declared as data so the tab rule can measure the
// table it sits above, and so a wide pane can earn extra columns.

func (m *Model) renderJobs(start, end int) string {
	var b strings.Builder
	jobs := m.visibleJobs()
	if len(jobs) == 0 {
		if m.query != "" {
			b.WriteString(dimStyle.Render("  no jobs match "+m.query) + "\n")
		} else {
			b.WriteString(dimStyle.Render("  no jobs yet — press n to add one") + "\n")
		}
		return b.String()
	}
	cols := m.jobColumns()
	widths := layout(cols, m.tableWidth())

	b.WriteString("  " + dimStyle.Render(row(titles(cols), widths)) + "\n")
	// line is where in this block the next row lands, counted from the header
	// it starts under, so a click can be turned back into a row. See mouse.go.
	line := 1
	for i := start; i < end; i++ {
		j := jobs[i]
		m.mark(line, hitJob, i)
		line++
		cursor := "  "
		if m.focus == focusJobs && i == m.cursor {
			cursor = selectedStyle.Render(cursorMark + " ")
		}
		// Show the name, falling back to the id. The board is navigated by
		// selection rather than by typing, so the readable label is more use
		// here than the handle; the id is on the detail page.
		name := j.Name
		if strings.TrimSpace(name) == "" {
			name = j.ID
		}
		// A finished one-shot is only on screen because it was asked for, so it
		// says so: paused and finished are both disabled jobs, and only one of
		// them can ever run again.
		switch {
		case m.jobFinished(j):
			name += " (finished)"
		case !j.Enabled:
			name += " (paused)"
		}
		star := ""
		if j.Favorite {
			star = "★"
		}
		outcome, when := "never", ""
		if r, ok := m.last[j.ID]; ok {
			outcome, when = r.Outcome, ago(r.StartedAt)
		}
		cells := []string{star, name, j.Model,
			strings.Join(j.Tags, ","), j.ScheduleLabel(), outcome, when}
		// Optional columns are filled by title rather than by position. There is
		// more than one of them now, and appending blind would put NEXT's value
		// under whichever optional column happened to come first — a table that
		// is wrong only at certain terminal widths.
		for _, c := range cols[len(cells):] {
			switch c.title {
			case "NEXT":
				cells = append(cells, m.nextFirePlain(j))
			case "FLOW":
				cells = append(cells, jobFlowCell(j))
			default:
				cells = append(cells, "")
			}
		}

		// Fit first, then style: escape sequences must not be measured as
		// width, so colour is applied to the already-sized cell.
		sized := make([]string, len(cells))
		for k, c := range cells {
			sized[k] = fitMarquee(c, widths[k], m.frame)
		}
		selected := m.focus == focusJobs && i == m.cursor
		sized[0] = favoriteStyle.Render(sized[0])
		switch {
		case selected:
			// One bright style across the whole row, so it reads as a single
			// selected line rather than a row of differently coloured cells.
			for k := 1; k < len(sized); k++ {
				sized[k] = rowSelected.Render(sized[k])
			}
		default:
			if !j.Enabled {
				sized[1] = dimStyle.Render(sized[1])
			}
			sized[2] = dimStyle.Render(sized[2])
			sized[3] = dimStyle.Render(sized[3])
			sized[4] = dimStyle.Render(sized[4])
			sized[5] = styleOutcome(sized[5])
			// Everything past the outcome is secondary detail, including any
			// optional column a wide pane added.
			for k := 6; k < len(sized); k++ {
				sized[k] = dimStyle.Render(sized[k])
			}
		}
		b.WriteString(cursor + strings.Join(sized, " ") + "\n")
	}
	return b.String()
}

// contentWidth is the width available to a table, leaving room for the cursor
// gutter and a margin so a full row cannot wrap.

// contentWidth is the width available to a table, leaving room for the cursor
// gutter and a margin so a full row cannot wrap.
func (m *Model) contentWidth() int {
	w := m.width
	if w <= 0 {
		w = 100
	}
	w -= 3
	if w < 24 {
		w = 24
	}
	return w
}

// jobColumns and runColumns are the two tables' shapes. They are methods so the
// tab rule can measure the table it sits above without duplicating them, and so
// a wide pane can earn extra columns rather than leaving the space empty.
//
// Spare width is spent on more information, not on padding: widening one column
// to swallow 60 spare columns just puts a stripe of blank across the table,
// which is what an earlier version did. So the optional columns below appear in
// order of usefulness as room allows.

// jobColumns and runColumns are the two tables' shapes. They are methods so the
// tab rule can measure the table it sits above without duplicating them, and so
// a wide pane can earn extra columns rather than leaving the space empty.
//
// Spare width is spent on more information, not on padding: widening one column
// to swallow 60 spare columns just puts a stripe of blank across the table,
// which is what an earlier version did. So the optional columns below appear in
// order of usefulness as room allows.
func (m *Model) jobColumns() []column {
	cols := []column{
		// The star lives in its own column so favorites line up with the jobs
		// that have none, instead of shifting every name right by two.
		{title: " ", width: 1},
		{title: "JOB", flex: true, width: 22, max: 34},
		{title: "MODEL", width: 8},
		{title: "TAGS", width: 16},
		{title: "SCHEDULE", width: 21}, // fits "once 2006-01-02 15:04"
		{title: "LAST", width: 9},
		{title: "WHEN", width: 9},
	}
	// FLOW buys the first spare column, ahead of NEXT, because it is the only
	// one of the two that cannot be worked out from what is already on screen.
	// SCHEDULE is right there, so NEXT refines something visible; nothing on
	// this row says whether the job sends one prompt or drives a four-step
	// sequence, and those behave nothing alike.
	if m.spareAfter(cols, colFlowWidth) {
		cols = append(cols, column{title: "FLOW", width: colFlowWidth})
	}
	// NEXT answers the question a cron expression does not. It only earns its
	// place if the inspector still fits afterwards.
	if m.spareAfter(cols, colNextWidth) {
		cols = append(cols, column{title: "NEXT", width: colNextWidth})
	}
	return cols
}

// jobFlowCell names the flow a job starts, or says it sends a prompt.
//
// A dash rather than a blank, because a blank cell in a table reads as missing
// data and "this one is a plain prompt" is the answer, not the absence of one.
func jobFlowCell(j store.Job) string {
	if !j.IsFlow() {
		return "—"
	}
	return j.Flow
}

func (m *Model) runColumns() []column {
	cols := []column{
		{title: "JOB", width: 20},
		{title: "OUTCOME", width: 8},
		{title: "WHEN", width: 9},
		{title: "DETAIL", flex: true, width: 30, max: 70},
	}
	return cols
}

// spareAfter reports whether adding a column of width w would still leave room
// for the inspector. Without this check an extra column would silently push the
// inspector off the pane, trading a panel for a column.

// spareAfter reports whether adding a column of width w would still leave room
// for the inspector. Without this check an extra column would silently push the
// inspector off the pane, trading a panel for a column.
func (m *Model) spareAfter(cols []column, w int) bool {
	used := len(cols) // separators
	for _, c := range cols {
		if c.flex {
			used += c.max
			continue
		}
		used += c.width
	}
	return m.contentWidth()-used-w-inspectorGap >= inspectorMin
}

// tableWidth is how wide the active list actually renders, which is how far
// the tab rule should run: to the edge of the table, not the edge of the pane.

// tableWidth is how wide the active list actually renders, which is how far
// the tab rule should run: to the edge of the table, not the edge of the pane.
func (m *Model) tableWidth() int {
	cols := m.jobColumns()
	switch m.focus {
	case focusRuns:
		cols = m.runColumns()
	case focusFlows:
		cols = m.flowColumns()
	case focusThread:
		// The thread has no table under the tabs — it has bubbles, each as wide
		// as its own message — so the rule runs to the width the widest of them
		// is allowed to reach.
		return min(m.contentWidth(), threadBubbleMax+4)
	case focusForum, focusMemory:
		// Neither draws a table, so sizing the rule by the jobs columns — the
		// default this switch would otherwise fall to — would run it to the
		// width of a table that is not on screen.
		return min(m.contentWidth(), summaryWidth)
	}
	total := len(cols) - 1 // separators
	for _, w := range layout(cols, m.contentWidth()) {
		total += w
	}
	if total > m.contentWidth() {
		total = m.contentWidth()
	}
	return total
}

func titles(cols []column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.title
	}
	return out
}

func (m *Model) renderRuns(start, end int) string {
	var b strings.Builder
	runs := m.visibleRuns()
	if len(runs) == 0 {
		b.WriteString(dimStyle.Render("  none") + "\n")
		return b.String()
	}
	cols := m.runColumns()
	widths := layout(cols, m.contentWidth())
	b.WriteString("  " + dimStyle.Render(row(titles(cols), widths)) + "\n")

	// Only the current page is rendered, and the page indicator says how many
	// there are, so nothing is hidden without a sign.
	line := 1
	for i := start; i < end; i++ {
		r := runs[i]
		m.mark(line, hitRun, i)
		line++
		cursor := "  "
		if m.focus == focusRuns && i == m.cursor {
			cursor = selectedStyle.Render(cursorMark + " ")
		}
		detail := r.Note
		if r.Outcome == "parked" && r.ParkReason != "" {
			detail = "waiting: " + r.ParkReason
		}
		steps := m.steps[r.ID]
		if len(steps) > 0 {
			// A flow row says where the sequence got to. The note belongs
			// to whichever step spoke last, and the row is about the run.
			detail = stepProgress(steps)
		}
		cells := []string{r.JobID, r.Outcome, ago(r.StartedAt), detail}
		sized := make([]string, len(cells))
		for k, c := range cells {
			sized[k] = fitMarquee(c, widths[k], m.frame)
		}
		if m.focus == focusRuns && i == m.cursor {
			for k := range sized {
				sized[k] = rowSelected.Render(sized[k])
			}
		} else {
			sized[1] = styleOutcome(sized[1])
			sized[2] = dimStyle.Render(sized[2])
			sized[3] = dimStyle.Render(sized[3])
		}
		b.WriteString(cursor + strings.Join(sized, " ") + "\n")
		if m.expanded[r.ID] && len(steps) > 0 {
			// The step lines belong to the row above them and are not rows
			// themselves, so they are counted but not claimed: a click on one
			// selects nothing rather than selecting whatever row happens to sit
			// that many lines further down.
			stepLines := m.renderStepLines(steps)
			line += blockRows(stepLines)
			b.WriteString(stepLines)
		}
	}
	return b.String()
}

// styleOutcome colors an outcome cell. The cell may already be padded, so the
// style is looked up on the trimmed value: padding must be applied before
// styling, since ANSI escapes would otherwise be counted as visible width.

// styleOutcome colors an outcome cell. The cell may already be padded, so the
// style is looked up on the trimmed value: padding must be applied before
// styling, since ANSI escapes would otherwise be counted as visible width.
func styleOutcome(cell string) string {
	if s, ok := outcomeStyles[strings.TrimSpace(cell)]; ok {
		return s.Render(cell)
	}
	return cell
}

// pad and truncate live in table.go: they measure display width rather than
// bytes, which is what keeps multi-byte glyphs from pushing a column into its
// neighbour.

// pad and truncate live in table.go: they measure display width rather than
// bytes, which is what keeps multi-byte glyphs from pushing a column into its
// neighbour.

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Run starts the board TUI, restarting into a newer build if one appears.
