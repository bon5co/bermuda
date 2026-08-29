package board

import (
	"testing"

	"github.com/bon5co/bermuda/v2/internal/flow"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// The cursor is an index into the *filtered* list, so every resolver has to
// filter before it indexes. Getting this wrong does not crash: it silently
// resolves to a different job than the one under the cursor, and the key that
// followed acts on it.

func TestSelectedJobIndexesTheFilteredList(t *testing.T) {
	m := &Model{height: 40, jobs: []store.Job{
		{ID: "alpha", Tags: []string{"x"}},
		{ID: "beta", Tags: []string{"y"}},
		{ID: "gamma", Tags: []string{"x"}},
	}}
	m.query = "x"
	m.cursor = 1 // second *visible* row, which is gamma, not beta

	j, ok := m.selectedJob()
	if !ok {
		t.Fatal("cursor on a visible row resolved to nothing")
	}
	if j.ID != "gamma" {
		t.Errorf("selected %q, want gamma — the cursor indexed the unfiltered list", j.ID)
	}
}

func TestSelectedJobFromRunsListResolvesTheOwningJob(t *testing.T) {
	m := &Model{height: 40, focus: focusRuns,
		jobs: []store.Job{{ID: "alpha"}, {ID: "beta"}},
		runs: []store.Run{{ID: "r1", JobID: "beta"}, {ID: "r2", JobID: "alpha"}},
	}
	m.cursor = 0

	j, ok := m.selectedJob()
	if !ok {
		t.Fatal("a selected run should resolve to the job that owns it")
	}
	if j.ID != "beta" {
		t.Errorf("run r1 belongs to beta, resolved to %q", j.ID)
	}
}

func TestSelectedJobRefusesAnOutOfRangeCursor(t *testing.T) {
	m := &Model{height: 40, jobs: []store.Job{{ID: "alpha"}}}
	m.cursor = 5
	if _, ok := m.selectedJob(); ok {
		t.Error("a cursor past the end must resolve to nothing, not to a job")
	}
}

func TestSelectedJobPrefersTheOpenDetail(t *testing.T) {
	detail := store.Job{ID: "opened"}
	m := &Model{height: 40, detail: &detail,
		jobs:   []store.Job{{ID: "alpha"}, {ID: "beta"}},
		cursor: 1,
	}
	j, ok := m.selectedJob()
	if !ok || j.ID != "opened" {
		t.Errorf("with a job open the cursor addresses its runs, so the job is the open one; got %q ok=%v", j.ID, ok)
	}
}

// The regression this locks: any focus that is not the jobs list used to fall
// through to the last-run lookup and return the last run of whichever job
// happened to be first. The thread has no selection at all, so a key that acts
// on "the selected run" there must get nothing.
func TestSelectedRunIsEmptyWhereThereIsNoSelection(t *testing.T) {
	m := &Model{height: 40,
		jobs: []store.Job{{ID: "alpha"}, {ID: "beta"}},
		last: map[string]store.Run{"alpha": {ID: "r-alpha", JobID: "alpha"}},
	}
	for _, f := range []struct {
		name  string
		focus focus
	}{
		{"thread", focusThread},
		{"flows", focusFlows},
	} {
		m.focus = f.focus
		m.cursor = 0
		if r, ok := m.selectedRun(); ok {
			t.Errorf("%s view has no run selection, resolved to %q", f.name, r.ID)
		}
	}
}

func TestSelectedRunFromJobsListIsThatJobsLastRun(t *testing.T) {
	m := &Model{height: 40, focus: focusJobs,
		jobs: []store.Job{{ID: "alpha"}, {ID: "beta"}},
		last: map[string]store.Run{
			"alpha": {ID: "r-alpha", JobID: "alpha"},
			"beta":  {ID: "r-beta", JobID: "beta"},
		},
	}
	m.cursor = 1
	r, ok := m.selectedRun()
	if !ok {
		t.Fatal("beta has a last run")
	}
	if r.ID != "r-beta" {
		t.Errorf("cursor on beta resolved to %q", r.ID)
	}

	// A job that has never run resolves to nothing rather than to another
	// job's run.
	m.last = map[string]store.Run{"alpha": {ID: "r-alpha", JobID: "alpha"}}
	if r, ok := m.selectedRun(); ok {
		t.Errorf("beta has never run, resolved to %q", r.ID)
	}
}

func TestSelectedRunInDetailIndexesThatJobsRuns(t *testing.T) {
	detail := store.Job{ID: "alpha"}
	m := &Model{height: 40, detail: &detail,
		detailRuns: []store.Run{{ID: "r1"}, {ID: "r2"}},
		cursor:     1,
	}
	r, ok := m.selectedRun()
	if !ok || r.ID != "r2" {
		t.Errorf("detail cursor 1 resolved to %q ok=%v, want r2", r.ID, ok)
	}

	m.cursor = 9
	if _, ok := m.selectedRun(); ok {
		t.Error("a cursor past the detail run list must resolve to nothing")
	}
}

// clampCursor is what keeps every resolver above in range after the list under
// the cursor changes size. It has to clamp against the list the *current* view
// is showing, not against the jobs list.
func TestClampCursorUsesTheListInView(t *testing.T) {
	base := func() *Model {
		return &Model{height: 40,
			jobs:       []store.Job{{ID: "alpha"}, {ID: "beta"}, {ID: "gamma"}},
			runs:       []store.Run{{ID: "r1", JobID: "alpha"}},
			flows:      []flow.Flow{{ID: "f1"}, {ID: "f2"}},
			detailRuns: []store.Run{{ID: "d1"}},
			last:       map[string]store.Run{},
			cursor:     9,
		}
	}
	cases := []struct {
		name  string
		setup func(*Model)
		want  int
	}{
		{"jobs", func(m *Model) { m.focus = focusJobs }, 2},
		{"runs", func(m *Model) { m.focus = focusRuns }, 0},
		{"flows", func(m *Model) { m.focus = focusFlows }, 1},
		{"thread has no rows", func(m *Model) { m.focus = focusThread }, 0},
		{"detail", func(m *Model) { d := store.Job{ID: "alpha"}; m.detail = &d }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.setup(m)
			m.clampCursor()
			if m.cursor != tc.want {
				t.Errorf("cursor clamped to %d, want %d", m.cursor, tc.want)
			}
		})
	}
}

func TestClampCursorNeverGoesNegative(t *testing.T) {
	m := &Model{height: 40, cursor: -5}
	m.clampCursor()
	if m.cursor != 0 {
		t.Errorf("cursor %d, want 0 — a negative index panics every resolver", m.cursor)
	}

	// An empty list clamps to 0, not to -1.
	m.cursor = 3
	m.clampCursor()
	if m.cursor != 0 {
		t.Errorf("empty list left cursor at %d, want 0", m.cursor)
	}
}
