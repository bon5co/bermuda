package board

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// prunableBoard is a board over a real store holding one of each kind of
// one-shot, plus a recurring job whose last run is also done.
func prunableBoard(t *testing.T) (*Model, *store.Store, context.Context) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	jobs := []store.Job{
		{ID: "done-once", Name: "Done one-shot", Prompt: "p", CWD: "/tmp",
			Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "parked-once", Name: "Parked one-shot", Prompt: "p", CWD: "/tmp",
			Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "failed-once", Name: "Failed one-shot", Prompt: "p", CWD: "/tmp",
			Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "never-once", Name: "Pending one-shot", Prompt: "p", CWD: "/tmp",
			Schedule: store.ScheduleOnce, RunAt: &at},
		{ID: "daily", Name: "Daily job", Prompt: "p", CWD: "/tmp", Enabled: true,
			Schedule: store.ScheduleCron, CronExpr: "0 7 * * *"},
	}
	for _, j := range jobs {
		if err := s.PutJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	runs := []store.Run{
		{ID: "r1", JobID: "done-once", Outcome: store.OutcomeDone, StartedAt: at},
		{ID: "r2", JobID: "parked-once", Outcome: "parked", ParkReason: "needs a human", StartedAt: at},
		{ID: "r3", JobID: "failed-once", Outcome: "failed", StartedAt: at},
		{ID: "r4", JobID: "daily", Outcome: store.OutcomeDone, StartedAt: at},
	}
	for _, r := range runs {
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	m := New(s, herdrcli.New(), Deps{DaemonRunning: func() bool { return true }})
	m.width, m.height = 160, 40
	m.apply(t, m.load()())
	return m, s, ctx
}

func jobIDs(t *testing.T, s *store.Store, ctx context.Context) []string {
	t.Helper()
	jobs, err := s.Jobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, j := range jobs {
		out = append(out, j.ID)
	}
	return out
}

// P opens a box and writes nothing. The keystroke that deletes several jobs at
// once must not be the same keystroke that chose them.
func TestPruneKeyOnlyAsksTheQuestion(t *testing.T) {
	m, s, ctx := prunableBoard(t)

	m.press(t, "P")
	if m.prune == nil {
		t.Fatal("P did not open the confirmation")
	}
	if got := len(m.prune.jobs); got != 1 {
		t.Fatalf("the box names %d jobs, want 1", got)
	}
	if m.prune.jobs[0].ID != "done-once" {
		t.Errorf("the box names %q, want done-once", m.prune.jobs[0].ID)
	}
	if got, want := len(jobIDs(t, s, ctx)), 5; got != want {
		t.Errorf("opening the box left %d jobs, want %d", got, want)
	}
	// And it says what it would delete, since this box is the only place that
	// list is visible.
	if body := m.renderPrune(); !strings.Contains(body, "done-once") {
		t.Errorf("the box does not name what it would delete:\n%s", body)
	}
}

// Any key that is not y walks away without writing.
func TestPruneCancelsOnAnythingButYes(t *testing.T) {
	for _, key := range []string{"n", "j", "q", "P"} {
		t.Run(key, func(t *testing.T) {
			m, s, ctx := prunableBoard(t)
			m.press(t, "P")
			m.press(t, key)
			if m.prune != nil {
				t.Error("the box is still open")
			}
			if got, want := len(jobIDs(t, s, ctx)), 5; got != want {
				t.Errorf("%q left %d jobs, want %d", key, got, want)
			}
		})
	}
}

// y deletes exactly the finished one-shots, and their runs stay in the store.
func TestPruneYesRemovesOnlyTheFinishedOneShots(t *testing.T) {
	m, s, ctx := prunableBoard(t)

	m.press(t, "P")
	m.press(t, "y")
	if m.prune != nil {
		t.Error("the box stayed open after answering")
	}

	left := strings.Join(jobIDs(t, s, ctx), ",")
	for _, id := range []string{"parked-once", "failed-once", "never-once", "daily"} {
		if !strings.Contains(left, id) {
			t.Errorf("%s was pruned; the store holds %q", id, left)
		}
	}
	if _, err := s.Job(ctx, "done-once"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the finished one-shot survived: %v", err)
	}
	// Pruning loses the schedule, not the record of what happened.
	if _, err := s.Run(ctx, "r1"); err != nil {
		t.Errorf("the pruned job's run was lost too: %v", err)
	}
}

// Nothing to prune is said out loud rather than answered with an empty box.
func TestPruneSaysWhenThereIsNothingToDo(t *testing.T) {
	m, s, ctx := prunableBoard(t)
	if err := s.DeleteJob(ctx, "done-once"); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())

	m.press(t, "P")
	if m.prune != nil {
		t.Fatal("an empty confirmation was opened")
	}
	if !strings.Contains(m.status, "nothing to prune") {
		t.Errorf("status is %q, want it to say there is nothing to prune", m.status)
	}
}

// The help line has to name the key, or the only way to find it is to press it.
func TestTheHelpLineNamesThePruneKey(t *testing.T) {
	m, _, _ := prunableBoard(t)
	if got := m.View(); !strings.Contains(got, "P prune") {
		t.Error("the jobs help line does not mention P")
	}
}
