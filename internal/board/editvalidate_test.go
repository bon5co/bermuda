package board

import (
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// slug derives a new job's id from its name. The store upserts by id, so a
// slug that collides is a job quietly replaced — saveEditor guards that with
// an existence check, but only because slug is deterministic and stable.
func TestSlugDerivesAnIDFromAName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain words", "Daily brief", "daily-brief"},
		{"already a slug", "daily-brief", "daily-brief"},
		{"mixed case", "Daily BRIEF", "daily-brief"},
		{"punctuation collapses", "Daily: brief!! (morning)", "daily-brief-morning"},
		{"runs of spaces are one dash", "daily     brief", "daily-brief"},
		{"digits survive", "N5 shorts 2026", "n5-shorts-2026"},
		{"surrounding space trimmed", "  daily brief  ", "daily-brief"},
		{"leading punctuation drops", "!!daily", "daily"},
		{"trailing punctuation drops", "daily!!", "daily"},
		{"empty stays empty", "", ""},
		{"punctuation only is empty", "!!!", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slug(tc.in); got != tc.want {
				t.Errorf("slug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The id has to be usable as one: whatever a name contains, the slug is
// lowercase, has no spaces, and neither starts nor ends with a separator.
func TestSlugAlwaysProducesAUsableID(t *testing.T) {
	for _, name := range []string{
		"Daily brief", "  weird   Name!! ", "a/b\\c", "tabs\tand\nnewlines",
		"UPPER", "trailing---", "---leading",
	} {
		got := slug(name)
		if got == "" {
			continue
		}
		for _, r := range got {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
			if !ok {
				t.Errorf("slug(%q) = %q contains %q, which is not id-safe", name, got, r)
			}
		}
		if got[0] == '-' || got[len(got)-1] == '-' {
			t.Errorf("slug(%q) = %q starts or ends with a separator", name, got)
		}
		if slug(got) != got {
			t.Errorf("slug is not stable: slug(%q) = %q", got, slug(got))
		}
	}
}

// validateJob catches the cross-field mistakes no single setter can see. A job
// that saves without them does not fail loudly — it sits on the board and
// never fires.
func TestValidateJobCatchesScheduleMismatches(t *testing.T) {
	at := time.Date(2026, 8, 2, 7, 0, 0, 0, time.UTC)
	base := func() store.Job {
		return store.Job{ID: "j", Prompt: "do the thing", CWD: "/tmp"}
	}
	cases := []struct {
		name    string
		mutate  func(*store.Job)
		wantErr bool
	}{
		{"cron with an expression", func(j *store.Job) {
			j.Schedule, j.CronExpr = store.ScheduleCron, "0 7 * * *"
		}, false},
		{"cron without an expression", func(j *store.Job) {
			j.Schedule = store.ScheduleCron
		}, true},
		{"cron with only whitespace", func(j *store.Job) {
			j.Schedule, j.CronExpr = store.ScheduleCron, "   "
		}, true},
		{"interval with seconds", func(j *store.Job) {
			j.Schedule, j.IntervalSeconds = store.ScheduleInterval, 300
		}, false},
		{"interval with zero", func(j *store.Job) {
			j.Schedule = store.ScheduleInterval
		}, true},
		{"interval with a negative", func(j *store.Job) {
			j.Schedule, j.IntervalSeconds = store.ScheduleInterval, -1
		}, true},
		{"once with a time", func(j *store.Job) {
			j.Schedule, j.RunAt = store.ScheduleOnce, &at
		}, false},
		{"once without a time", func(j *store.Job) {
			j.Schedule = store.ScheduleOnce
		}, true},
		{"empty prompt", func(j *store.Job) {
			j.Prompt = ""
		}, true},
		{"whitespace prompt", func(j *store.Job) {
			j.Prompt = "\n\t "
		}, true},
		{"empty working dir", func(j *store.Job) {
			j.CWD = ""
		}, true},
		{"whitespace working dir", func(j *store.Job) {
			j.CWD = " "
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := base()
			tc.mutate(&j)
			err := validateJob(j)
			if tc.wantErr && err == nil {
				t.Error("accepted a job that cannot run")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("rejected a valid job: %v", err)
			}
		})
	}
}

// The bool fields round-trip through strings in the form, so the two halves
// have to agree on the spelling.
func TestBoolFieldRoundTripsThroughTheForm(t *testing.T) {
	if boolStr(true) != "true" || boolStr(false) != "false" {
		t.Fatalf("boolStr renders %q/%q", boolStr(true), boolStr(false))
	}

	fields := jobFields()
	var checked int
	for _, f := range fields {
		if f.kind != fieldBool {
			continue
		}
		checked++
		for _, want := range []bool{true, false} {
			var j store.Job
			if err := f.set(&j, boolStr(want)); err != nil {
				t.Fatalf("field %q rejected %q: %v", f.key, boolStr(want), err)
			}
			if got := f.get(&j); got != boolStr(want) {
				t.Errorf("field %q round-tripped %v to %q", f.key, want, got)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no bool fields found; this test is asserting nothing")
	}
}
