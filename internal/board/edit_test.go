package board

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// The edit form is the only place a person changes a job by hand, and every
// mistake it can make is quiet: a value written to the wrong field, an edit
// abandoned that stuck anyway, a rejected value that looked accepted, or a new
// job whose id lands on a live one and replaces it. None of those raise an
// error at the time. These tests assert what a person driving the form relies
// on, not how it draws.

// fieldIndex finds an editable field by key, so a test can point the cursor at
// one without depending on the display order.
func fieldIndex(t *testing.T, e *editor, key string) int {
	t.Helper()
	for i, f := range e.fields {
		if f.key == key {
			return i
		}
	}
	t.Fatalf("no field %q in the edit form", key)
	return -1
}

// openEditorOn puts the form on a named job, ready at the given field.
func openEditorOn(t *testing.T, m *Model, jobID, fieldKey string) *editor {
	t.Helper()
	for i, j := range m.jobs {
		if j.ID == jobID {
			m.focus, m.cursor = focusJobs, i
			m.press(t, "e")
			if m.editor == nil {
				t.Fatalf("e did not open the editor on %s", jobID)
			}
			m.editor.cursor = fieldIndex(t, m.editor, fieldKey)
			return m.editor
		}
	}
	t.Fatalf("no job %q on the board", jobID)
	return nil
}

func storedJob(t *testing.T, m *Model, id string) store.Job {
	t.Helper()
	j, err := m.store.Job(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatalf("job %q is no longer in the store", id)
	}
	return *j
}

func TestJobDetailAdvertisesEditingWhileHistoryScrolls(t *testing.T) {
	m := newTestModel(t)
	m.focus, m.height = focusJobs, 24
	m.pressSpecial(t, tea.KeyEnter)
	if m.detail == nil {
		t.Fatal("Enter did not open job detail")
	}
	for i := 0; i < 50; i++ {
		m.detailRuns = append(m.detailRuns, store.Run{ID: "history", JobID: "alpha", Outcome: "done"})
	}
	for _, cursor := range []int{0, 40} {
		m.cursor = cursor
		out := m.View()
		if !strings.Contains(out, "e edit job") {
			t.Fatalf("edit action is hidden at run %d:\n%s", cursor, out)
		}
		if blockRows(out) > 24 {
			t.Fatalf("detail exceeds pane: %d rows", blockRows(out))
		}
	}
}

func TestJobDetailEditsTheFilteredJob(t *testing.T) {
	m := newTestModel(t)
	m.focus, m.query, m.cursor = focusJobs, "Beta", 0
	m.pressSpecial(t, tea.KeyEnter)
	m.press(t, "e")
	if m.editor == nil || m.editor.job.ID != "beta" {
		t.Fatal("editor did not target filtered beta job")
	}
}

func TestFilteredJobDetailDoesNotPromiseEscapeClearsSearch(t *testing.T) {
	m := newTestModel(t)
	m.focus, m.query, m.cursor = focusJobs, "Beta", 0
	m.pressSpecial(t, tea.KeyEnter)
	m.status = "job saved"
	out := m.View()
	if strings.Contains(out, "esc clears") {
		t.Fatal("job detail advertises a search key it does not handle")
	}
	if !strings.Contains(out, "job saved") || !strings.Contains(out, "e edit job") {
		t.Fatal("detail lost status or edit help")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if m.detail != nil || m.query != "Beta" {
		t.Fatal("Escape changed the filtered list when leaving detail")
	}
}

func TestJobReachedThroughARunCanBeEdited(t *testing.T) {
	m := newTestModel(t)
	m.focus, m.cursor = focusRuns, 0
	m.pressSpecial(t, tea.KeyEnter)
	if m.runDetail == nil {
		t.Fatal("run detail did not open")
	}
	m.press(t, "l")
	if m.detail == nil {
		t.Fatal("run's job detail did not open")
	}
	m.press(t, "e")
	if m.editor == nil || m.editor.job.ID != "alpha" {
		t.Fatal("editor did not target run's owner")
	}
}

func TestEditOnAnEmptyJobsListDoesNothing(t *testing.T) {
	m := newTestModel(t)
	m.focus, m.query = focusJobs, "no matching job"
	m.press(t, "e")
	if m.editor != nil {
		t.Fatal("empty jobs list opened an editor")
	}
}

// An abandoned edit must leave nothing behind. The form works on a copy so a
// run scheduled mid-edit still uses the definition on disk.
func TestAbandoningAnEditLeavesTheStoredJobAlone(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "name")

	e.beginField()
	m.typeText(t, " CHANGED")
	e.commitField()
	if e.job.Name != "Alpha job CHANGED" {
		t.Fatalf("the copy holds %q, so the field never took the typed text", e.job.Name)
	}

	m.press(t, "q")
	if m.editor != nil {
		t.Error("q should close the form")
	}
	if got := storedJob(t, m, "alpha").Name; got != "Alpha job" {
		t.Errorf("stored name is %q — an abandoned edit reached the store", got)
	}
}

// Committing writes to the copy and nowhere else; only a save touches the
// store. Saving then has to land every edited field.
func TestSavingWritesTheEditedFieldsToTheStore(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "name")

	e.beginField()
	m.typeText(t, " v2")
	e.commitField()

	if got := storedJob(t, m, "alpha").Name; got != "Alpha job" {
		t.Fatalf("committing a field already wrote %q to the store", got)
	}

	m.apply(t, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.editor != nil {
		t.Error("a successful save should close the form")
	}
	if got := storedJob(t, m, "alpha").Name; got != "Alpha job v2" {
		t.Errorf("stored name is %q, want the edited one", got)
	}
}

func TestControlSSavesTheActiveJobField(t *testing.T) {
	for _, key := range []string{"name", "prompt"} {
		t.Run(key, func(t *testing.T) {
			m := newTestModel(t)
			e := openEditorOn(t, m, "alpha", key)
			e.beginField()
			if key == "prompt" {
				m.pressSpecial(t, tea.KeyEnter)
			}
			m.typeText(t, "updated")
			m.pressSpecial(t, tea.KeyCtrlS)
			if m.editor != nil {
				t.Fatal("Ctrl+S did not save and close the active field")
			}
			got := storedJob(t, m, "alpha")
			if key == "name" && got.Name != "Alpha jobupdated" {
				t.Fatalf("name = %q", got.Name)
			}
			if key == "prompt" && got.Prompt != "p\nupdated" {
				t.Fatalf("prompt = %q", got.Prompt)
			}
		})
	}
}

func TestTabCommitsAFieldAndMovesThroughTheForm(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "name")
	e.beginField()
	m.typeText(t, " updated")
	m.pressSpecial(t, tea.KeyTab)
	if e.active >= 0 || e.cursor != 1 || e.job.Name != "Alpha job updated" {
		t.Fatal("Tab did not commit and advance")
	}
	m.pressSpecial(t, tea.KeyShiftTab)
	if e.cursor != 0 {
		t.Fatal("Shift+Tab did not go back")
	}
	m.pressSpecial(t, tea.KeyShiftTab)
	if e.cursor != 0 {
		t.Fatal("Shift+Tab crossed the first field")
	}
	e.cursor = len(e.fields) - 1
	m.pressSpecial(t, tea.KeyTab)
	if e.cursor != len(e.fields)-1 {
		t.Fatal("Tab crossed the last field")
	}
	e.cursor = fieldIndex(t, e, "prompt")
	e.beginField()
	m.pressSpecial(t, tea.KeyEnter)
	m.typeText(t, "another line")
	m.pressSpecial(t, tea.KeyShiftTab)
	if e.active >= 0 || e.cursor != 2 || e.job.Prompt != "p\nanother line" {
		t.Fatal("Shift+Tab did not commit prompt and move back")
	}
}

func TestInvalidActiveInputBlocksSaveAndNavigation(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyCtrlS, tea.KeyTab, tea.KeyShiftTab, tea.KeyEnter} {
		t.Run(tea.KeyMsg{Type: key}.String(), func(t *testing.T) {
			m := newTestModel(t)
			e := openEditorOn(t, m, "alpha", "timeout")
			e.beginField()
			e.input.SetValue("soon")
			cursor := e.cursor
			m.pressSpecial(t, key)
			if m.editor != e || e.active != cursor || e.cursor != cursor || e.input.Value() != "soon" || !e.input.Focused() || e.errMsg == "" {
				t.Fatal("invalid input lost its value, focus, or error")
			}
			if got := storedJob(t, m, "alpha").Timeout; got != 0 {
				t.Fatalf("invalid timeout persisted: %v", got)
			}
			e.input.SetValue("2m")
			m.pressSpecial(t, tea.KeyCtrlS)
			if m.editor != nil || storedJob(t, m, "alpha").Timeout != 2*time.Minute {
				t.Fatal("correcting the rejected input did not save")
			}
		})
	}
}

func TestFlowJobCanSaveWithoutAPrompt(t *testing.T) {
	m := newTestModel(t)
	j := storedJob(t, m, "beta")
	j.Flow, j.Input, j.Prompt = "daily", "original input", ""
	if err := m.store.PutJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	e := openEditorOn(t, m, "beta", "description")
	e.beginField()
	m.typeText(t, "updated description")
	m.pressSpecial(t, tea.KeyCtrlS)
	if m.editor != nil {
		t.Fatal("valid flow job refused to save")
	}
	got := storedJob(t, m, "beta")
	if got.Description != "updated description" || got.Flow != "daily" || got.Input != "original input" || got.Prompt != "" {
		t.Fatalf("flow job was not preserved: %+v", got)
	}
}

func TestEmptyFlowPromptCanBeCommittedAndSaved(t *testing.T) {
	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyCtrlS} {
		t.Run(tea.KeyMsg{Type: key}.String(), func(t *testing.T) {
			m := newTestModel(t)
			j := storedJob(t, m, "beta")
			j.Flow, j.Input, j.Prompt = "daily", "saved input", ""
			if err := m.store.PutJob(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			m.apply(t, m.load()())
			e := openEditorOn(t, m, "beta", "prompt")
			e.beginField()
			m.pressSpecial(t, key)
			if key == tea.KeyTab {
				if e.active >= 0 {
					t.Fatal("empty flow prompt traps field navigation")
				}
				m.pressSpecial(t, tea.KeyCtrlS)
			}
			if m.editor != nil {
				t.Fatal("empty active flow prompt prevents saving")
			}
			got := storedJob(t, m, "beta")
			if got.Flow != "daily" || got.Input != "saved input" || got.Prompt != "" {
				t.Fatal("flow definition changed")
			}
		})
	}
}

func TestPendingSaveOwnsTheFormUntilItsResult(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "name")
	e.beginField()
	m.typeText(t, " saved")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("active save returned no write command")
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyCtrlS}, {Type: tea.KeyEsc}, {Type: tea.KeyTab}, {Type: tea.KeyRunes, Runes: []rune("q")}, {Type: tea.KeyRunes, Runes: []rune("e")}} {
		_, extra := m.Update(key)
		if extra != nil || m.editor != e || e.job.Name != "Alpha job saved" {
			t.Fatal("a key modified or dismissed the form during save")
		}
	}
	m.apply(t, cmd())
	if m.editor != nil || storedJob(t, m, "alpha").Name != "Alpha job saved" {
		t.Fatal("pending save did not finish")
	}
}

func TestStoreSaveFailureAppearsInTheFormAndAllowsRetry(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "description")
	e.job.Description = "unsaved draft"
	if err := m.store.Close(); err != nil {
		t.Fatal(err)
	}
	m.pressSpecial(t, tea.KeyCtrlS)
	if m.editor != e || e.errMsg == "" || e.job.Description != "unsaved draft" {
		t.Fatal("failed save did not preserve draft and show error in form")
	}
	m.pressSpecial(t, tea.KeyEnter)
	if e.active != e.cursor {
		t.Fatal("failed save left form unable to edit")
	}
}

func TestDuplicateNameSaveKeepsTheDraft(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "name")
	e.beginField()
	e.input.SetValue("Beta job")
	m.pressSpecial(t, tea.KeyCtrlS)
	if m.editor != e || !strings.Contains(e.errMsg, "named") {
		t.Fatal("duplicate name failure was not shown")
	}
	if storedJob(t, m, "alpha").Name != "Alpha job" {
		t.Fatal("duplicate name changed stored job")
	}
	m.pressSpecial(t, tea.KeyEnter)
	e.input.SetValue("Unique job")
	m.pressSpecial(t, tea.KeyCtrlS)
	if m.editor != nil || storedJob(t, m, "alpha").Name != "Unique job" {
		t.Fatal("duplicate-name retry failed")
	}
}

// Drain only the finite commands generated by a user action, including reload batches.
func drainEditAction(t *testing.T, m *Model, msg tea.Msg) {
	t.Helper()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			if cmd != nil {
				drainEditAction(t, m, cmd())
			}
		}
		return
	}
	if msg != nil {
		if _, cmd := m.Update(msg); cmd != nil {
			drainEditAction(t, m, cmd())
		}
	}
}

func TestSaveFromDetailRefreshesTheJobAndPreservesRuns(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusJobs
	m.pressSpecial(t, tea.KeyEnter)
	m.press(t, "e")
	e := m.editor
	e.cursor = fieldIndex(t, e, "description")
	e.beginField()
	m.typeText(t, "new description")
	drainEditAction(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.editor != nil || m.detail == nil || m.detail.ID != "alpha" || m.detail.Description != "new description" {
		t.Fatal("saved detail did not refresh")
	}
	if len(m.detailRuns) != 1 || m.detailRuns[0].ID != "r1" {
		t.Fatal("save changed run history")
	}
	if storedJob(t, m, "alpha").Description != "new description" {
		t.Fatal("detail save did not persist")
	}
}

// One esc abandons the whole form, even with a field open. Neither a schedule
// changed earlier in the form nor text still being typed may reach the store.
func TestEscapeFromActiveFieldAbandonsTheWholeJobEdit(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "schedule")
	m.press(t, "l")
	if e.job.Schedule != store.ScheduleInterval {
		t.Fatalf("schedule did not change in the form: %q", e.job.Schedule)
	}

	e.cursor = fieldIndex(t, e, "name")
	e.beginField()
	m.typeText(t, " throwaway")
	m.apply(t, tea.KeyMsg{Type: tea.KeyEsc})

	if m.editor != nil {
		t.Error("one esc left the form open after closing only the active field")
	}
	got := storedJob(t, m, "alpha")
	if got.Schedule != store.ScheduleCron || got.Name != "Alpha job" {
		t.Errorf("esc saved unsaved edits: schedule=%q name=%q", got.Schedule, got.Name)
	}
}

// Enter commits a single-line field but has to stay a newline inside the
// prompt, which is the one field people write paragraphs in.
func TestEnterCommitsALineButAddsOneInsideTheTextArea(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "prompt")

	e.beginField()
	if e.active < 0 {
		t.Fatal("the prompt field did not open")
	}
	m.typeText(t, "first")
	m.apply(t, tea.KeyMsg{Type: tea.KeyEnter})
	if e.active < 0 {
		t.Fatal("enter closed the prompt instead of adding a line")
	}
	m.typeText(t, "second")
	m.apply(t, tea.KeyMsg{Type: tea.KeyTab})
	if e.active >= 0 {
		t.Fatal("tab should close the prompt field")
	}
	if !strings.Contains(e.job.Prompt, "\n") {
		t.Errorf("prompt is %q — enter did not add a line", e.job.Prompt)
	}

	e.cursor = fieldIndex(t, e, "name")
	e.beginField()
	m.typeText(t, "!")
	m.apply(t, tea.KeyMsg{Type: tea.KeyEnter})
	if e.active >= 0 {
		t.Error("enter should commit a single-line field")
	}
	if e.job.Name != "Alpha job!" {
		t.Errorf("name committed as %q", e.job.Name)
	}
}

// A field that refuses a value has to say so and keep the old one. Silently
// accepting it in the form and dropping it at save is how an edit disappears.
func TestARejectedValueSaysSoAndKeepsTheOldOne(t *testing.T) {
	cases := []struct {
		name  string
		field string
		typed string
	}{
		{"timeout that is not a duration", "timeout", "soon"},
		{"budget that is not a number", "max_budget", "lots"},
		{"run-at in the wrong format", "run_at", "next tuesday"},
		{"prompt emptied", "prompt", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			e := openEditorOn(t, m, "alpha", tc.field)
			f := e.fields[e.cursor]
			before := f.get(&e.job)

			e.active = e.cursor
			if f.kind == fieldTextArea {
				e.area.SetValue(tc.typed)
			} else {
				e.input.SetValue(tc.typed)
			}
			e.commitField()

			if e.errMsg == "" {
				t.Errorf("%q was accepted without a word", tc.typed)
			}
			if got := f.get(&e.job); got != before {
				t.Errorf("field changed to %q despite being rejected (was %q)", got, before)
			}
		})
	}
}

// A choice field cycles in place, both ways, and wraps. Left is "leave" on
// every other kind of field, so the two meanings must not blur.
func TestChoiceFieldCyclesBothWaysAndWraps(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "schedule")
	f := e.fields[e.cursor]
	opts := f.options
	if len(opts) < 3 {
		t.Fatalf("this test needs a few options, got %v", opts)
	}

	if err := f.set(&e.job, opts[0]); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= len(opts); i++ {
		m.press(t, "l")
		want := opts[i%len(opts)]
		if got := f.get(&e.job); got != want {
			t.Fatalf("after %d forward steps the choice is %q, want %q", i, got, want)
		}
	}
	if e.active >= 0 {
		t.Error("a choice is cycled in place, it must not open a text editor")
	}

	m.press(t, "h")
	if got := f.get(&e.job); got != opts[len(opts)-1] {
		t.Errorf("left cycled to %q, want %q", got, opts[len(opts)-1])
	}
	if m.editor == nil {
		t.Fatal("left on a choice field closed the form instead of cycling")
	}

	// On any other field, left means leave.
	e.cursor = fieldIndex(t, e, "name")
	m.press(t, "h")
	if m.editor != nil {
		t.Error("left on a text field should close the form")
	}
}

// Space toggles a bool and does nothing anywhere else — in particular it must
// not open a text editor, since space is how a person scrolls a form.
func TestSpaceTogglesOnlyBooleanFields(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "enabled")
	before := e.job.Enabled

	m.press(t, " ")
	if e.job.Enabled == before {
		t.Error("space did not toggle the boolean")
	}
	m.press(t, " ")
	if e.job.Enabled != before {
		t.Error("space is not its own inverse")
	}

	e.cursor = fieldIndex(t, e, "name")
	nameBefore := e.job.Name
	m.press(t, " ")
	if e.active >= 0 {
		t.Error("space opened a text field")
	}
	if e.job.Name != nameBefore {
		t.Errorf("space changed a text field to %q", e.job.Name)
	}
}

// The cursor stays inside the form. Off either end it would index a field that
// is not there, and the next key would panic on it.
func TestFieldCursorClampsAtBothEnds(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "name")

	for range len(e.fields) + 5 {
		m.press(t, "j")
		if e.cursor < 0 || e.cursor >= len(e.fields) {
			t.Fatalf("cursor left the form at %d of %d fields", e.cursor, len(e.fields))
		}
	}
	if e.cursor != len(e.fields)-1 {
		t.Errorf("cursor stopped at %d, want the last field %d", e.cursor, len(e.fields)-1)
	}
	for range len(e.fields) + 5 {
		m.press(t, "k")
		if e.cursor < 0 || e.cursor >= len(e.fields) {
			t.Fatalf("cursor left the form at %d of %d fields", e.cursor, len(e.fields))
		}
	}
	if e.cursor != 0 {
		t.Errorf("cursor stopped at %d, want the first field", e.cursor)
	}
}

// A new job starts unattended: nobody is there to answer a permission prompt
// for a scheduled run, and a job that starts disabled or on a schedule the
// person did not pick is a surprise either way.
func TestANewJobStartsUnattendedEnabledAndManual(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "n")
	if m.editor == nil {
		t.Fatal("n did not open a new job")
	}
	j := m.editor.job
	if !m.editor.isNew {
		t.Error("the form does not know the job is new, so it will skip the id collision check")
	}
	if !j.SkipPermissions {
		t.Error("a new job would stop on a permission prompt with nobody watching")
	}
	if !j.Enabled {
		t.Error("a new job starts disabled")
	}
	if j.Schedule != store.ScheduleManual {
		t.Errorf("a new job schedules itself as %q", j.Schedule)
	}
	if j.Model == "" {
		t.Error("a new job names no model")
	}
	if j.Timeout <= 0 {
		t.Error("a new job has no timeout, so a stuck run never ends")
	}
	// The working dir is inherited rather than left blank, which would fail
	// validation on a form the person never filled in.
	if j.CWD != m.jobs[0].CWD {
		t.Errorf("new job's working dir is %q, want %q from the job list", j.CWD, m.jobs[0].CWD)
	}
}

// The store upserts by id, so a new job whose name slugs onto a live id would
// replace it outright. This check is the only thing standing between a typo
// and a lost job.
func TestANewJobRefusesToLandOnAnExistingID(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "n")
	e := m.editor
	e.job.Name = "Alpha" // slugs to "alpha", which already exists
	e.job.Prompt = "a different prompt"

	msg := m.saveEditor()()
	failed, ok := msg.(editFailedMsg)
	if !ok {
		t.Fatalf("saving over job alpha returned %T, want a refusal", msg)
	}
	if !strings.Contains(failed.reason, "alpha") {
		t.Errorf("the refusal does not name the job in the way: %q", failed.reason)
	}
	if got := storedJob(t, m, "alpha").Prompt; got == "a different prompt" {
		t.Fatal("the new job overwrote job alpha")
	}
}

// The duplicate-name check has to exclude the job being edited. If it did not,
// editing any other field of a saved job would fail on the name it already
// has, and the form would be unusable for every existing job.
func TestSavingAnExistingJobDoesNotClashWithItsOwnName(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "beta", "description")
	e.beginField()
	m.typeText(t, "now with a description")
	e.commitField()

	msg := m.saveEditor()()
	if failed, bad := msg.(editFailedMsg); bad {
		t.Fatalf("a job clashed with its own name: %q", failed.reason)
	}
	m.apply(t, msg)
	if got := storedJob(t, m, "beta").Description; got != "now with a description" {
		t.Errorf("stored description is %q, want the edited one", got)
	}
}

// A save that cannot go through keeps the form open with the reason on it —
// closing would throw the edit away.
func TestAnUnsaveableJobKeepsTheFormOpenWithTheReason(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*editor)
	}{
		{"new job with no name", func(e *editor) { e.job.Name = "" }},
		{"cron schedule with no expression", func(e *editor) {
			e.job.Name, e.job.Schedule = "fine", store.ScheduleCron
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.press(t, "n")
			e := m.editor
			e.job.Prompt = "p"
			tc.mutate(e)

			if cmd := m.saveEditor(); cmd != nil {
				t.Fatal("an invalid job produced a store write")
			}
			if m.editor == nil {
				t.Fatal("the form closed on a failed save, losing the edit")
			}
			if e.errMsg == "" {
				t.Error("the form gives no reason for refusing to save")
			}
		})
	}
}

// Every field formats a value and parses it back. If the two halves disagree,
// simply opening a job and saving it rewrites the field with something else —
// the failure nobody notices, because nothing was typed.
func TestEveryFieldRoundTripsItsOwnFormatting(t *testing.T) {
	at := time.Date(2026, 8, 2, 7, 30, 0, 0, time.Local)
	full := store.Job{
		ID: "j", Name: "A job", Description: "does a thing",
		Tags: []string{"one", "two"}, Prompt: "line one\nline two",
		Schedule: store.ScheduleCron, CronExpr: "0 7 * * *",
		IntervalSeconds: 900, RunAt: &at, Catchup: store.CatchupAll,
		Timeout: 15 * time.Minute, CWD: "/tmp", Kind: "claude",
		Model: "opus", PermissionMode: "plan", SkipPermissions: true,
		AllowedTools: "Read,Edit", DisallowedTools: "Bash",
		AddDirs: []string{"/a", "/b"}, ExtraArgs: "--verbose",
		MaxBudgetUSD: "2.50", Enabled: true, Favorite: true, Persistent: true,
	}
	for _, f := range jobFields() {
		t.Run(f.key, func(t *testing.T) {
			shown := f.get(&full)
			var into store.Job
			if err := f.set(&into, shown); err != nil {
				t.Fatalf("field %q refused the value it just displayed (%q): %v", f.key, shown, err)
			}
			if got := f.get(&into); got != shown {
				t.Errorf("field %q shows %q but reads back %q", f.key, shown, got)
			}
		})
	}
}

// A choice field can only offer values it accepts, or cycling puts the form in
// a state the save then rejects.
func TestChoiceFieldsOfferOnlyValuesTheyAccept(t *testing.T) {
	for _, f := range jobFields() {
		if f.kind != fieldChoice {
			continue
		}
		for _, o := range f.options {
			var j store.Job
			if err := f.set(&j, o); err != nil {
				t.Errorf("field %q offers %q but refuses it: %v", f.key, o, err)
				continue
			}
			if got := f.get(&j); got != o {
				t.Errorf("field %q stored option %q as %q", f.key, o, got)
			}
		}
	}
}

// The form shows one line per field, so a multi-line prompt is summarised
// rather than allowed to break the layout — and the summary has to say how
// much is hidden.
func TestFirstLineSummarisesAMultiLineValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single line is untouched", "just this", "just this"},
		{"empty is untouched", "", ""},
		{"two lines", "head\ntail", "head … (+1 lines)"},
		{"three lines", "head\nmid\ntail", "head … (+2 lines)"},
		{"trailing newline counts", "head\n", "head … (+1 lines)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstLine(tc.in)
			if got != tc.want {
				t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.Contains(got, "\n") {
				t.Errorf("firstLine(%q) kept a newline", tc.in)
			}
		})
	}
}
