package board

import (
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/internal/store"
)

// finishedBoard is a board holding one of each kind of one-shot, plus a
// recurring job whose last run is also done.
func finishedBoard() *Model {
	at := time.Now().Add(-time.Hour)
	jobs := []store.Job{
		{ID: "done-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "parked-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "failed-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "never-once", Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "daily", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *"},
	}
	last := map[string]store.Run{
		"done-once":   {JobID: "done-once", Outcome: "done"},
		"parked-once": {JobID: "parked-once", Outcome: "parked", ParkReason: "needs a human"},
		"failed-once": {JobID: "failed-once", Outcome: "failed"},
		"daily":       {JobID: "daily", Outcome: "done"},
	}
	return &Model{height: 40, width: 120, jobs: jobs, last: last}
}

func visibleIDs(m *Model) []string {
	var out []string
	for _, j := range m.visibleJobs() {
		out = append(out, j.ID)
	}
	return out
}

// Only the one-shot that actually completed is put away. Parked wants a human,
// failed must be noticed, and one that never ran is still pending.
func TestOnlyAFinishedOneShotIsHidden(t *testing.T) {
	m := finishedBoard()
	got := strings.Join(visibleIDs(m), ",")
	want := "parked-once,failed-once,never-once,daily"
	if got != want {
		t.Errorf("visible jobs are %q, want %q", got, want)
	}
}

// A recurring job is never hidden, even when its last run is done: it will
// fire again.
func TestARecurringJobIsNeverHidden(t *testing.T) {
	m := finishedBoard()
	for _, id := range visibleIDs(m) {
		if id == "daily" {
			return
		}
	}
	t.Error("the recurring job was hidden by a done run")
}

func TestTheHiddenCountMatchesWhatWasWithheld(t *testing.T) {
	m := finishedBoard()
	if got, want := m.hiddenFinished(), len(m.jobs)-len(m.visibleJobs()); got != want {
		t.Errorf("reported %d hidden, actually withheld %d", got, want)
	}
	if m.hiddenFinished() != 1 {
		t.Errorf("reported %d hidden, want 1", m.hiddenFinished())
	}
	label := m.pageLabel(len(m.visibleJobs()), len(m.jobs), 1, 1)
	if !strings.Contains(label, "1 finished hidden") {
		t.Errorf("page label %q does not say what is hidden", label)
	}
}

// Showing them puts every job back, and the label stops claiming anything is
// missing.
func TestToggleShowsTheFinishedOnesAndMarksThem(t *testing.T) {
	m := finishedBoard()
	m.showFinished = true
	if got := len(m.visibleJobs()); got != len(m.jobs) {
		t.Fatalf("showing finished jobs listed %d of %d", got, len(m.jobs))
	}
	if n := m.hiddenFinished(); n != 0 {
		t.Errorf("still claims %d hidden while showing them", n)
	}
	table := m.renderJobs(0, len(m.jobs))
	if !strings.Contains(table, "(finished)") {
		t.Error("a shown finished job is not marked (finished)")
	}
}

// The count describes the list on screen: a job the search already removed is
// not also reported as hidden by the finished rule.
func TestTheHiddenCountRespectsTheSearch(t *testing.T) {
	m := finishedBoard()
	m.query = "daily"
	if n := m.hiddenFinished(); n != 0 {
		t.Errorf("counted %d hidden among jobs the search had already dropped", n)
	}
}
