package main

import (
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// applyArgs parses argv against the real job flag set and applies it to j, the
// way `job add` and `job edit` do.
func applyArgs(t *testing.T, j *store.Job, argv ...string) error {
	t.Helper()
	fs := flag.NewFlagSet("job test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerJobFlags(fs)
	if err := fs.Parse(argv); err != nil {
		t.Fatalf("parse %v: %v", argv, err)
	}
	return f.apply(fs, j)
}

// storedJob is a job as `job edit` would find it: every field already set to
// something a careless apply could wipe.
func storedJob() store.Job {
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	return store.Job{
		ID:              "nightly",
		Name:            "Nightly sweep",
		Description:     "sweeps",
		Prompt:          "do the thing",
		CWD:             "/srv/work",
		Tags:            []string{"ops", "daily"},
		Kind:            "claude",
		Model:           "opus",
		PermissionMode:  "acceptEdits",
		AllowedTools:    "Read,Edit",
		DisallowedTools: "Bash",
		AddDirs:         []string{"/srv/extra"},
		ExtraArgs:       "--verbose",
		SkipPermissions: true,
		MaxBudgetUSD:    "2.50",
		Schedule:        store.ScheduleCron,
		CronExpr:        "0 7 * * *",
		IntervalSeconds: 0,
		RunAt:           &at,
		Catchup:         store.CatchupSkip,
		Timeout:         30 * time.Minute,
		Enabled:         false,
		Favorite:        true,
		Persistent:      true,
	}
}

// An edit that names no flags must change nothing. This is the entire reason
// apply consults fs.Visit instead of reading the pointers: every bool flag has
// a default that would otherwise be written over the stored value, silently
// re-enabling a disabled job or unpinning a favorite on an unrelated edit.
func TestApplyLeavesUnsetFlagsAlone(t *testing.T) {
	want := storedJob()
	got := storedJob()
	if err := applyArgs(t, &got); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("apply with no flags changed the job:\n got %+v\nwant %+v", got, want)
	}
}

// The same guarantee, one flag at a time: naming one field must not disturb
// its neighbours.
func TestApplyOneFlagTouchesOnlyThatField(t *testing.T) {
	got := storedJob()
	if err := applyArgs(t, &got, "-favorite=false"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Favorite {
		t.Error("-favorite=false did not clear favorite")
	}
	want := storedJob()
	want.Favorite = false
	if !reflect.DeepEqual(got, want) {
		t.Errorf("editing favorite disturbed another field:\n got %+v\nwant %+v", got, want)
	}
}

func TestApplySetsNamedFields(t *testing.T) {
	cases := []struct {
		name  string
		argv  []string
		check func(store.Job) error
	}{
		{"name", []string{"-name", "Renamed"}, func(j store.Job) error {
			return eq("name", j.Name, "Renamed")
		}},
		{"description", []string{"-description", "why"}, func(j store.Job) error {
			return eq("description", j.Description, "why")
		}},
		{"prompt", []string{"-prompt", "new prompt"}, func(j store.Job) error {
			return eq("prompt", j.Prompt, "new prompt")
		}},
		{"cwd", []string{"-cwd", "/tmp/elsewhere"}, func(j store.Job) error {
			return eq("cwd", j.CWD, "/tmp/elsewhere")
		}},
		{"kind", []string{"-kind", "codex"}, func(j store.Job) error {
			return eq("kind", j.Kind, "codex")
		}},
		{"permission-mode", []string{"-permission-mode", "plan"}, func(j store.Job) error {
			return eq("permission-mode", j.PermissionMode, "plan")
		}},
		{"allowed-tools", []string{"-allowed-tools", "Read"}, func(j store.Job) error {
			return eq("allowed-tools", j.AllowedTools, "Read")
		}},
		{"disallowed-tools", []string{"-disallowed-tools", "Write"}, func(j store.Job) error {
			return eq("disallowed-tools", j.DisallowedTools, "Write")
		}},
		{"extra-args", []string{"-extra-args", "--foo bar"}, func(j store.Job) error {
			return eq("extra-args", j.ExtraArgs, "--foo bar")
		}},
		{"max-budget-usd", []string{"-max-budget-usd", "9"}, func(j store.Job) error {
			return eq("max-budget-usd", j.MaxBudgetUSD, "9")
		}},
		{"skip-permissions", []string{"-skip-permissions=false"}, func(j store.Job) error {
			return eq("skip-permissions", j.SkipPermissions, false)
		}},
		{"enabled", []string{"-enabled=true"}, func(j store.Job) error {
			return eq("enabled", j.Enabled, true)
		}},
		{"persistent", []string{"-persistent=false"}, func(j store.Job) error {
			return eq("persistent", j.Persistent, false)
		}},
		{"timeout", []string{"-timeout", "90s"}, func(j store.Job) error {
			return eq("timeout", j.Timeout, 90*time.Second)
		}},
		{"catchup", []string{"-catchup", "all"}, func(j store.Job) error {
			return eq("catchup", j.Catchup, store.CatchupAll)
		}},
		// Tags and add-dir are lists on the job but strings on the command
		// line, so the split has to happen here or the job stores one tag
		// literally named "a,b".
		{"tags", []string{"-tags", "Ops, Nightly ,"}, func(j store.Job) error {
			return eqList("tags", j.Tags, []string{"ops", "nightly"})
		}},
		{"add-dir", []string{"-add-dir", "/a, /b ,"}, func(j store.Job) error {
			return eqList("add-dir", j.AddDirs, []string{"/a", "/b"})
		}},
		// Clearing a list field has to be reachable: an empty value means no
		// tags, not "leave the old ones".
		{"tags cleared", []string{"-tags", ""}, func(j store.Job) error {
			return eqList("tags", j.Tags, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := storedJob()
			if err := applyArgs(t, &j, tc.argv...); err != nil {
				t.Fatalf("apply %v: %v", tc.argv, err)
			}
			if err := tc.check(j); err != nil {
				t.Error(err)
			}
		})
	}
}

// An explicitly empty --model resolves to bermuda's default rather than to
// whatever model the agent would pick on its own. A caller that clears the
// field still gets a job whose model is knowable from the row.
func TestApplyEmptyModelFallsBackToDefault(t *testing.T) {
	for _, arg := range []string{"", "   "} {
		j := storedJob()
		if err := applyArgs(t, &j, "-model", arg); err != nil {
			t.Fatalf("apply -model %q: %v", arg, err)
		}
		if j.Model != store.DefaultModel {
			t.Errorf("-model %q gave model %q, want %q", arg, j.Model, store.DefaultModel)
		}
	}
	j := storedJob()
	if err := applyArgs(t, &j, "-model", "haiku"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if j.Model != "haiku" {
		t.Errorf("model = %q, want haiku", j.Model)
	}
}

// The schedule type is inferred from whichever trigger was named, so a caller
// never has to state it twice. Getting this wrong means a job that says it is
// cron and fires on an interval, or one that never fires at all.
func TestApplyInfersScheduleFromTrigger(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want store.ScheduleType
		then func(store.Job) error
	}{
		{"cron", []string{"-cron", "0 6 * * *"}, store.ScheduleCron, func(j store.Job) error {
			return eq("cron expr", j.CronExpr, "0 6 * * *")
		}},
		{"interval", []string{"-interval", "2h"}, store.ScheduleInterval, func(j store.Job) error {
			return eq("interval seconds", j.IntervalSeconds, 7200)
		}},
		{"at", []string{"-at", "2026-08-01 07:30"}, store.ScheduleOnce, func(j store.Job) error {
			want := time.Date(2026, 8, 1, 7, 30, 0, 0, time.Local)
			if j.RunAt == nil || !j.RunAt.Equal(want) {
				return errf("run at = %v, want %v", j.RunAt, want)
			}
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// From a manual job, so the schedule can only have come from the
			// flag under test.
			j := store.Job{Catchup: store.CatchupLatest, Schedule: store.ScheduleManual}
			if err := applyArgs(t, &j, tc.argv...); err != nil {
				t.Fatalf("apply %v: %v", tc.argv, err)
			}
			if j.Schedule != tc.want {
				t.Errorf("schedule = %q, want %q", j.Schedule, tc.want)
			}
			if err := tc.then(j); err != nil {
				t.Error(err)
			}
		})
	}
}

// An explicit --schedule is the last word, so a job can be taken off its
// trigger without also having to unset it.
func TestApplyExplicitScheduleWinsOverInferred(t *testing.T) {
	j := storedJob()
	if err := applyArgs(t, &j, "-cron", "0 8 * * *", "-schedule", "manual"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if j.Schedule != store.ScheduleManual {
		t.Errorf("schedule = %q, want manual", j.Schedule)
	}
	if j.CronExpr != "0 8 * * *" {
		t.Errorf("cron expr = %q, want the value that was given", j.CronExpr)
	}
}

// Switching a cron job to an interval must actually switch it, not leave a job
// carrying two triggers with the scheduler free to pick either.
func TestApplyRetriggeringReplacesTheSchedule(t *testing.T) {
	j := storedJob() // cron
	if err := applyArgs(t, &j, "-interval", "15m"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if j.Schedule != store.ScheduleInterval {
		t.Errorf("schedule = %q, want interval", j.Schedule)
	}
	if j.IntervalSeconds != 900 {
		t.Errorf("interval seconds = %d, want 900", j.IntervalSeconds)
	}
}

// A schedule that names no trigger is rejected at the point of edit. The
// alternative is a job that is stored looking scheduled and never fires, which
// nothing downstream reports as wrong.
func TestApplyRejectsScheduleWithoutItsTrigger(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"cron without expression", []string{"-schedule", "cron"}, "--cron"},
		{"interval without duration", []string{"-schedule", "interval"}, "--interval"},
		{"once without time", []string{"-schedule", "once"}, "--at"},
		{"unknown schedule type", []string{"-schedule", "hourly"}, "unknown schedule type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := store.Job{Catchup: store.CatchupLatest}
			err := applyArgs(t, &j, tc.argv...)
			if err == nil {
				t.Fatalf("apply %v returned no error", tc.argv)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A sub-second interval truncates to zero seconds, which as a schedule is a
// job that is due every time the daemon looks. It has to be an error, not a
// stored zero.
func TestApplyRejectsSubSecondInterval(t *testing.T) {
	j := store.Job{Catchup: store.CatchupLatest}
	err := applyArgs(t, &j, "-interval", "500ms")
	if err == nil {
		t.Fatal("a 500ms interval was accepted; it truncates to a 0s schedule")
	}
	if !strings.Contains(err.Error(), "--interval") {
		t.Errorf("error %q does not mention --interval", err)
	}
}

func TestApplyRejectsUnparseableAt(t *testing.T) {
	j := store.Job{Catchup: store.CatchupLatest}
	err := applyArgs(t, &j, "-at", "tomorrow-ish")
	if err == nil {
		t.Fatal("apply accepted an unparseable --at")
	}
	if !strings.Contains(err.Error(), "cannot parse time") {
		t.Errorf("error %q does not name the problem", err)
	}
}

func TestApplyRejectsUnknownCatchup(t *testing.T) {
	j := store.Job{Catchup: store.CatchupLatest}
	err := applyArgs(t, &j, "-catchup", "sometimes")
	if err == nil {
		t.Fatal("apply accepted an unknown catchup policy")
	}
	if !strings.Contains(err.Error(), "--catchup") {
		t.Errorf("error %q does not mention --catchup", err)
	}
}

// A job is one prompt or one flow. A flow's steps carry their own prompts, so
// a job-level prompt alongside a flow is text nothing would ever send -- the
// caller has to be told rather than have one half quietly ignored.
func TestApplyRejectsPromptAndFlowTogether(t *testing.T) {
	j := store.Job{Catchup: store.CatchupLatest}
	err := applyArgs(t, &j, "-prompt", "do it", "-flow", "nightly")
	if err == nil {
		t.Fatal("apply accepted both --prompt and --flow")
	}
	if !strings.Contains(err.Error(), "--flow") {
		t.Errorf("error %q does not name the conflict", err)
	}

	// Converting an existing prompt job to a flow means clearing the prompt in
	// the same edit; that has to be allowed.
	j = storedJob()
	j.Schedule = store.ScheduleManual
	if err := applyArgs(t, &j, "-flow", "nightly", "-prompt", ""); err != nil {
		t.Fatalf("clearing the prompt while setting a flow: %v", err)
	}
	if j.Flow != "nightly" || j.Prompt != "" {
		t.Errorf("flow = %q prompt = %q, want nightly and empty", j.Flow, j.Prompt)
	}
}

func TestParseWhenAcceptsItsDocumentedLayouts(t *testing.T) {
	want := time.Date(2026, 8, 1, 7, 30, 0, 0, time.Local)
	cases := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"local minutes", "2026-08-01 07:30", want},
		{"local seconds", "2026-08-01 07:30:00", want},
		{"surrounding space", "  2026-08-01 07:30  ", want},
		{"rfc3339", want.Format(time.RFC3339), want},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWhen(tc.input)
			if err != nil {
				t.Fatalf("parseWhen(%q): %v", tc.input, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseWhen(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// A bare date or a US-style time is a plausible thing to type and would fire at
// the wrong moment if it were quietly reinterpreted, so it must be refused.
func TestParseWhenRejectsOtherFormats(t *testing.T) {
	for _, in := range []string{"", "2026-08-01", "08/01/2026 07:30", "7:30pm", "2026-13-01 07:30"} {
		if got, err := parseWhen(in); err == nil {
			t.Errorf("parseWhen(%q) = %v, want an error", in, got)
		}
	}
}

func TestSplitListDropsBlanksAndTrims(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty is no entries", "", nil},
		{"only separators is no entries", ",,, ,", nil},
		{"trims each entry", " /a , /b ", []string{"/a", "/b"}},
		{"keeps a single entry", "/only", []string{"/only"}},
		{"keeps interior spaces", "/a b,/c", []string{"/a b", "/c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := eqList("splitList", splitList(tc.in), tc.want); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestStepsLabelNamesTheFlowOrNothing(t *testing.T) {
	if got := stepsLabel(store.Job{Prompt: "p"}); got != "-" {
		t.Errorf("a prompt job labelled %q, want -", got)
	}
	if got := stepsLabel(store.Job{Flow: "nightly"}); got != "nightly" {
		t.Errorf("a flow job labelled %q, want nightly", got)
	}
}

func eq[T comparable](what string, got, want T) error {
	if got != want {
		return errf("%s = %v, want %v", what, got, want)
	}
	return nil
}

func eqList(what string, got, want []string) error {
	if !reflect.DeepEqual(got, want) {
		return errf("%s = %v, want %v", what, got, want)
	}
	return nil
}

func errf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
