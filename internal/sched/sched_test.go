package sched

import (
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestManualNeverFires(t *testing.T) {
	j := store.Job{ID: "m", Schedule: store.ScheduleManual, Enabled: true}
	got, err := Due(j, time.Time{}, at("2026-07-25 10:00"))
	if err != nil || len(got) != 0 {
		t.Fatalf("manual job fired: %v %v", got, err)
	}
}

func TestDisabledNeverFires(t *testing.T) {
	j := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "* * * * *"}
	got, err := Due(j, at("2026-07-25 09:00"), at("2026-07-25 10:00"))
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled job fired: %v %v", got, err)
	}
}

// A newly added job must not immediately replay history just because it has
// never run.
func TestNeverRunDoesNotBackfill(t *testing.T) {
	now := at("2026-07-25 10:30")
	created := at("2026-07-25 10:29")
	cron := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *",
		Enabled: true, Catchup: store.CatchupAll, CreatedAt: created}
	if got, err := Due(cron, time.Time{}, now); err != nil || len(got) != 0 {
		t.Fatalf("fresh cron job backfilled: %v %v", got, err)
	}
	iv := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
		Enabled: true, Catchup: store.CatchupAll, CreatedAt: created}
	if got, err := Due(iv, time.Time{}, now); err != nil || len(got) != 0 {
		t.Fatalf("fresh interval job backfilled: %v %v", got, err)
	}
}

// The regression that left every scheduled job at "last: never": a job with no
// run must still fire when its schedule comes round, or it can never earn the
// run that would anchor it.
func TestNeverRunFiresWhenItsTimeComes(t *testing.T) {
	created := at("2026-07-24 20:11")

	cron := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *",
		Enabled: true, Catchup: store.CatchupLatest, CreatedAt: created}
	if got, err := Due(cron, time.Time{}, at("2026-07-25 07:00")); err != nil || len(got) != 1 {
		t.Fatalf("never-run cron did not fire at 07:00: %v %v", got, err)
	}

	iv := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
		Enabled: true, Catchup: store.CatchupLatest, CreatedAt: at("2026-07-25 09:00")}
	if got, err := Due(iv, time.Time{}, at("2026-07-25 10:00")); err != nil || len(got) != 1 {
		t.Fatalf("never-run interval did not fire after one interval: %v %v", got, err)
	}
}

// A never-run job measured from its creation time has a window of missed fires
// behind it, and must not replay them the moment the daemon sees it.
func TestNeverRunDoesNotReplayMissedWindow(t *testing.T) {
	created := at("2026-07-20 00:00")
	now := at("2026-07-25 10:30") // hours past today's 07:00 fire

	for _, c := range []string{store.CatchupAll, store.CatchupLatest, store.CatchupSkip} {
		j := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *",
			Enabled: true, Catchup: c, CreatedAt: created}
		if got, err := Due(j, time.Time{}, now); err != nil || len(got) != 0 {
			t.Errorf("catchup=%s: never-run cron replayed: %v %v", c, got, err)
		}
	}

	iv := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
		Enabled: true, Catchup: store.CatchupAll, CreatedAt: created}
	if got, err := Due(iv, time.Time{}, now); err != nil || len(got) != 0 {
		t.Errorf("never-run interval replayed: %v %v", got, err)
	}
}

// Once a job has run, its own run is the anchor and creation time stops
// mattering -- including for the catchup window after downtime.
func TestAnchorWinsOverCreatedAt(t *testing.T) {
	j := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
		Enabled: true, Catchup: store.CatchupAll, CreatedAt: at("2026-07-01 00:00")}
	got, err := Due(j, at("2026-07-25 06:00"), at("2026-07-25 12:00"))
	if err != nil || len(got) != 6 {
		t.Fatalf("want the 6 fires since the last run, got %v %v", got, err)
	}
}

func TestIntervalFiresAfterElapsed(t *testing.T) {
	j := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
		Enabled: true, Catchup: store.CatchupLatest}
	anchor := at("2026-07-25 09:00")
	if got, _ := Due(j, anchor, at("2026-07-25 09:30")); len(got) != 0 {
		t.Fatalf("interval fired early: %v", got)
	}
	if got, _ := Due(j, anchor, at("2026-07-25 10:05")); len(got) != 1 {
		t.Fatalf("interval did not fire: %v", got)
	}
}

// Catchup decides how a window of missed fires collapses.
func TestCatchupPolicies(t *testing.T) {
	anchor := at("2026-07-25 06:00")
	now := at("2026-07-25 12:00") // six missed hourly fires
	base := store.Job{ID: "i", Schedule: store.ScheduleInterval,
		IntervalSeconds: 3600, Enabled: true}

	all := base
	all.Catchup = store.CatchupAll
	got, _ := Due(all, anchor, now)
	if len(got) != 6 {
		t.Errorf("catchup=all: want 6 fires, got %d", len(got))
	}

	latest := base
	latest.Catchup = store.CatchupLatest
	if got, _ := Due(latest, anchor, now); len(got) != 1 {
		t.Errorf("catchup=latest: want 1 fire, got %d", len(got))
	}

	// The newest fire here is 12:00, which is now — the scheduler is standing
	// on it rather than having missed it, so every policy runs it once.
	skip := base
	skip.Catchup = store.CatchupSkip
	if got, _ := Due(skip, anchor, now); len(got) != 1 {
		t.Errorf("catchup=skip: want 1 fire for the current one, got %d", len(got))
	}
}

// Skip drops what was missed, which is the only thing that distinguishes it
// from latest.
//
// Half an hour after the last hourly fire, the daemon was plainly not running
// when it came due. latest still collapses the window into one run; all replays
// every fire; skip runs nothing. skip and latest used to return the same slice,
// so two of the three documented values did the same thing.
func TestCatchupSkipDropsAMissedWindow(t *testing.T) {
	anchor := at("2026-07-25 06:00")
	now := at("2026-07-25 12:30") // last fire was 12:00, missed by 30 minutes
	base := store.Job{ID: "i", Schedule: store.ScheduleInterval,
		IntervalSeconds: 3600, Enabled: true}

	skip := base
	skip.Catchup = store.CatchupSkip
	if got, _ := Due(skip, anchor, now); len(got) != 0 {
		t.Errorf("catchup=skip: want nothing, got %d fires", len(got))
	}

	latest := base
	latest.Catchup = store.CatchupLatest
	if got, _ := Due(latest, anchor, now); len(got) != 1 {
		t.Errorf("catchup=latest: want 1 fire, got %d", len(got))
	}

	all := base
	all.Catchup = store.CatchupAll
	if got, _ := Due(all, anchor, now); len(got) != 6 {
		t.Errorf("catchup=all: want 6 fires, got %d", len(got))
	}
}

// Long downtime must not enqueue an unbounded number of runs.
func TestCatchupIsBounded(t *testing.T) {
	j := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 60,
		Enabled: true, Catchup: store.CatchupAll}
	got, err := Due(j, at("2026-07-01 00:00"), at("2026-07-25 00:00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxCatchup+1 {
		t.Errorf("unbounded catchup: %d fires", len(got))
	}
}

func TestOnceFiresOnlyOnce(t *testing.T) {
	when := at("2026-07-25 09:00")
	j := store.Job{ID: "o", Schedule: store.ScheduleOnce, RunAt: &when, Enabled: true}

	if got, _ := Due(j, time.Time{}, at("2026-07-25 08:59")); len(got) != 0 {
		t.Fatalf("one-shot fired early: %v", got)
	}
	if got, _ := Due(j, time.Time{}, at("2026-07-25 09:01")); len(got) != 1 {
		t.Fatalf("one-shot did not fire: %v", got)
	}
	// Already ran: the anchor is at or after the due time.
	if got, _ := Due(j, when, at("2026-07-25 10:00")); len(got) != 0 {
		t.Fatalf("one-shot fired twice: %v", got)
	}
}

func TestCronFiresOnSchedule(t *testing.T) {
	j := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *",
		Enabled: true, Catchup: store.CatchupLatest}
	anchor := at("2026-07-24 07:00")
	if got, _ := Due(j, anchor, at("2026-07-25 06:59")); len(got) != 0 {
		t.Errorf("cron fired before its time: %v", got)
	}
	if got, _ := Due(j, anchor, at("2026-07-25 07:00")); len(got) != 1 {
		t.Errorf("cron did not fire at 07:00: %v", got)
	}
}

func TestBadCronIsAnError(t *testing.T) {
	j := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "not a cron",
		Enabled: true}
	if _, err := Due(j, at("2026-07-25 09:00"), at("2026-07-25 10:00")); err == nil {
		t.Fatal("expected an error for an invalid cron expression")
	}
}
