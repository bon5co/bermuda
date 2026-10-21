package board

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v3/internal/store"
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
	saving   bool
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
func (e *editor) commitField() bool {
	if e.active < 0 {
		return true
	}
	f := e.fields[e.active]
	var val string
	if f.kind == fieldTextArea {
		val = e.area.Value()
	} else {
		val = e.input.Value()
	}
	if err := f.set(&e.job, val); err != nil {
		e.errMsg = err.Error()
		return false
	}
	e.errMsg = ""
	e.area.Blur()
	e.input.Blur()
	e.active = -1
	return true
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
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	// A save already owns this draft. Do not allow its result to dismiss a
	// different edit, or queue another write while the first one is pending.
	if e.saving {
		return m, nil
	}
	if msg.String() == "ctrl+s" {
		if !e.commitField() {
			return m, nil
		}
		return m, m.saveEditor()
	}
	if msg.String() == "tab" || msg.String() == "shift+tab" {
		if !e.commitField() {
			return m, nil
		}
		if msg.String() == "tab" {
			e.cursor = min(e.cursor+1, len(e.fields)-1)
		} else {
			e.cursor = max(e.cursor-1, 0)
		}
		return m, nil
	}

	// Esc abandons the whole form. A multi-line field keeps Enter for newlines.
	if e.active >= 0 {
		switch msg.String() {
		case "esc":
			m.editor = nil
			m.scroll = 0
			return m, m.load()
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
		m.scroll = 0
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
		m.scroll = 0
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
	if e.saving {
		return nil
	}
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
	e.saving = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// A new job must not overwrite an existing one: the store upserts by
		// id, so a name that slugs onto a taken id would silently replace it.
		if isNew {
			exists, err := m.store.Exists(ctx, job.ID)
			if err != nil {
				return editFailedMsg{err.Error()}
			}
			if exists {
				return editFailedMsg{"job " + job.ID + " already exists"}
			}
		}
		taken, err := m.store.NameTaken(ctx, job.Name, job.ID)
		if err != nil {
			return editFailedMsg{err.Error()}
		}
		if taken {
			return editFailedMsg{"another job is already named " + job.Name}
		}
		if err := m.store.PutJob(ctx, job); err != nil {
			return editFailedMsg{err.Error()}
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
	width := m.editorWidth()
	labelWidth, valueWidth := editorRowWidths(width)
	var b strings.Builder

	title := "edit " + e.job.ID
	if e.isNew {
		title = "new job"
	}
	b.WriteString(titleStyle.Render(truncate(title, width)) + "\n\n")

	for i, f := range e.fields {
		cursor := "  "
		if i == e.cursor {
			cursor = selectedStyle.Render(cursorMark + " ")
		}
		label := headerStyle.Render(pad(truncate(f.label, labelWidth), labelWidth))

		if e.active == i {
			if f.kind == fieldTextArea {
				b.WriteString(cursor + label + "\n")
				b.WriteString(e.area.View() + "\n")
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
		line := cursor + label + " " + truncate(val, valueWidth)
		if i == e.cursor && f.help != "" {
			line += "\n  " + dimStyle.Render(truncate(pad("", labelWidth)+" "+f.help, max(1, width-2)))
		}
		b.WriteString(line + "\n")
	}

	return b.String()
}

func (m *Model) editorWidth() int {
	width := m.contentWidth()
	if m.width > 0 {
		// Table columns have a minimum width; text widgets must respect the
		// actual terminal even when it is narrower than that minimum.
		width = min(width, max(1, m.width-3))
	}
	return width
}

func editorRowWidths(width int) (label, value int) {
	label = min(18, max(0, width-10))
	return label, max(1, width-label-3)
}

// editorPane keeps the save action and validation errors beside the draft.
func (m *Model) editorPane() pane {
	e := m.editor
	width := m.editorWidth()
	help := "ctrl+s save job · esc abandon edit · tab/shift+tab move · j/k move · enter edit · space toggle"
	if e.saving {
		help = "saving… · ctrl+c quit"
	} else if e.active >= 0 {
		help = "ctrl+s save job · esc abandon edit · tab/shift+tab commit & move"
		if e.fields[e.active].kind == fieldTextArea {
			help += " · enter newline"
		} else {
			help += " · enter commit field"
		}
	}
	bottom := ""
	if e.errMsg != "" {
		bottom = outcomeStyles["failed"].Render(strings.Join(wrapText(e.errMsg, width), "\n")) + "\n"
	}
	bottom += helpStyle.Render(strings.Join(wrapText(help, width), "\n"))
	_, inputWidth := editorRowWidths(width)
	if e.input.Width != inputWidth {
		// Bubbles keeps horizontal offsets when Width changes. Rebuild them
		// at the new width and restore the user's cursor position.
		value, pos := e.input.Value(), e.input.Position()
		e.input.Width = inputWidth
		e.input.SetValue("")
		e.input.SetValue(value)
		e.input.SetCursor(pos)
	}
	e.area.SetWidth(width)
	e.area.SetHeight(max(1, min(10, m.paneHeight()-blockRows(bottom)-3)))
	// Bubbles populates its viewport during View and follows its cursor during
	// Update. Refresh the wrapped content first, then follow the cursor at the
	// new dimensions (including when opening a long saved prompt).
	if e.active >= 0 && e.fields[e.active].kind == fieldTextArea {
		_ = e.area.View()
		e.area, _ = e.area.Update(nil)
	}
	body := m.renderEditor()
	if e.active >= 0 && e.fields[e.active].kind == fieldTextArea {
		// Follow the whole input area, not just its field label. Otherwise a
		// label that already fits can leave the line being typed offscreen.
		for line, text := range strings.Split(body, "\n") {
			if strings.Contains(text, cursorMark) {
				m.scroll = max(0, line-1)
				break
			}
		}
	}
	return pane{body: body, bottom: bottom}
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
