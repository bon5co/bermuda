package board

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/v3/internal/sched"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// The inspector fills the space to the right of the jobs table with a summary
// of the selected job, so the common questions — when does this next run, what
// does it run on, how did it go last time — are answered without leaving the
// list.
//
// It is deliberately a summary. The full record, the prompt, and the run
// history live on the detail page; duplicating them here would make a panel
// that is neither glanceable nor complete.

const (
	// inspectorMin is the narrowest useful panel. Below this the labels and
	// values collide, so the panel is dropped rather than rendered cramped.
	inspectorMin = 30
	// inspectorMax keeps the panel from sprawling: past this width the eye has
	// to travel too far from the table to associate the two.
	inspectorMax = 64
	// inspectorGap separates the table from the panel.
	inspectorGap = 3
)

// inspectorWidth is how much room the panel gets, or 0 when there is not
// enough space for one.
func (m *Model) inspectorWidth() int {
	spare := m.contentWidth() - m.tableWidth() - inspectorGap
	if spare < inspectorMin {
		return 0
	}
	if spare > inspectorMax {
		return inspectorMax
	}
	return spare
}

// inspector renders a tab's panel at the width there is room for, or nothing at
// all when there is not. Which panel is the tab's business; whether there is
// space for one is this package's, decided in one place so a tab added later
// cannot invent a second answer.
func (m *Model) inspector(render func(int) string) string {
	w := m.inspectorWidth()
	if w <= 0 {
		return ""
	}
	return render(w)
}

// beside puts a panel in the space to the right of a table.
//
// JoinHorizontal does not end with a newline, and a panel is usually taller
// than the table it sits beside, so without one the next line is appended to the
// panel's last row of padding — which pushed the page counter past the right
// edge of the pane.
func beside(table, panel string) string {
	if panel == "" {
		return table
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		table, strings.Repeat(" ", inspectorGap), panel) + "\n"
}

// renderInspector summarises the selected job.
func (m *Model) renderInspector(width int) string {
	j, ok := m.selectedJob()
	if !ok {
		return ""
	}

	var b strings.Builder
	title := j.Name
	if strings.TrimSpace(title) == "" {
		title = j.ID
	}
	b.WriteString(titleStyle.Render(fitMarquee(title, width, m.frame)) + "\n")
	b.WriteString(dimStyle.Render(fitMarquee(j.ID, width, m.frame)) + "\n\n")

	// Labels are short so the values get the room; this panel exists to be
	// read at a glance, not to be complete.
	field := func(k, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		label := headerStyle.Render(pad(k, 9))
		b.WriteString(label + " " + fitMarquee(v, width-10, m.frame) + "\n")
	}

	state := "enabled"
	if !j.Enabled {
		state = outcomeStyles["parked"].Render("paused")
	}
	if j.Persistent {
		state += dimStyle.Render(" · persistent")
	}
	if j.KeepContext {
		state += dimStyle.Render(" · keeps context")
	}

	field("state", state)
	field("schedule", j.ScheduleLabel())
	field("next", m.nextFireLabel(j))
	// What this job actually does when it fires, which is the difference between
	// one prompt and a sequence somebody has to go and read. The flow id is not
	// enough on its own: a scheduled flow is called with a fixed input, and that
	// input is the argument nothing else on this screen reveals.
	if j.IsFlow() {
		field("flow", j.Flow)
		if strings.TrimSpace(j.Input) != "" {
			field("input", j.Input)
		}
	}
	field("model", j.Model)
	field("timeout", j.Timeout.String())
	if len(j.Tags) > 0 {
		field("tags", strings.Join(j.Tags, ", "))
	}
	field("dir", j.CWD)
	if j.SkipPermissions {
		field("perms", dimStyle.Render("unrestricted"))
	} else {
		field("perms", j.PermissionMode)
	}

	// Last run: the outcome alone rarely answers "what happened", so the note
	// or the park reason comes with it.
	if r, ok := m.last[j.ID]; ok {
		b.WriteString("\n" + headerStyle.Render("LAST RUN") + "\n")
		b.WriteString(styleOutcome(r.Outcome) + dimStyle.Render(" · "+ago(r.StartedAt)) + "\n")
		detail := r.Note
		if r.Outcome == "parked" && r.ParkReason != "" {
			detail = "waiting: " + r.ParkReason
		}
		if detail != "" {
			b.WriteString(dimStyle.Render(wrap(detail, width, 3)) + "\n")
		}
	} else {
		b.WriteString("\n" + dimStyle.Render("never run") + "\n")
	}

	return lipgloss.NewStyle().Width(width).Render(b.String())
}

// nextFirePlain is the next fire time without styling or the relative part, for
// a table cell where the column is narrow and the styling is applied later.
func (m *Model) nextFirePlain(j store.Job) string {
	if !j.Enabled {
		return "paused"
	}
	var anchor time.Time
	if r, ok := m.last[j.ID]; ok {
		anchor = r.StartedAt
	}
	next := sched.NextFire(j, anchor, time.Now())
	if next.IsZero() {
		return ""
	}
	if in := time.Until(next); in < 0 {
		return "due"
	}
	// Same-day fires need only the clock; anything further out needs the date,
	// or "06:00" would be ambiguous about which morning.
	if next.YearDay() == time.Now().YearDay() && next.Year() == time.Now().Year() {
		return next.Format("15:04")
	}
	return next.Format("Jan 2 15:04")
}

// nextFireLabel says when the job is next due, in a form that answers the
// question rather than restating the schedule.
func (m *Model) nextFireLabel(j store.Job) string {
	if !j.Enabled {
		return "paused"
	}
	var anchor time.Time
	if r, ok := m.last[j.ID]; ok {
		anchor = r.StartedAt
	}
	next := sched.NextFire(j, anchor, time.Now())
	if next.IsZero() {
		return ""
	}
	in := time.Until(next).Round(time.Minute)
	if in < 0 {
		return "due"
	}
	return next.Format("15:04") + dimStyle.Render(" (in "+compactDuration(in)+")")
}

// compactDuration renders a duration the way a person would say it.
func compactDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		h := int(d.Hours())
		mins := int(d.Minutes()) - h*60
		if mins == 0 {
			return itoa(h) + "h"
		}
		return itoa(h) + "h " + itoa(mins) + "m"
	default:
		return itoa(int(d.Hours()/24)) + "d"
	}
}

// wrap breaks text to a width, over at most max lines, ending with an ellipsis
// when it does not fit.
func wrap(s string, width, max int) string {
	words := strings.Fields(s)
	var lines []string
	cur := ""
	dropped := false
	for i, w := range words {
		if len(lines) == max {
			// The budget is checked here, at the top of the loop. It used to be
			// a bare `break` inside the switch below, which broke the switch
			// and not the loop: the budget was never enforced, both guards
			// after the loop went dead, and a long note silently lost its tail
			// while overrunning the pane it shares with the job table.
			dropped = i < len(words)
			break
		}
		switch {
		case cur == "":
			cur = w
		case lipgloss.Width(cur+" "+w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		if len(lines) < max {
			lines = append(lines, cur)
		} else {
			// No room left for it: the words are real, they just cannot be shown.
			dropped = true
		}
	}
	if dropped && len(lines) > 0 {
		last := lines[len(lines)-1]
		if lipgloss.Width(last)+1 > width {
			last = truncate(last, width)
		} else {
			last += "…"
		}
		lines[len(lines)-1] = last
	}
	return strings.Join(lines, "\n")
}
