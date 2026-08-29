// Package sched decides which jobs are due.
//
// It is pure: it reads a job and its last run and returns fire times. Nothing
// here talks to Herdr or the store, so the policy is testable on its own.
package sched

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// parser accepts standard 5-field cron expressions plus descriptors such as
// @hourly, matching what a person expects to be able to type.
var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Due returns the fire times a job owes as of now.
//
// anchor is when the job was last run; the zero time means it has never run.
// The returned slice is empty when nothing is due. It contains more than one
// entry only for catchup=all after downtime.
func Due(j store.Job, anchor time.Time, now time.Time) ([]time.Time, error) {
	if !j.Enabled {
		return nil, nil
	}
	switch j.Schedule {
	case store.ScheduleManual, "":
		return nil, nil

	case store.ScheduleOnce:
		if j.RunAt == nil || now.Before(*j.RunAt) {
			return nil, nil
		}
		// A one-shot that already ran has an anchor at or after its due time.
		if !anchor.IsZero() && !anchor.Before(*j.RunAt) {
			return nil, nil
		}
		return []time.Time{*j.RunAt}, nil

	case store.ScheduleInterval:
		if j.IntervalSeconds <= 0 {
			return nil, fmt.Errorf("job %s: interval schedule with no interval", j.ID)
		}
		every := time.Duration(j.IntervalSeconds) * time.Second
		first := anchor.IsZero()
		from := startFrom(j, anchor, now)
		var fires []time.Time
		for t := from.Add(every); !t.After(now); t = t.Add(every) {
			fires = append(fires, t)
			if len(fires) > maxCatchup {
				break
			}
		}
		return firstRunFilter(first, applyCatchup(j, fires, now), now), nil

	case store.ScheduleCron:
		schedule, err := parser.Parse(j.CronExpr)
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", j.ID, err)
		}
		first := anchor.IsZero()
		from := startFrom(j, anchor, now)
		var fires []time.Time
		for t := schedule.Next(from); !t.After(now); t = schedule.Next(t) {
			fires = append(fires, t)
			if len(fires) > maxCatchup {
				break
			}
		}
		return firstRunFilter(first, applyCatchup(j, fires, now), now), nil

	default:
		return nil, fmt.Errorf("job %s: unknown schedule type %q", j.ID, j.Schedule)
	}
}

// maxCatchup bounds replay after long downtime, so a job that was due every
// minute for a week cannot enqueue ten thousand runs.
const maxCatchup = 100

// firstRunGrace is how stale the first fire of a never-run job may be and
// still count. It is far larger than any sane tick, so the fire a job earns
// while the daemon is up is always taken.
const firstRunGrace = 2 * time.Minute

// startFrom is the point a job's schedule is measured from.
//
// A job that has run is measured from that run. A job that has never run is
// measured from when it was created, NOT from now: every fire computed from
// now lies in the future, so measuring from now means the job never fires, and
// a job that never fires never gets a run to anchor on. That is a deadlock,
// and it is why a scheduled job could sit at "last: never" forever.
//
// Creation time can still be missing or in the future -- a hand-written row, a
// clock that moved -- and then now is the only sane anchor.
func startFrom(j store.Job, anchor time.Time, now time.Time) time.Time {
	if !anchor.IsZero() {
		return anchor
	}
	if j.CreatedAt.IsZero() || j.CreatedAt.After(now) {
		return now
	}
	return j.CreatedAt
}

// firstRunFilter drops a first fire the job was not around for.
//
// Measuring from creation time means a job added last week has a window of
// missed fires behind it. Replaying that window on the first sweep would run
// every scheduled job at once the moment the daemon learns about them, so the
// job instead waits for the next fire that happens while the daemon is
// watching. Once it has run, its anchor is real and ordinary catchup applies.
func firstRunFilter(first bool, fires []time.Time, now time.Time) []time.Time {
	if !first {
		return fires
	}
	kept := fires[:0]
	for _, t := range fires {
		if now.Sub(t) <= firstRunGrace {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// missedAfter is how old a fire may be and still count as "now".
//
// It is what separates a fire the scheduler is standing on from one it missed
// while it was not running, which is the whole distinction catchup=skip is
// about. Generous next to any tick, because a loaded machine can take seconds
// to get around to a sweep and dropping a fire for that would be wrong.
const missedAfter = time.Minute

func applyCatchup(j store.Job, fires []time.Time, now time.Time) []time.Time {
	if len(fires) == 0 {
		return nil
	}
	switch j.Catchup {
	case store.CatchupAll:
		// Every fire owed, replayed in order. The daemon runs them in series.
		return fires
	case store.CatchupSkip:
		// Drop what was missed. Only the current fire survives — and if even
		// the newest one is stale, this job does not run at all, which is what
		// "skip" was always documented to mean and never did: this branch and
		// the one below returned the same slice, so skip behaved like latest.
		last := fires[len(fires)-1]
		if now.Sub(last) > missedAfter {
			return nil
		}
		return fires[len(fires)-1:]
	default: // CatchupLatest
		// Collapse the whole missed window into a single run, however old.
		return fires[len(fires)-1:]
	}
}

// NextFire reports when a job is next due after now, for display. It returns
// the zero time when the job has no future scheduled fire.
func NextFire(j store.Job, anchor time.Time, now time.Time) time.Time {
	switch j.Schedule {
	case store.ScheduleOnce:
		if j.RunAt != nil && j.RunAt.After(now) {
			return *j.RunAt
		}
	case store.ScheduleInterval:
		if j.IntervalSeconds > 0 {
			every := time.Duration(j.IntervalSeconds) * time.Second
			next := startFrom(j, anchor, now).Add(every)
			for !next.After(now) {
				next = next.Add(every)
			}
			return next
		}
	case store.ScheduleCron:
		if schedule, err := parser.Parse(j.CronExpr); err == nil {
			return schedule.Next(now)
		}
	}
	return time.Time{}
}
