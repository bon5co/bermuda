package board

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// Mouse tests, driven against the rendered frame rather than against the hit
// map: a click arrives as a coordinate on the screen the reader is looking at,
// so the row and column every test clicks are found by searching that frame for
// the text that is on it. Asserting the map against itself would pass with the
// rows an entire table out of place.

// click presses the left button at a screen cell.
func (m *Model) clickCell(t *testing.T, x, y int) {
	t.Helper()
	m.apply(t, tea.MouseMsg{Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft, X: x, Y: y})
}

// wheel turns the wheel one notch.
func (m *Model) wheelBy(t *testing.T, down bool) {
	t.Helper()
	button := tea.MouseButtonWheelUp
	if down {
		button = tea.MouseButtonWheelDown
	}
	m.apply(t, tea.MouseMsg{Action: tea.MouseActionPress, Button: button})
}

// seedManyRuns gives a job more history than a short pane can show.
func seedManyRuns(t *testing.T, m *Model, jobID string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := m.store.PutRun(ctx, store.Run{
			ID: jobID + "-" + itoa(i), JobID: jobID, Outcome: "done",
			Trigger: "manual", Note: "run " + itoa(i),
			StartedAt: time.Now().Add(-time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	m.apply(t, m.load()())
}

// frameRow renders the board and reports which screen row shows want.
func frameRow(t *testing.T, m *Model, want string) (frame string, row int) {
	t.Helper()
	frame = m.View()
	for i, line := range strings.Split(frame, "\n") {
		if strings.Contains(stripStyle(line), want) {
			return frame, i
		}
	}
	t.Fatalf("no row of the board shows %q:\n%s", want, stripStyle(frame))
	return "", 0
}

// frameColumn is where want starts on a line, counted in cells rather than
// bytes: the frame is full of box-drawing runes and escape sequences, and
// neither of those is one byte wide.
func frameColumn(t *testing.T, line, want string) int {
	t.Helper()
	runes := []rune(stripStyle(line))
	w := []rune(want)
	for i := 0; i+len(w) <= len(runes); i++ {
		if string(runes[i:i+len(w)]) == want {
			return i
		}
	}
	t.Fatalf("%q is not on the line %q", want, stripStyle(line))
	return 0
}

func TestClickSelectsTheRowUnderThePointer(t *testing.T) {
	m := newTestModel(t)
	// Every row, not just one: an offset that is wrong by a constant selects
	// something for the first click too, and only looks right.
	for i, name := range []string{"Alpha job", "Beta job", "Gamma job"} {
		_, y := frameRow(t, m, name)
		m.clickCell(t, 4, y)
		if m.cursor != i {
			t.Fatalf("clicking %q on screen row %d selected row %d, want %d",
				name, y, m.cursor, i)
		}
	}
}

// The row under the pointer is the row the search left there, not the row the
// unfiltered list would have had.
func TestClickSelectsWithinTheFilteredList(t *testing.T) {
	m := newTestModel(t)
	m.query = "gamma"
	if got := len(m.visibleJobs()); got != 1 {
		t.Fatalf("the filter leaves %d jobs, want 1", got)
	}
	_, y := frameRow(t, m, "Gamma job")
	m.clickCell(t, 4, y)
	if m.cursor != 0 {
		t.Fatalf("cursor is %d, want 0: the only visible row is the first one", m.cursor)
	}
	if j, _ := m.selectedJob(); j.ID != "gamma" {
		t.Errorf("clicking the only matching row selected %q", j.ID)
	}
}

func TestSecondClickOpensTheSelectedJob(t *testing.T) {
	m := newTestModel(t)
	_, y := frameRow(t, m, "Beta job")

	m.clickCell(t, 4, y)
	if m.detail != nil {
		t.Fatal("the first click opened the job; it should only select it")
	}
	m.clickCell(t, 4, y)
	if m.detail == nil {
		t.Fatal("a second click on the selected row should open it")
	}
	if m.detail.ID != "beta" {
		t.Errorf("opened %q, want beta", m.detail.ID)
	}
}

// The runs list injects step lines under an expanded flow, so the nth row of
// the table is not the nth line of the body. This is the case the hit map
// exists for: arithmetic off the row index selects a run several places from
// the one under the pointer.
func TestClickPastAnExpandedFlowSelectsTheRightRun(t *testing.T) {
	m := newTestModel(t)
	seedFlowRun(t, m)
	m.selectTab(focusRuns)

	runs := m.visibleRuns()
	if len(runs) != 3 || runs[0].ID != "wf1" || runs[2].ID != "r2" {
		t.Fatalf("unexpected runs list: %v", runs)
	}
	// Expand the flow at the top, which pushes everything under it down.
	m.press(t, " ")
	if !m.expanded["wf1"] {
		t.Fatal("space did not expand the flow run")
	}

	frame, y := frameRow(t, m, "waiting: blocked") // the last run, r2
	// The step lines are really there: without them this test would pass on the
	// arithmetic it is meant to rule out.
	if _, plain := frameRow(t, m, "verify"); plain >= y {
		t.Fatalf("the expanded steps are not between the rows:\n%s", stripStyle(frame))
	}
	m.clickCell(t, 4, y)
	if m.cursor != 2 {
		t.Fatalf("clicking the last run selected row %d, want 2", m.cursor)
	}
	if r, _ := m.selectedRun(); r.ID != "r2" {
		t.Errorf("selected run %q, want r2", r.ID)
	}
}

// A step line belongs to the run above it and is not a row of its own.
func TestClickingAnExpandedStepSelectsNothing(t *testing.T) {
	m := newTestModel(t)
	seedFlowRun(t, m)
	m.selectTab(focusRuns)
	m.press(t, " ")
	m.cursor = 0

	_, y := frameRow(t, m, "author")
	m.clickCell(t, 4, y)
	if m.cursor != 0 {
		t.Errorf("clicking a step line moved the cursor to %d", m.cursor)
	}
}

func TestClickOnAFolderTabSwitchesLists(t *testing.T) {
	m := newTestModel(t)
	frame, y := frameRow(t, m, "THREADS")
	line := strings.Split(frame, "\n")[y]

	for _, tc := range []struct {
		label string
		want  focus
	}{
		{"RUNS", focusRuns},
		{"FLOWS", focusFlows},
		{"THREADS", focusThread},
		{"JOBS", focusJobs},
	} {
		// The tab row keeps its geometry as the active tab moves, so the line
		// found once is good for every click.
		m.clickCell(t, frameColumn(t, line, tc.label), y)
		if m.focus != tc.want {
			t.Fatalf("clicking %s moved to focus %d, want %d", tc.label, m.focus, tc.want)
		}
	}
}

// The tab row is only clickable on a frame that drew tabs. The job detail has
// none, and a click at that height there would otherwise leave the reader in a
// different list than the one they came from.
func TestClickOnTheTabRowDoesNothingInTheJobDetail(t *testing.T) {
	m := newTestModel(t)
	frame, y := frameRow(t, m, "THREADS")
	x := frameColumn(t, strings.Split(frame, "\n")[y], "RUNS")

	m.press(t, "l") // open the first job
	if m.detail == nil {
		t.Fatal("l did not open the job detail")
	}
	m.View() // the frame the click lands on
	m.clickCell(t, x, y)
	if m.focus != focusJobs {
		t.Errorf("a click in the detail switched to focus %d", m.focus)
	}
	if m.detail == nil {
		t.Error("the click closed the detail")
	}
}

// The inspector describes the selected row; clicking it must not choose a
// different one.
func TestClickInTheInspectorLeavesTheSelectionAlone(t *testing.T) {
	m := newTestModel(t)
	if !m.spareAfter(m.jobColumns(), 0) && m.tableWidth()+2 >= m.width {
		t.Skip("no inspector at this width")
	}
	_, y := frameRow(t, m, "Gamma job")
	m.clickCell(t, m.tableWidth()+4, y)
	if m.cursor != 0 {
		t.Errorf("a click in the inspector moved the cursor to %d", m.cursor)
	}
}

// A box being typed into owns the input: a click that moved the selection under
// it would launch a flow the reader had not been looking at.
func TestClickIsIgnoredWhileAFlowInputIsOpen(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml": flowWithInput, "sweep.yml": flowWithoutInput})
	fb.selectFlow(t, "triage")
	fb.pressSpecial(t, tea.KeyEnter)
	if fb.flowInput == nil {
		t.Fatal("enter on a flow that takes an input should ask for it")
	}
	before := fb.cursor

	_, y := frameRow(t, fb.Model, "sweep")
	fb.Model.clickCell(t, 4, y)
	if fb.cursor != before {
		t.Errorf("a click moved the cursor to %d while the input box was open", fb.cursor)
	}
	if len(fb.started) != 0 {
		t.Errorf("the click started something: %v", fb.started)
	}
}

// A list is paged from its cursor, so the wheel moves the cursor and lets the
// page follow it.
func TestWheelMovesTheCursorInAList(t *testing.T) {
	m := newTestModel(t)
	m.View()
	m.wheelBy(t, true)
	if m.cursor != 2 {
		t.Errorf("a wheel notch left the cursor at %d, want 2 (clamped to the last job)", m.cursor)
	}
	m.wheelBy(t, false)
	if m.cursor != 0 {
		t.Errorf("wheeling back left the cursor at %d, want 0", m.cursor)
	}
}

// The conversation has no selection, so there the wheel moves the window — and
// scrolling up stops it following its newest message, or the next render drags
// the reader straight back down.
func TestWheelScrollsTheThreadAndStopsItFollowing(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusThread)
	m.View()
	m.wheelBy(t, true)
	if m.cursor != 0 {
		t.Errorf("the wheel moved a cursor the thread does not have: %d", m.cursor)
	}
	m.wheelBy(t, false)
	if m.threadFollow {
		t.Error("scrolling up left the thread following its newest message")
	}
}

// The scroll hint counts the rows that did not fit. It is not one of them, and
// clicking it must not reach the first row below the fold.
func TestClickOnTheScrollHintSelectsNothing(t *testing.T) {
	m := newTestModel(t)
	seedManyRuns(t, m, "alpha", 30)
	m.press(t, "l") // the job detail, which is windowed rather than paged
	if m.detail == nil {
		t.Fatal("l did not open the job detail")
	}
	m.height = 16

	frame, y := frameRow(t, m, "more")
	if !strings.Contains(stripStyle(strings.Split(frame, "\n")[y]), "↓") {
		t.Fatalf("no scroll hint on the detail page:\n%s", stripStyle(frame))
	}
	before := m.cursor
	m.clickCell(t, 4, y)
	if m.cursor != before {
		t.Errorf("clicking the scroll hint moved the cursor from %d to %d", before, m.cursor)
	}
}

// A board left open all day is a board somebody copies a run id out of, and a
// program holding the mouse has taken the terminal's own selection with it.
func TestMouseCanBeHandedBackToTheTerminal(t *testing.T) {
	m := newTestModel(t)
	_, y := frameRow(t, m, "Gamma job")

	m.press(t, "M")
	if !m.mouseOff {
		t.Fatal("M did not release the mouse")
	}
	m.clickCell(t, 4, y)
	if m.cursor != 0 {
		t.Errorf("a click was acted on after the mouse was released: cursor %d", m.cursor)
	}

	m.press(t, "M")
	if m.mouseOff {
		t.Fatal("M did not take the mouse back")
	}
	m.View()
	m.clickCell(t, 4, y)
	if m.cursor != 2 {
		t.Errorf("clicking after taking the mouse back selected %d, want 2", m.cursor)
	}
}

// `M` is a letter in a box that is being typed into, and a search for a job
// with a capital M in its name must not turn the mouse off.
func TestMDoesNotReleaseTheMouseWhileSearching(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "/")
	m.press(t, "M")
	if m.mouseOff {
		t.Error("M released the mouse while a search was being typed")
	}
	if m.queryDraft != "M" {
		t.Errorf("the search box has %q, want M", m.queryDraft)
	}
}

// Motion is not a click. With cell motion enabled a drag arrives as a stream of
// motion events, and treating them as presses drags the selection across every
// row the pointer crosses.
func TestDraggingDoesNotMoveTheSelection(t *testing.T) {
	m := newTestModel(t)
	_, y := frameRow(t, m, "Gamma job")
	m.apply(t, tea.MouseMsg{Action: tea.MouseActionMotion,
		Button: tea.MouseButtonLeft, X: 4, Y: y})
	if m.cursor != 0 {
		t.Errorf("a drag moved the cursor to %d", m.cursor)
	}
}
