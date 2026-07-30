package board

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// editor edits a job in place.
//
// It works on a copy: nothing reaches the store until the edit is saved, so
// abandoning an edit cannot leave a job half-changed, and a scheduled run
// during the edit still uses the old definition.
type editor struct {
	job    store.Job
	fields []field
	cursor int

	// active is the field currently being typed into; -1 when navigating.
	active   int
	input    textinput.Model
	area     textarea.Model
	isNew    bool
	errMsg   string
	editedID string
}

func newEditor(j store.Job, isNew bool) *editor {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 0

	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.SetHeight(10)
	ta.CharLimit = 0

	return &editor{
		job: j, fields: jobFields(), active: -1,
		input: in, area: ta, isNew: isNew, editedID: j.ID,
	}
}

// beginField opens the editor for the field under the cursor. Booleans and
// choices are changed in place instead, since typing "true" is worse than
// pressing a key.
func (e *editor) beginField() {
	f := e.fields[e.cursor]
	switch f.kind {
	case fieldBool:
		cur := f.get(&e.job) == "true"
		if err := f.set(&e.job, boolStr(!cur)); err != nil {
			e.errMsg = err.Error()
		}
	case fieldChoice:
		e.cycleChoice(f, 1)
	case fieldTextArea:
		e.area.SetValue(f.get(&e.job))
		e.area.Focus()
		e.area.CursorEnd()
		e.active = e.cursor
	default:
		e.input.SetValue(f.get(&e.job))
		e.input.Focus()
		e.input.CursorEnd()
		e.active = e.cursor
	}
}

func (e *editor) cycleChoice(f field, delta int) {
	cur := f.get(&e.job)
	idx := 0
	for i, o := range f.options {
		if o == cur {
			idx = i
			break
		}
	}
	if len(f.options) == 0 {
		return
	}
	idx = (idx + delta + len(f.options)) % len(f.options)
	// A rejected value has to say so. Discarding this error left the editor
	// showing a choice the store had refused, which the next save then dropped
	// without a word.
	if err := f.set(&e.job, f.options[idx]); err != nil {
		e.errMsg = err.Error()
		return
	}
	e.errMsg = ""
}

// commitField writes the open editor's value back to the job copy.
func (e *editor) commitField() {
	if e.active < 0 {
		return
	}
	f := e.fields[e.active]
	var val string
	if f.kind == fieldTextArea {
		val = e.area.Value()
		e.area.Blur()
	} else {
		val = e.input.Value()
		e.input.Blur()
	}
	if err := f.set(&e.job, val); err != nil {
		e.errMsg = err.Error()
	} else {
		e.errMsg = ""
	}
	e.active = -1
}

func (e *editor) cancelField() {
	if e.active < 0 {
		return
	}
	e.area.Blur()
	e.input.Blur()
	e.active = -1
}

// openEditor starts editing the job currently in view.
func (m *Model) openEditor() tea.Cmd {
	j, ok := m.selectedJob()
	if !ok {
		return nil
	}
	m.editor = newEditor(j, false)
	return nil
}

// openNewJob starts a blank job with workable defaults.
func (m *Model) openNewJob() tea.Cmd {
	cwd := "."
	if len(m.jobs) > 0 {
		// Reuse an existing job's directory: a new job is almost always for
		// the same machine and project as the last one.
		cwd = m.jobs[0].CWD
	}
	// Unattended by default: a scheduled job has nobody to answer a permission
	// prompt, so it would simply park until a human noticed.
	j := store.Job{
		Kind: "claude", SkipPermissions: true, Enabled: true,
		Catchup: store.CatchupLatest, Schedule: store.ScheduleManual,
		Timeout: 15 * time.Minute, CWD: cwd, Model: store.DefaultModel,
	}
	m.editor = newEditor(j, true)
	return nil
}

// handleEditorKey drives the edit form.
func (m *Model) handleEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := m.editor

	// While a text editor is open, keys belong to it, except for the two that
	// close it. A multi-line field needs Enter for newlines, so it commits
	// with ctrl+s instead.
	if e.active >= 0 {
		switch msg.String() {
		case "esc":
			e.cancelField()
			return m, nil
		case "ctrl+s":
			e.commitField()
			return m, nil
		case "enter":
			if e.fields[e.active].kind != fieldTextArea {
				e.commitField()
				return m, nil
			}
		}
		var cmd tea.Cmd
		if e.fields[e.active].kind == fieldTextArea {
			e.area, cmd = e.area.Update(msg)
		} else {
			e.input, cmd = e.input.Update(msg)
		}
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.editor = nil
		return m, m.load()
	case "j", "down":
		e.cursor = min(e.cursor+1, len(e.fields)-1)
		return m, nil
	case "k", "up":
		e.cursor = max(e.cursor-1, 0)
		return m, nil
	case "enter", "l", "right":
		e.beginField()
		return m, nil
	case "h", "left":
		// Left cycles a choice backwards; elsewhere it means "leave".
		if e.fields[e.cursor].kind == fieldChoice {
			e.cycleChoice(e.fields[e.cursor], -1)
			return m, nil
		}
		m.editor = nil
		return m, m.load()
	case " ":
		if e.fields[e.cursor].kind == fieldBool {
			e.beginField()
		}
		return m, nil
	case "ctrl+s", "S":
		return m, m.saveEditor()
	}
	return m, nil
}

// saveEditor validates and writes the edited job.
func (m *Model) saveEditor() tea.Cmd {
	e := m.editor
	job := e.job

	if e.isNew && strings.TrimSpace(job.ID) == "" {
		// Derive an id from the name so a new job does not need a separate
		// field for something the name already implies.
		job.ID = slug(job.Name)
		if job.ID == "" {
			e.errMsg = "give the job a name first"
			return nil
		}
	}
	if err := validateJob(job); err != nil {
		e.errMsg = err.Error()
		return nil
	}

	isNew := e.isNew
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// A new job must not overwrite an existing one: the store upserts by
		// id, so a name that slugs onto a taken id would silently replace it.
		if isNew {
			exists, err := m.store.Exists(ctx, job.ID)
			if err != nil {
				return actionMsg{err: err}
			}
			if exists {
				return editFailedMsg{"job " + job.ID + " already exists"}
			}
		}
		taken, err := m.store.NameTaken(ctx, job.Name, job.ID)
		if err != nil {
			return actionMsg{err: err}
		}
		if taken {
			return editFailedMsg{"another job is already named " + job.Name}
		}
		if err := m.store.PutJob(ctx, job); err != nil {
			return actionMsg{err: err}
		}
		return editSavedMsg{jobID: job.ID}
	}
}

type editSavedMsg struct{ jobID string }

// editFailedMsg keeps the form open with the reason, so the edit is not lost.
type editFailedMsg struct{ reason string }

// slug turns a name into an id: lowercase, words joined by hyphens.
func slug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// renderEditor draws the edit form.
func (m *Model) renderEditor() string {
	e := m.editor
	var b strings.Builder

	title := "edit " + e.job.ID
	if e.isNew {
		title = "new job"
	}
	b.WriteString(titleStyle.Render(title) + "\n\n")

	for i, f := range e.fields {
		cursor := "  "
		if i == e.cursor {
			cursor = selectedStyle.Render(cursorMark + " ")
		}
		label := headerStyle.Render(pad(f.label, 18))

		if e.active == i {
			if f.kind == fieldTextArea {
				b.WriteString(cursor + label + "\n")
				b.WriteString(e.area.View() + "\n")
				b.WriteString(dimStyle.Render("      ctrl+s save field · esc cancel") + "\n")
				continue
			}
			b.WriteString(cursor + label + " " + e.input.View() + "\n")
			continue
		}

		val := f.get(&e.job)
		switch f.kind {
		case fieldTextArea:
			val = firstLine(val)
		case fieldBool:
			if val == "true" {
				val = "yes"
			} else {
				val = "no"
			}
		}
		if val == "" {
			val = dimStyle.Render("—")
		} else if f.key == "skip_permissions" && f.get(&e.job) == "true" {
			val = outcomeStyles["failed"].Render("YES — no permission checks")
		}
		line := cursor + label + " " + truncate(val, 70)
		if i == e.cursor && f.help != "" {
			line += "\n  " + dimStyle.Render(pad("", 18)+" "+f.help)
		}
		b.WriteString(line + "\n")
	}

	if e.errMsg != "" {
		b.WriteString("\n" + outcomeStyles["failed"].Render(e.errMsg) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render(
		"j/k move · l/→ or enter edit · space toggle · ctrl+s save job · esc cancel"))
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + fmt.Sprintf(" … (+%d lines)", strings.Count(s, "\n"))
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
