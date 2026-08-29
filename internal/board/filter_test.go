package board

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
)

func TestSearchMatchesEveryFieldSomeoneWouldType(t *testing.T) {
	j := store.Job{
		ID: "daily-brief", Name: "Daily brief", Description: "Morning summary",
		Tags: []string{"marketing", "daily"}, Schedule: store.ScheduleCron,
		CronExpr: "0 7 * * *",
	}
	for _, q := range []string{"daily", "DAILY", "brief", "morning", "marketing", "0 7"} {
		if !matchesJob(j, q) {
			t.Errorf("query %q should have matched", q)
		}
	}
	if matchesJob(j, "nonsense") {
		t.Error("unrelated query matched")
	}
	if !matchesJob(j, "") {
		t.Error("an empty query must match everything")
	}
}

func TestRunSearchIncludesOutcomeAndNote(t *testing.T) {
	r := store.Run{JobID: "smoke", Outcome: "parked", ParkReason: "blocked",
		Note: "Said hello", Trigger: "scheduled"}
	for _, q := range []string{"smoke", "parked", "blocked", "hello", "sched"} {
		if !matchesRun(r, q) {
			t.Errorf("query %q should have matched", q)
		}
	}
}

func TestFilteringNarrowsTheList(t *testing.T) {
	m := &Model{height: 40, jobs: []store.Job{
		{ID: "alpha", Tags: []string{"x"}},
		{ID: "beta", Tags: []string{"y"}},
		{ID: "gamma", Tags: []string{"x"}},
	}}
	m.query = "x"
	if got := len(m.visibleJobs()); got != 2 {
		t.Errorf("tag filter returned %d jobs, want 2", got)
	}
	m.query = ""
	if got := len(m.visibleJobs()); got != 3 {
		t.Errorf("cleared filter returned %d jobs, want 3", got)
	}
}

// Paging must follow the cursor: selecting a row past the bottom of a page
// turns the page, so the selection can never be off-screen.
func TestPageFollowsCursor(t *testing.T) {
	m := &Model{height: 20}
	rows := m.pageRows()
	total := rows*3 + 1

	_, _, pageNum, pages := m.page(total)
	if pageNum != 1 {
		t.Errorf("cursor at 0 should be on page 1, got %d", pageNum)
	}
	if pages != 4 {
		t.Errorf("want 4 pages for %d rows at %d per page, got %d", total, rows, pages)
	}

	m.cursor = rows // first row of page 2
	start, end, pageNum, _ := m.page(total)
	if pageNum != 2 {
		t.Errorf("cursor at %d should be on page 2, got %d", rows, pageNum)
	}
	if m.cursor < start || m.cursor >= end {
		t.Errorf("cursor %d outside its own page bounds [%d,%d)", m.cursor, start, end)
	}
}

func TestPageBoundsNeverExceedTotal(t *testing.T) {
	m := &Model{height: 20}
	for _, total := range []int{0, 1, 5, 37, 100} {
		m.cursor = total // deliberately past the end
		start, end, _, _ := m.page(total)
		if start < 0 || end > total || start > end {
			t.Errorf("total %d produced bounds [%d,%d)", total, start, end)
		}
	}
}

func TestHealthReportsWhatIsWrong(t *testing.T) {
	m := &Model{daemonUp: true}
	if got := m.health(); got != "" {
		t.Errorf("healthy board reported %q", got)
	}

	// A stopped scheduler outranks a read error: without it nothing on screen
	// will ever fire.
	m.daemonUp, m.err = false, errTest
	if got := m.health(); got != "scheduler stopped" {
		t.Errorf("want scheduler stopped, got %q", got)
	}

	m.daemonUp = true
	if got := m.health(); got != "store error" {
		t.Errorf("want store error, got %q", got)
	}
}

func TestBrandStatesTheProblemBesideTheDot(t *testing.T) {
	m := &Model{daemonUp: false}
	brand := m.renderBrand()
	if !strings.Contains(brand, "Bermuda") {
		t.Error("brand should name the board")
	}
	if !strings.Contains(brand, "scheduler stopped") {
		t.Error("a red dot must say what is wrong, or it only raises the question")
	}
	if !strings.Contains(m.renderBrand(), "●") {
		t.Error("status dot missing")
	}
}

var errTest = testErr{}

type testErr struct{}

func (testErr) Error() string { return "boom" }

// A taller pane must show a longer list. The regression this guards: a
// hardcoded cap left most of a 60-row pane empty while paginating a list that
// already fitted on screen.
func TestPageRowsGrowWithHeight(t *testing.T) {
	prev := 0
	for _, h := range []int{20, 30, 45, 60, 100} {
		m := &Model{height: h}
		rows := m.pageRows()
		if rows <= prev {
			t.Errorf("height %d gave %d rows, no more than the %d of a shorter pane", h, rows, prev)
		}
		if rows > h {
			t.Errorf("height %d gave %d rows, more than the pane has", h, rows)
		}
		prev = rows
	}
}

// The list plus its chrome must never exceed the pane, or the help line and
// page counter would scroll off the bottom.
func TestPageRowsLeaveRoomForChrome(t *testing.T) {
	for h := 12; h <= 120; h++ {
		m := &Model{height: h}
		if got := m.pageRows() + chromeRows; got > h && m.pageRows() > 3 {
			t.Fatalf("height %d: %d rows plus chrome is %d, over the pane", h, m.pageRows(), got)
		}
	}
}
