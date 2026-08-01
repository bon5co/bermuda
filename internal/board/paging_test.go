package board

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/store"
)

func TestVisibleRunsNarrowsAndRestores(t *testing.T) {
	runs := []store.Run{
		{ID: "r1", JobID: "alpha", Outcome: store.OutcomeDone},
		{ID: "r2", JobID: "beta", Outcome: "parked", ParkReason: "waiting on the browser"},
		{ID: "r3", JobID: "alpha", Outcome: "failed"},
	}
	m := &Model{height: 40, runs: runs}

	if got := len(m.visibleRuns()); got != 3 {
		t.Errorf("no query returned %d runs, want all 3", got)
	}

	m.query = "alpha"
	got := m.visibleRuns()
	if len(got) != 2 {
		t.Fatalf("job filter returned %d runs, want 2", len(got))
	}
	for _, r := range got {
		if r.JobID != "alpha" {
			t.Errorf("run %q for job %q survived a filter it does not match", r.ID, r.JobID)
		}
	}

	// The park reason is searchable, because it is the text a reader actually
	// remembers about a stuck run.
	m.query = "browser"
	if got := m.visibleRuns(); len(got) != 1 || got[0].ID != "r2" {
		t.Errorf("park-reason search returned %v, want just r2", got)
	}

	m.query = "nothing matches this"
	if got := len(m.visibleRuns()); got != 0 {
		t.Errorf("unmatched query returned %d runs, want 0", got)
	}
}

// The finished rule hides completed one-shots. A recurring job is never
// finished, and a one-shot that has not run yet is not either — hiding those
// would make a job that is about to fire look deleted.
func TestFinishedRuleHidesOnlyCompletedOneShots(t *testing.T) {
	jobs := []store.Job{
		{ID: "once-done", Schedule: store.ScheduleOnce},
		{ID: "once-pending", Schedule: store.ScheduleOnce},
		{ID: "once-failed", Schedule: store.ScheduleOnce},
		{ID: "cron", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *"},
	}
	m := &Model{height: 40, jobs: jobs, last: map[string]store.Run{
		"once-done":   {ID: "r1", JobID: "once-done", Outcome: store.OutcomeDone},
		"once-failed": {ID: "r2", JobID: "once-failed", Outcome: "failed"},
		"cron":        {ID: "r3", JobID: "cron", Outcome: store.OutcomeDone},
	}}

	visible := map[string]bool{}
	for _, j := range m.visibleJobs() {
		visible[j.ID] = true
	}
	if visible["once-done"] {
		t.Error("a completed one-shot should be put away")
	}
	for _, id := range []string{"once-pending", "once-failed", "cron"} {
		if !visible[id] {
			t.Errorf("%s is not finished and must stay on the list", id)
		}
	}

	if n := m.hiddenFinished(); n != 1 {
		t.Errorf("hiddenFinished said %d, want 1 — the count is what tells the reader the job was not deleted", n)
	}

	m.showFinished = true
	if got := len(m.visibleJobs()); got != len(jobs) {
		t.Errorf("F showed %d of %d jobs", got, len(jobs))
	}
	if n := m.hiddenFinished(); n != 0 {
		t.Errorf("nothing is hidden once F is on, got %d", n)
	}
}

// The hidden count describes the list on screen, not the store: a job the
// query already removed must not be counted a second time as "finished
// hidden", or two different numbers claim to explain the same absence.
func TestHiddenFinishedCountsOnlyWithinTheSearch(t *testing.T) {
	m := &Model{height: 40, jobs: []store.Job{
		{ID: "alpha-done", Schedule: store.ScheduleOnce},
		{ID: "beta-done", Schedule: store.ScheduleOnce},
	}, last: map[string]store.Run{
		"alpha-done": {Outcome: store.OutcomeDone},
		"beta-done":  {Outcome: store.OutcomeDone},
	}}

	if n := m.hiddenFinished(); n != 2 {
		t.Fatalf("unfiltered hidden count %d, want 2", n)
	}
	m.query = "alpha"
	if n := m.hiddenFinished(); n != 1 {
		t.Errorf("with the search on, hidden count %d, want 1", n)
	}
}

func TestPageLabelSaysWhatIsMissing(t *testing.T) {
	m := &Model{height: 40, focus: focusJobs}

	// Always shown, even at 1/1, so the header does not jump when a list grows.
	if got := m.pageLabel(3, 3, 1, 1); got != "page 1/1" {
		t.Errorf("unfiltered label %q, want page 1/1", got)
	}

	m.query = "x"
	label := m.pageLabel(2, 7, 1, 1)
	if !strings.Contains(label, "2 of 7 match") {
		t.Errorf("a filtered list must say how much it is hiding, got %q", label)
	}

	m.query = ""
	m.jobs = []store.Job{{ID: "done", Schedule: store.ScheduleOnce}}
	m.last = map[string]store.Run{"done": {Outcome: store.OutcomeDone}}
	label = m.pageLabel(0, 0, 1, 1)
	if !strings.Contains(label, "1 finished hidden") {
		t.Errorf("the jobs list must account for what the finished rule took, got %q", label)
	}

	// Only the jobs list hides finished jobs, so only it reports them.
	m.focus = focusRuns
	if got := m.pageLabel(0, 0, 1, 1); strings.Contains(got, "finished hidden") {
		t.Errorf("runs list reported a jobs-list rule: %q", got)
	}
}

// Paging keys move the cursor exactly one page and never off the list — the
// selection has to stay resolvable after every step.
func TestMovePageStepsWholePagesAndStaysInRange(t *testing.T) {
	m := &Model{height: 20}
	rows := m.pageRows()
	for i := 0; i < rows*3+2; i++ {
		m.jobs = append(m.jobs, store.Job{ID: "job-" + itoa(i)})
	}
	total := len(m.jobs)

	m.movePage(1)
	if m.cursor != rows {
		t.Errorf("one page down moved to %d, want %d", m.cursor, rows)
	}
	_, _, pageNum, _ := m.page(total)
	if pageNum != 2 {
		t.Errorf("after one page down the reader is on page %d, want 2", pageNum)
	}

	m.movePage(-1)
	if m.cursor != 0 {
		t.Errorf("one page up returned to %d, want 0", m.cursor)
	}

	// Past either end, the cursor clamps rather than escaping the list.
	m.movePage(-5)
	if m.cursor != 0 {
		t.Errorf("paging above the top left cursor at %d", m.cursor)
	}
	m.movePage(99)
	if m.cursor != total-1 {
		t.Errorf("paging past the end left cursor at %d, want %d", m.cursor, total-1)
	}
	if _, ok := m.selectedJob(); !ok {
		t.Error("after paging to the end the cursor must still resolve to a job")
	}
}

// Paging applies to the filtered list, so a search cannot leave the reader on
// a page that no longer exists.
func TestPagingFollowsTheFilteredList(t *testing.T) {
	m := &Model{height: 20}
	rows := m.pageRows()
	for i := 0; i < rows*3; i++ {
		tag := "keep"
		if i%3 != 0 {
			tag = "drop"
		}
		m.jobs = append(m.jobs, store.Job{ID: "job-" + itoa(i), Tags: []string{tag}})
	}

	m.movePage(2) // deep into page 3 of the unfiltered list
	m.query = "keep"
	m.clampCursor()

	visible := len(m.visibleJobs())
	if m.cursor >= visible {
		t.Fatalf("cursor %d past the %d filtered rows", m.cursor, visible)
	}
	start, end, pageNum, pages := m.page(visible)
	if pageNum > pages {
		t.Errorf("reader left on page %d of %d", pageNum, pages)
	}
	if m.cursor < start || m.cursor >= end {
		t.Errorf("cursor %d outside its page bounds [%d,%d)", m.cursor, start, end)
	}
}
