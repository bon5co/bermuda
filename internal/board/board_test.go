package board

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// These tests drive the model the way a person drives the board: send keys,
// then assert what the model became. They deliberately avoid asserting rendered
// strings, which are full of escape codes and would break on any restyle — the
// exceptions are width invariants, where the whole point is the geometry.
//
// They exist to make a refactor of this package provable: if splitting board.go
// changes behaviour, something here fails.

// newTestModel builds a model over a real store, seeded with jobs and runs.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	jobs := []store.Job{
		{ID: "alpha", Name: "Alpha job", Tags: []string{"one"}, Prompt: "p",
			CWD: "/tmp", Model: "sonnet", Enabled: true, Favorite: true,
			Schedule: store.ScheduleCron, CronExpr: "0 6 * * *"},
		{ID: "beta", Name: "Beta job", Tags: []string{"two"}, Prompt: "p",
			CWD: "/tmp", Model: "opus", Enabled: true, Schedule: store.ScheduleManual},
		{ID: "gamma", Name: "Gamma job", Tags: []string{"one", "two"}, Prompt: "p",
			CWD: "/tmp", Model: "sonnet", Enabled: false, Schedule: store.ScheduleManual},
	}
	for _, j := range jobs {
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	runs := []store.Run{
		{ID: "r1", JobID: "alpha", Outcome: "done", Trigger: "scheduled",
			Note: "all good", StartedAt: time.Now().Add(-time.Hour)},
		{ID: "r2", JobID: "beta", Outcome: "parked", ParkReason: "blocked",
			Trigger: "manual", StartedAt: time.Now().Add(-2 * time.Hour)},
	}
	for _, r := range runs {
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	seedThread(t, s)

	ran := map[string]int{}
	m := New(s, herdrcli.New(), Deps{
		Run:           func(j store.Job, trigger string) error { ran[j.ID]++; return nil },
		DaemonRunning: func() bool { return true },
		EnsureDaemon:  func() error { return nil },
	})
	m.width, m.height = 160, 40
	m.daemonUp = true
	m.testRuns = ran

	// Load real data through the same message the tick uses.
	m.apply(t, m.load()())
	return m
}

// apply feeds a message through Update, as the runtime would.
func (m *Model) apply(t *testing.T, msg tea.Msg) {
	t.Helper()
	if msg == nil {
		return
	}
	if _, cmd := m.Update(msg); cmd != nil {
		// Follow one level of returned command, which is where store writes and
		// their resulting messages live.
		if next := cmd(); next != nil {
			m.Update(next)
		}
	}
}

// press sends a key by name.
func (m *Model) press(t *testing.T, key string) {
	t.Helper()
	m.apply(t, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

// pressSpecial sends a non-rune key.
func (m *Model) pressSpecial(t *testing.T, k tea.KeyType) {
	t.Helper()
	m.apply(t, tea.KeyMsg{Type: k})
}

func TestLoadPopulatesJobsRunsAndLastRun(t *testing.T) {
	m := newTestModel(t)
	if len(m.jobs) != 3 {
		t.Fatalf("loaded %d jobs, want 3", len(m.jobs))
	}
	// Favourites sort first, which is what makes them worth pinning.
	if m.jobs[0].ID != "alpha" {
		t.Errorf("favourite should sort first, got %s", m.jobs[0].ID)
	}
	if got := m.last["alpha"].Outcome; got != "done" {
		t.Errorf("last run for alpha is %q, want done", got)
	}
}

func TestTabCyclesEveryListAndResetsCursor(t *testing.T) {
	m := newTestModel(t)
	m.cursor = 2
	m.pressSpecial(t, tea.KeyTab)
	if m.focus != focusRuns {
		t.Error("tab should move to the runs list")
	}
	if m.cursor != 0 {
		t.Errorf("cursor is %d after switching lists, want 0: the old index means nothing in the new list", m.cursor)
	}
	for _, want := range []focus{focusFlows, focusThread, focusJobs} {
		m.pressSpecial(t, tea.KeyTab)
		if m.focus != want {
			t.Fatalf("tab moved to focus %d, want %d", m.focus, want)
		}
	}

	// Backwards too, or a reader who overshoots has to walk all the way round.
	for _, want := range []focus{focusThread, focusFlows, focusRuns, focusJobs} {
		m.pressSpecial(t, tea.KeyShiftTab)
		if m.focus != want {
			t.Fatalf("shift+tab moved to focus %d, want %d", m.focus, want)
		}
	}
}

func TestCursorMovesAndClampsToTheList(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "k") // already at the top
	if m.cursor != 0 {
		t.Errorf("cursor went above the first row: %d", m.cursor)
	}
	for i := 0; i < 10; i++ {
		m.press(t, "j")
	}
	if m.cursor != len(m.jobs)-1 {
		t.Errorf("cursor is %d, want the last row %d", m.cursor, len(m.jobs)-1)
	}
}

// Horizontal keys mean depth, and Tab means lists. That separation was a
// deliberate decision and is easy to undo by accident.
func TestRightDescendsAndLeftAscends(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "l")
	if m.detail == nil {
		t.Fatal("right on a job should open its detail")
	}
	if m.detail.ID != "alpha" {
		t.Errorf("opened %s, want the selected job alpha", m.detail.ID)
	}
	m.press(t, "h")
	if m.detail != nil {
		t.Error("left should leave the detail view")
	}
}

func TestEnterOnARunOpensTheRunThenItsJob(t *testing.T) {
	m := newTestModel(t)
	m.pressSpecial(t, tea.KeyTab) // runs list
	m.pressSpecial(t, tea.KeyEnter)
	if m.runDetail == nil {
		t.Fatal("enter on a run should open the run detail")
	}
	jobOfRun := m.runDetail.JobID

	m.press(t, "l") // deeper: the job that produced it
	if m.runDetail != nil {
		t.Error("opening the job should leave the run detail")
	}
	if m.detail == nil || m.detail.ID != jobOfRun {
		t.Errorf("should have opened job %s", jobOfRun)
	}
}

func TestSearchFiltersBothListsAndDismissesOnErase(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "/")
	if !m.searching {
		t.Fatal("/ should start a search")
	}
	for _, r := range "alpha" {
		m.press(t, string(r))
	}
	if got := len(m.visibleJobs()); got != 1 {
		t.Errorf("search matched %d jobs, want 1", got)
	}

	// Erasing past the start dismisses the search rather than leaving an empty
	// prompt with nothing to delete.
	for i := 0; i < len("alpha"); i++ {
		m.pressSpecial(t, tea.KeyBackspace)
	}
	m.pressSpecial(t, tea.KeyBackspace)
	if m.searching {
		t.Error("erasing past the start should dismiss the search")
	}
	if m.query != "" {
		t.Errorf("query is %q after dismissal, want empty", m.query)
	}
	if len(m.visibleJobs()) != 3 {
		t.Error("dismissing the search should restore every job")
	}
}

// Selection must follow the filtered list, or a key would act on a job the
// reader cannot see.
func TestSelectionRespectsTheFilter(t *testing.T) {
	m := newTestModel(t)
	m.query = "gamma"
	m.cursor = 0
	j, ok := m.selectedJob()
	if !ok || j.ID != "gamma" {
		t.Fatalf("selected %v (%v), want gamma", j.ID, ok)
	}
}

func TestFavouriteAndPauseWriteThrough(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "f") // alpha is selected and is a favourite
	if j, _ := m.store.Job(context.Background(), "alpha"); j.Favorite {
		t.Error("f should have unpinned alpha")
	}

	m.press(t, "p")
	if j, _ := m.store.Job(context.Background(), "alpha"); j.Enabled {
		t.Error("p should have paused alpha")
	}
}

func TestRunNowInvokesTheRunner(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "R")
	if m.testRuns["alpha"] != 1 {
		t.Errorf("R ran alpha %d times, want 1", m.testRuns["alpha"])
	}
}

// A second run of the same job must not start from the board while the first is
// still going.
func TestRunNowRefusesWhileRunning(t *testing.T) {
	m := newTestModel(t)
	m.running["alpha"] = true
	m.press(t, "R")
	if m.testRuns["alpha"] != 0 {
		t.Error("R should not start a second run of a job already running")
	}
	if !strings.Contains(m.status, "already running") {
		t.Errorf("status should explain the refusal, got %q", m.status)
	}
}

func TestDeleteRemovesTheJobButKeepsItsRuns(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "D")
	ctx := context.Background()
	if _, err := m.store.Job(ctx, "alpha"); err == nil {
		t.Error("D should delete the selected job")
	}
	runs, err := m.store.JobRuns(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Error("deleting a job must keep its run history")
	}
}

func TestNewJobFormOpensWithUnattendedDefaults(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "n")
	if m.editor == nil {
		t.Fatal("n should open the new-job form")
	}
	if !m.editor.isNew {
		t.Error("the form should know it is creating a job")
	}
	if !m.editor.job.SkipPermissions {
		t.Error("a new job must default to skipping permissions: nobody is there to answer a prompt")
	}
	if m.editor.job.Model == "" {
		t.Error("a new job must name a model rather than inheriting the agent's default")
	}
}

func TestEditFormRefusesADuplicateName(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "n")
	m.editor.job.ID = "delta"
	m.editor.job.Name = "Beta job" // already taken
	m.editor.job.Prompt = "p"
	m.editor.job.CWD = "/tmp"
	m.apply(t, m.saveEditor()())

	if m.editor == nil {
		t.Fatal("a rejected save must keep the form open, not discard the edit")
	}
	if !strings.Contains(m.editor.errMsg, "already named") {
		t.Errorf("form error is %q, want a duplicate-name message", m.editor.errMsg)
	}
	if _, err := m.store.Job(context.Background(), "delta"); err == nil {
		t.Error("the job must not have been written")
	}
}

// The editor owns the keyboard while open, or a key meant for a text field would
// act on the list behind it.
func TestEditorSwallowsListKeys(t *testing.T) {
	m := newTestModel(t)
	before := m.cursor
	m.press(t, "e")
	if m.editor == nil {
		t.Fatal("e should open the edit form")
	}
	m.press(t, "j")
	if m.cursor != before {
		t.Error("a key pressed in the editor moved the list behind it")
	}
}

func TestRenderedRowsMatchThePageBounds(t *testing.T) {
	m := newTestModel(t)
	start, end, _, _ := m.page(len(m.visibleJobs()))
	out := m.renderJobs(start, end)
	// One line per row, plus the column titles.
	if got := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1; got != (end-start)+1 {
		t.Errorf("rendered %d lines for rows [%d,%d)", got, start, end)
	}
}

// The help line is the line that grows, and the demo terminal is where a line
// that grew too long is seen: `demo/board.tape` shoots the documentation at
// about 134 columns, and a help line past that is published with its last key
// cut off.
func TestHelpFitsTheDemoTerminal(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = demoColumns, 40
	for _, focus := range []focus{focusJobs, focusRuns, focusThread, focusFlows} {
		m.focus = focus
		for _, line := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(line); w > m.width {
				t.Errorf("a line is %d columns wide, past the %d-column demo terminal: %q",
					w, m.width, line)
			}
		}
	}
}

// demoColumns is how wide the terminal in demo/board.tape is.
const demoColumns = 134

// Width is measured in display columns: a styled line carries escape sequences
// that are not printed, so counting runes would flag lines that fit.
func TestNoRenderedLineExceedsThePane(t *testing.T) {
	m := newTestModel(t)
	for _, focus := range []focus{focusJobs, focusRuns, focusThread, focusFlows} {
		m.focus = focus
		for _, line := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(line); w > m.width {
				t.Errorf("a line is %d columns wide, past the %d-column pane: %q", w, m.width, line)
			}
		}
	}
}
