package board

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/v2/internal/flow"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// The flows inspector fills the space to the right of the flows table with a
// summary of the selected flow, so the questions the row cannot answer — what
// does this actually do, in what order, and which file do I edit — are answered
// without leaving the list.
//
// The step list is the point of it. A flow's value is its sequence, and the
// table has room for a count: "4" says nothing about whether the fourth step is
// the one that verifies. Everything else here is what a reader reaches for
// straight after that: the path, because these files are written by hand; the
// input, because it decides whether enter opens a box; and the permission
// bypass, because it decides whether an unattended step parks.
//
// It shares the jobs panel's width machinery on purpose. A second set of
// min/max/gap constants would drift from the first, and the column layout
// already measures the space it must leave against inspectorMin and
// inspectorGap.

// flowStepsShown caps how much of the sequence the panel draws.
//
// The panel shares the pane's rows with the table beside it, so an unbounded
// step list would push flows off the board in order to describe one of them.
// What is not drawn is counted out loud underneath: silent truncation reads as
// "that is the whole flow", which for a harness whose entire point is the
// sequence is the worst possible lie.
const flowStepsShown = 6

// renderFlowInspector summarises the selected flow.
func (m *Model) renderFlowInspector(width int) string {
	fr, ok := m.selectedFlow()
	if !ok {
		return ""
	}
	if fr.err != nil {
		return m.renderBrokenFlow(fr.err, width)
	}
	f := fr.flow

	var b strings.Builder
	b.WriteString(titleStyle.Render(fitMarquee(f.ID, width, m.frame)) + "\n")
	// The path scrolls rather than being cut. A truncated path loses its tail,
	// which is the filename — the one part of it the reader was going to type.
	b.WriteString(dimStyle.Render(fitMarquee(f.Path, width, m.frame)) + "\n")

	if about := strings.TrimSpace(f.About); about != "" {
		b.WriteString("\n" + wrap(about, width, 2) + "\n")
	}
	b.WriteString("\n")

	// Labels are short so the values get the room, and the shape is the jobs
	// panel's: two panels that appear in the same place beside the same table
	// and indent differently read as two unrelated things.
	field := func(k, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		b.WriteString(headerStyle.Render(pad(k, 7)) + " " + fitMarquee(v, width-8, m.frame) + "\n")
	}

	if f.TakesInput() {
		field("input", f.Input)
	} else {
		// Said rather than left blank: an absent row is indistinguishable from a
		// row that failed to render, and this is the answer that decides whether
		// enter asks for something or launches on the spot.
		field("input", dimStyle.Render("none — enter runs it"))
	}

	if f.BypassesPermissions() {
		field("perms", dimStyle.Render("unrestricted"))
	} else {
		field("perms", outcomeStyles["parked"].Render("prompts"))
		// skip_permissions: false gets a sentence rather than one field, because
		// it is the surprising answer. The bypass is on by default and on for a
		// reason — nobody is sitting in a flow step's pane — so a step that hits
		// a prompt waits out its grace and parks. A reader who does not know the
		// file turned it off diagnoses that as a hung agent.
		b.WriteString(dimStyle.Render(wrap(
			"this flow turned the bypass off: an agent step that hits a prompt parks",
			width, 2)) + "\n")
	}

	b.WriteString("\n" + headerStyle.Render("STEPS") +
		dimStyle.Render(" "+itoa(len(f.Steps))) + "\n")
	shown := min(len(f.Steps), flowStepsShown)
	for i, s := range f.Steps[:shown] {
		b.WriteString(flowStepLine(i+1, s, width) + "\n")
	}
	if rest := len(f.Steps) - shown; rest > 0 {
		b.WriteString(dimStyle.Render(fit("… "+itoa(rest)+" more steps", width)) + "\n")
	}

	// The last run, from what the tab already loaded. Asking the store again
	// here would be a query per redraw, three seconds apart, for a row the tab
	// keeps anyway.
	if r, ok := m.lastFlow[f.ID]; ok {
		b.WriteString("\n" + headerStyle.Render("LAST RUN") + "\n")
		b.WriteString(styleOutcome(r.Outcome) + dimStyle.Render(" · "+ago(r.StartedAt)) + "\n")
		detail := r.Note
		if r.Outcome == outcomeParked && r.ParkReason != "" {
			detail = "waiting: " + r.ParkReason
		}
		if detail != "" {
			b.WriteString(dimStyle.Render(wrap(detail, width, 2)) + "\n")
		}
	} else {
		b.WriteString("\n" + dimStyle.Render("never run") + "\n")
	}

	return lipgloss.NewStyle().Width(width).Render(b.String())
}

// renderBrokenFlow is the panel for a file that would not parse.
//
// This is the case an empty panel serves worst. The row beside it says only
// that something is wrong, and the reader's next move needs two things: the
// file to open and the line the parser choked on. Both are in the error, so the
// error is what this shows — wrapped, rather than cut at the panel's edge.
func (m *Model) renderBrokenFlow(err error, width int) string {
	var b strings.Builder
	b.WriteString(outcomeStyles["failed"].Render(fit("does not parse", width)) + "\n")
	path, complaint := flowErrorParts(err)
	if path != "" {
		b.WriteString(dimStyle.Render(fitMarquee(path, width, m.frame)) + "\n")
	}
	b.WriteString("\n" + outcomeStyles["failed"].Render(wrap(complaint, width, 8)) + "\n")
	b.WriteString("\n" + dimStyle.Render(fit("fix the file, r reloads", width)) + "\n")
	return lipgloss.NewStyle().Width(width).Render(b.String())
}

// flowErrorParts splits a parse error into the file it is about and the
// complaint about it.
//
// flow.Load prefixes its errors with the path so a reader knows what to open.
// Split out, the path can scroll on a line of its own; left in the prose it
// wraps wherever the panel's width happens to fall, which is not something
// anybody can read back or type.
//
// The message is flattened to words first because a YAML error arrives as a
// heading with the offending line under it. A "line" that is secretly two is
// the same bug flowErrorLine exists to prevent in the table: the panel would
// draw more rows than the step cap and the pane budgeted for.
func flowErrorParts(err error) (path, complaint string) {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	head, rest, ok := strings.Cut(msg, ": ")
	if ok && strings.HasSuffix(head, flow.Ext) {
		return head, rest
	}
	return "", msg
}

// flowStepLine puts one step on exactly one line: what it is called, whether it
// costs tokens, and what it costs them on.
//
// The width is spent here rather than left to the panel's Width style, which
// wraps what it cannot fit. A step drawn as two rows is one row more than the
// cap above counted, and the pane's arithmetic is the only thing keeping the
// board from scrolling itself off the top of the terminal.
func flowStepLine(n int, s store.Step, width int) string {
	right := s.Label()
	if s.IsAgent() {
		// The model a run launched from this board will use: the step's own, or
		// the default its synthesised job carries. "agent" alone does not
		// separate the step worth watching from the two-line mechanical one.
		model := strings.TrimSpace(s.Model)
		if model == "" {
			model = store.DefaultModel
		}
		right += " " + model
	}
	if s.OnFail != nil {
		// Which step this one hands back to. A reader who cannot see the edge
		// reads a run that spent forty minutes on step three as a hung flow
		// rather than as one healing itself.
		right += " ↺" + s.OnFail.Goto
	}
	num := itoa(n) + "."

	// The name gets whatever the rest leaves. It is what resume, the run detail
	// and the step's own directory all call the step by, so it is the half worth
	// keeping when the panel is narrow.
	idw := width - lipgloss.Width(num) - lipgloss.Width(right) - 2
	if idw < 8 {
		right = truncate(right, max(width/3, 1))
		idw = width - lipgloss.Width(num) - lipgloss.Width(right) - 2
	}
	if idw < 1 {
		return fit(num+" "+s.ID, width)
	}
	return dimStyle.Render(num) + " " + fit(s.ID, idw) + " " + dimStyle.Render(right)
}
