package board

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v2/internal/store"
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

// esc closes the open field without keeping what was typed. The previous value
// survives, and the form stays open on the same job.
func TestCancellingAFieldKeepsThePreviousValue(t *testing.T) {
	m := newTestModel(t)
	e := openEditorOn(t, m, "alpha", "name")

	e.beginField()
	m.typeText(t, " throwaway")
	m.apply(t, tea.KeyMsg{Type: tea.KeyEsc})

	if e.active >= 0 {
		t.Error("esc should close the open field")
	}
	if e.job.Name != "Alpha job" {
		t.Errorf("cancelled edit kept %q", e.job.Name)
	}
	if m.editor == nil {
		t.Error("esc on an open field closed the whole form, not just the field")
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
	m.apply(t, tea.KeyMsg{Type: tea.KeyCtrlS})
	if e.active >= 0 {
		t.Fatal("ctrl+s should close the prompt field")
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
