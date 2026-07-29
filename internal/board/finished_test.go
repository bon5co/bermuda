package board

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
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

// The tests above hand the model a last-run map, which is the one thing the
// board does not do for itself. This one loads it from a real store, with the
// finished one-shot's run buried under a busier job's history — which is what
// the board sees on a machine that has been running jobs for a week.
//
// The board used to build that map from the newest hundred runs, so a one-shot
// that ran days ago fell out of the window, read as "never run", and was never
// hidden. Every board test passed throughout, because none of them ever asked
// the store.
func TestTheFinishedRuleSeesARunOlderThanTheRunsWindow(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	at := time.Now().Add(-72 * time.Hour)
	jobs := []store.Job{
		{ID: "old-once", Name: "Old one-shot", Prompt: "p", CWD: "/tmp",
			Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "busy", Name: "Busy job", Prompt: "p", CWD: "/tmp", Enabled: true,
			Schedule: store.ScheduleCron, CronExpr: "* * * * *"},
	}
	for _, j := range jobs {
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	// The one-shot's only run is the oldest row in the table.
	if err := s.PutRun(ctx, store.Run{ID: "old-run", JobID: "old-once",
		Outcome: store.OutcomeDone, Trigger: "scheduled", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	// Then more recent runs than the board's window holds.
	for i := 0; i < 150; i++ {
		r := store.Run{ID: fmt.Sprintf("busy-%03d", i), JobID: "busy",
			Outcome: store.OutcomeDone, Trigger: "scheduled",
			StartedAt: time.Now().Add(-time.Duration(i) * time.Minute)}
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	m := New(s, herdrcli.New(), Deps{DaemonRunning: func() bool { return true }})
	m.width, m.height = 160, 40
	m.apply(t, m.load()())

	if r, ok := m.last["old-once"]; !ok || r.Outcome != store.OutcomeDone {
		t.Fatalf("the board's last run for old-once is %+v (present=%v), want a done run",
			r, ok)
	}
	if got := strings.Join(visibleIDs(m), ","); got != "busy" {
		t.Errorf("visible jobs are %q, want %q", got, "busy")
	}
	if n := m.hiddenFinished(); n != 1 {
		t.Errorf("reported %d hidden, want 1", n)
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
