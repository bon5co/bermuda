package sched

import (
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// NextFire answers "when does this run next" for the board. A wrong answer
// here is silent: the cell simply shows the wrong clock time, and nobody finds
// out until a job appears to be late or appears to be due and is not.

// A schedule with no future fire must say so with the zero time rather than
// with some plausible-looking date, because the caller renders anything
// non-zero verbatim.
func TestNextFireZeroWhenNothingIsScheduled(t *testing.T) {
	now := at("2026-07-25 10:30")
	runAt := at("2026-07-25 09:00")

	cases := []struct {
		name string
		job  store.Job
	}{
		{"manual", store.Job{ID: "m", Schedule: store.ScheduleManual, Enabled: true}},
		{"no schedule", store.Job{ID: "e", Enabled: true}},
		{"unknown schedule", store.Job{ID: "u", Schedule: "weekly-ish", Enabled: true}},
		{"once with no time", store.Job{ID: "o", Schedule: store.ScheduleOnce, Enabled: true}},
		{"once already past", store.Job{ID: "o", Schedule: store.ScheduleOnce,
			Enabled: true, RunAt: &runAt}},
		{"interval with no interval", store.Job{ID: "i", Schedule: store.ScheduleInterval,
			Enabled: true}},
		{"interval with negative interval", store.Job{ID: "i", Schedule: store.ScheduleInterval,
			Enabled: true, IntervalSeconds: -3600}},
		{"unparseable cron", store.Job{ID: "c", Schedule: store.ScheduleCron,
			Enabled: true, CronExpr: "not a cron"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextFire(tc.job, time.Time{}, now); !got.IsZero() {
				t.Fatalf("want the zero time, got %v", got)
			}
		})
	}
}

// A one-shot that has not happened yet is due exactly when it says, not at
// some interval away from it.
func TestNextFireOnceIsItsOwnTime(t *testing.T) {
	runAt := at("2026-07-25 14:00")
	j := store.Job{ID: "o", Schedule: store.ScheduleOnce, Enabled: true, RunAt: &runAt}
	if got := NextFire(j, time.Time{}, at("2026-07-25 10:30")); !got.Equal(runAt) {
		t.Fatalf("want %v, got %v", runAt, got)
	}
}

// An interval job's fires sit on a grid measured from its last run, so the
// next one is the first grid point strictly after now -- not now plus the
// interval, which would keep sliding the answer forward on every redraw.
func TestNextFireIntervalStepsFromTheAnchor(t *testing.T) {
	j := store.Job{ID: "i", Schedule: store.ScheduleInterval,
		IntervalSeconds: 3600, Enabled: true}
	anchor := at("2026-07-25 09:00")

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before the first fire", at("2026-07-25 09:30"), at("2026-07-25 10:00")},
		{"exactly on a fire", at("2026-07-25 10:00"), at("2026-07-25 11:00")},
		{"several intervals stale", at("2026-07-25 12:30"), at("2026-07-25 13:00")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextFire(j, anchor, tc.now); !got.Equal(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// A job that has never run is measured from when it was created, the same
// anchor Due uses. If the display measured from now instead, a fresh job would
// show a fire time it does not actually have.
func TestNextFireIntervalOfNeverRunJobUsesCreatedAt(t *testing.T) {
	j := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
		Enabled: true, CreatedAt: at("2026-07-25 09:00")}
	want := at("2026-07-25 11:00")
	if got := NextFire(j, time.Time{}, at("2026-07-25 10:30")); !got.Equal(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// Creation time can be missing or ahead of the clock -- a hand-written row, a
// clock that moved. Then now is the only sane anchor, and the answer must
// still be one whole interval away rather than a time in the past.
func TestNextFireIntervalFallsBackToNow(t *testing.T) {
	now := at("2026-07-25 10:30")
	want := at("2026-07-25 11:30")

	for _, tc := range []struct {
		name    string
		created time.Time
	}{
		{"no creation time", time.Time{}},
		{"creation time in the future", at("2026-08-01 00:00")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := store.Job{ID: "i", Schedule: store.ScheduleInterval, IntervalSeconds: 3600,
				Enabled: true, CreatedAt: tc.created}
			if got := NextFire(j, time.Time{}, now); !got.Equal(want) {
				t.Fatalf("want %v, got %v", want, got)
			}
		})
	}
}

// Cron is answered from now, so a job that missed this morning's fire shows
// tomorrow's rather than the one it already slept through.
func TestNextFireCronIsTheNextOccurrence(t *testing.T) {
	j := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *",
		Enabled: true, CreatedAt: at("2026-07-01 00:00")}

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before today's fire", at("2026-07-25 06:00"), at("2026-07-25 07:00")},
		{"after today's fire", at("2026-07-25 10:30"), at("2026-07-26 07:00")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextFire(j, at("2026-07-25 05:00"), tc.now); !got.Equal(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// Descriptors are accepted by the same parser Due uses, so anything the
// scheduler will run has a next-fire time the board can show.
func TestNextFireAcceptsCronDescriptors(t *testing.T) {
	j := store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "@hourly", Enabled: true}
	want := at("2026-07-25 11:00")
	if got := NextFire(j, time.Time{}, at("2026-07-25 10:30")); !got.Equal(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// Whatever the shape of the job, a non-zero answer is always in the future.
// The caller reads a past time as "due", so a stale answer would leave a job
// permanently claiming to be overdue.
func TestNextFireIsNeverInThePast(t *testing.T) {
	now := at("2026-07-25 10:30")
	future := at("2026-07-25 23:00")

	jobs := []struct {
		name   string
		job    store.Job
		anchor time.Time
	}{
		{"interval long overdue", store.Job{ID: "i", Schedule: store.ScheduleInterval,
			IntervalSeconds: 60, Enabled: true}, at("2026-07-01 00:00")},
		{"cron long overdue", store.Job{ID: "c", Schedule: store.ScheduleCron,
			CronExpr: "* * * * *", Enabled: true}, at("2026-07-01 00:00")},
		{"once still ahead", store.Job{ID: "o", Schedule: store.ScheduleOnce,
			Enabled: true, RunAt: &future}, time.Time{}},
	}

	for _, tc := range jobs {
		t.Run(tc.name, func(t *testing.T) {
			got := NextFire(tc.job, tc.anchor, now)
			if got.IsZero() {
				t.Fatalf("want a fire time, got the zero time")
			}
			if !got.After(now) {
				t.Fatalf("next fire %v is not after now %v", got, now)
			}
		})
	}
}

// The display and the scheduler must agree: waiting until the moment NextFire
// names has to be enough for Due to hand back a fire. If they disagreed, a job
// would come due at a time the board never showed.
func TestNextFireAgreesWithDue(t *testing.T) {
	cases := []struct {
		name   string
		job    store.Job
		anchor time.Time
		now    time.Time
	}{
		{"interval", store.Job{ID: "i", Schedule: store.ScheduleInterval,
			IntervalSeconds: 3600, Enabled: true, Catchup: store.CatchupLatest},
			at("2026-07-25 09:00"), at("2026-07-25 09:30")},
		{"cron", store.Job{ID: "c", Schedule: store.ScheduleCron, CronExpr: "0 7 * * *",
			Enabled: true, Catchup: store.CatchupLatest},
			at("2026-07-25 06:00"), at("2026-07-25 06:30")},
		{"once", store.Job{ID: "o", Schedule: store.ScheduleOnce, Enabled: true,
			RunAt: ptr(at("2026-07-25 14:00"))}, time.Time{}, at("2026-07-25 10:30")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := NextFire(tc.job, tc.anchor, tc.now)
			if next.IsZero() {
				t.Fatalf("no next fire to check against Due")
			}
			if got, err := Due(tc.job, tc.anchor, tc.now); err != nil || len(got) != 0 {
				t.Fatalf("job was already due before its next fire: %v %v", got, err)
			}
			got, err := Due(tc.job, tc.anchor, next)
			if err != nil {
				t.Fatalf("Due: %v", err)
			}
			if len(got) == 0 {
				t.Fatalf("nothing due at the announced fire time %v", next)
			}
			if !got[len(got)-1].Equal(next) {
				t.Fatalf("Due fired at %v, NextFire announced %v", got[len(got)-1], next)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }
