package board

import (
	"fmt"
	"strings"

	"github.com/bon5co/bermuda/v3/internal/runner"
)

// renderDetail shows one job's full configuration and its run history, which
// is what a job is: a schedule plus the exact agent invocation it produces.
func (m *Model) renderDetail() string {
	j := m.detail
	if j == nil {
		return ""
	}
	var b strings.Builder

	title := j.ID
	if j.Name != "" {
		title += "  " + dimStyle.Render(j.Name)
	}
	b.WriteString(titleStyle.Render(title) + "\n")
	if j.Description != "" {
		b.WriteString(dimStyle.Render(j.Description) + "\n")
	}
	b.WriteString("\n")

	row := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString(headerStyle.Render(pad(k, 16)) + " " + v + "\n")
	}
	state := "enabled"
	if !j.Enabled {
		state = "paused"
	}
	if j.Favorite {
		state += ", favorite"
	}
	if j.Persistent {
		state += ", persistent agent"
	}
	if j.KeepContext {
		state += ", keeps context"
	}
	row("tags", strings.Join(j.Tags, ", "))
	row("schedule", j.ScheduleLabel())
	row("catchup", j.Catchup)
	row("state", state)
	row("cwd", j.CWD)
	row("agent", j.Kind)
	row("timeout", j.Timeout.String())
	// Always a concrete model: an unset one resolves to store.DefaultModel, so
	// there is nothing ambiguous to render.
	row("model", j.Model)
	if j.SkipPermissions {
		row("permissions", outcomeStyles["failed"].Render("SKIPPED"))
	} else {
		row("permissions", j.PermissionMode)
	}
	row("allowed tools", j.AllowedTools)
	row("denied tools", j.DisallowedTools)
	row("add dirs", strings.Join(j.AddDirs, ", "))
	row("extra args", j.ExtraArgs)
	row("budget", j.MaxBudgetUSD)
	row("auto-compact", j.AutoCompact)
	row("argv", dimStyle.Render(strings.Join(runner.BuildAgentArgs(*j), " ")))

	b.WriteString("\n" + headerStyle.Render("PROMPT") + "\n")
	for _, line := range promptPreview(j.Prompt, 6) {
		b.WriteString("  " + line + "\n")
	}

	b.WriteString("\n" + headerStyle.Render("RUNS") + "\n")
	if len(m.detailRuns) == 0 {
		b.WriteString(dimStyle.Render("  none") + "\n")
	}
	// Where the run rows start. This view is a page of prose above a list, and
	// the prose is a different length for every job, so the offset is counted
	// off the block already written rather than known in advance.
	line := strings.Count(b.String(), "\n")
	for i, r := range m.detailRuns {
		m.mark(line, hitDetailRun, i)
		line++
		cursor := "  "
		if i == m.cursor {
			cursor = selectedStyle.Render(cursorMark + " ")
		}
		detail := r.Note
		if r.Outcome == "parked" && r.ParkReason != "" {
			detail = "waiting: " + r.ParkReason
		}
		b.WriteString(fmt.Sprintf("%s%s %s %s %s\n", cursor,
			styleOutcome(pad(r.Outcome, 8)),
			dimStyle.Render(pad(r.Trigger, 9)),
			dimStyle.Render(pad(ago(r.StartedAt), 9)),
			dimStyle.Render(truncate(detail, 40))))
	}

	if m.err != nil {
		b.WriteString("\n" + outcomeStyles["failed"].Render("error: "+m.err.Error()) + "\n")
	} else if m.status != "" {
		b.WriteString("\n" + dimStyle.Render(m.status) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render(
		"h/← back · j/k move · l/→ attach agent · R run now · p pause/resume · q quit"))
	return b.String()
}

// promptPreview trims a prompt to a few lines for display.
func promptPreview(prompt string, max int) []string {
	lines := strings.Split(strings.TrimSpace(prompt), "\n")
	var out []string
	for i, l := range lines {
		if i == max {
			out = append(out, dimStyle.Render(fmt.Sprintf("… %d more lines", len(lines)-max)))
			break
		}
		out = append(out, truncate(l, 96))
	}
	return out
}
