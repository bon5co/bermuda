package board

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// fieldKind decides which editor a field gets.
type fieldKind int

const (
	fieldText     fieldKind = iota // single-line
	fieldTextArea                  // multi-line
	fieldBool                      // toggled in place, no editor
	fieldChoice                    // cycled through options in place
)

// field describes one editable property of a job.
//
// Each field owns its own parsing and formatting so the editor stays generic
// and a bad value is rejected at the field, with a message naming that field,
// rather than failing the whole save with one opaque error.
type field struct {
	key     string
	label   string
	kind    fieldKind
	options []string // for fieldChoice
	help    string

	get func(*store.Job) string
	set func(*store.Job, string) error
}

// jobFields is the editable surface of a job, in display order.
func jobFields() []field {
	return []field{
		{
			key: "name", label: "Name", kind: fieldText,
			get: func(j *store.Job) string { return j.Name },
			set: func(j *store.Job, v string) error { j.Name = v; return nil },
		},
		{
			key: "description", label: "Description", kind: fieldText,
			get: func(j *store.Job) string { return j.Description },
			set: func(j *store.Job, v string) error { j.Description = v; return nil },
		},
		{
			key: "tags", label: "Tags", kind: fieldText,
			help: "comma-delimited, e.g. marketing,daily",
			get:  func(j *store.Job) string { return strings.Join(j.Tags, ", ") },
			set: func(j *store.Job, v string) error {
				j.Tags = store.SplitTags(v)
				return nil
			},
		},
		{
			key: "prompt", label: "Prompt", kind: fieldTextArea,
			help: "what the agent is told to do",
			get:  func(j *store.Job) string { return j.Prompt },
			set: func(j *store.Job, v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("prompt cannot be empty")
				}
				j.Prompt = v
				return nil
			},
		},
		{
			key: "schedule", label: "Schedule", kind: fieldChoice,
			options: []string{"manual", "cron", "interval", "once"},
			get:     func(j *store.Job) string { return string(j.Schedule) },
			set: func(j *store.Job, v string) error {
				j.Schedule = store.ScheduleType(v)
				return nil
			},
		},
		{
			key: "cron", label: "Cron", kind: fieldText,
			help: "5-field cron, e.g. 0 7 * * *",
			get:  func(j *store.Job) string { return j.CronExpr },
			set:  func(j *store.Job, v string) error { j.CronExpr = strings.TrimSpace(v); return nil },
		},
		{
			key: "interval", label: "Interval", kind: fieldText,
			help: "e.g. 1h, 30m",
			get: func(j *store.Job) string {
				if j.IntervalSeconds == 0 {
					return ""
				}
				return (time.Duration(j.IntervalSeconds) * time.Second).String()
			},
			set: func(j *store.Job, v string) error {
				v = strings.TrimSpace(v)
				if v == "" {
					j.IntervalSeconds = 0
					return nil
				}
				d, err := time.ParseDuration(v)
				if err != nil {
					return fmt.Errorf("interval: %w", err)
				}
				j.IntervalSeconds = int(d.Seconds())
				return nil
			},
		},
		{
			key: "run_at", label: "Run at", kind: fieldText,
			help: "one-shot time: 2006-01-02 15:04",
			get: func(j *store.Job) string {
				if j.RunAt == nil {
					return ""
				}
				return j.RunAt.Format("2006-01-02 15:04")
			},
			set: func(j *store.Job, v string) error {
				v = strings.TrimSpace(v)
				if v == "" {
					j.RunAt = nil
					return nil
				}
				t, err := time.ParseInLocation("2006-01-02 15:04", v, time.Local)
				if err != nil {
					return fmt.Errorf("run at: use 2006-01-02 15:04")
				}
				j.RunAt = &t
				return nil
			},
		},
		{
			key: "catchup", label: "Catchup", kind: fieldChoice,
			options: []string{store.CatchupLatest, store.CatchupAll, store.CatchupSkip},
			get:     func(j *store.Job) string { return j.Catchup },
			set:     func(j *store.Job, v string) error { j.Catchup = v; return nil },
		},
		{
			key: "timeout", label: "Timeout", kind: fieldText,
			help: "e.g. 15m",
			get:  func(j *store.Job) string { return j.Timeout.String() },
			set: func(j *store.Job, v string) error {
				d, err := time.ParseDuration(strings.TrimSpace(v))
				if err != nil {
					return fmt.Errorf("timeout: %w", err)
				}
				j.Timeout = d
				return nil
			},
		},
		{
			key: "cwd", label: "Working dir", kind: fieldText,
			get: func(j *store.Job) string { return j.CWD },
			set: func(j *store.Job, v string) error {
				v = strings.TrimSpace(v)
				if v == "" {
					return fmt.Errorf("working dir cannot be empty")
				}
				j.CWD = v
				return nil
			},
		},
		{
			key: "kind", label: "Agent", kind: fieldText,
			get: func(j *store.Job) string { return j.Kind },
			set: func(j *store.Job, v string) error { j.Kind = strings.TrimSpace(v); return nil },
		},
		{
			key: "model", label: "Model", kind: fieldText,
			help: "sonnet or opus; blank falls back to " + store.DefaultModel,
			get:  func(j *store.Job) string { return j.Model },
			set: func(j *store.Job, v string) error {
				// Never left empty: a job must run on a model it names.
				if v = strings.TrimSpace(v); v == "" {
					j.Model = store.DefaultModel
				} else {
					j.Model = v
				}
				return nil
			},
		},
		{
			key: "permission_mode", label: "Permissions", kind: fieldChoice,
			options: []string{"acceptEdits", "default", "plan", "bypassPermissions", ""},
			get:     func(j *store.Job) string { return j.PermissionMode },
			set:     func(j *store.Job, v string) error { j.PermissionMode = v; return nil },
		},
		{
			key: "skip_permissions", label: "Skip permissions", kind: fieldBool,
			help: "no permission prompts — the default, since jobs run unattended",
			get:  func(j *store.Job) string { return boolStr(j.SkipPermissions) },
			set:  func(j *store.Job, v string) error { j.SkipPermissions = v == "true"; return nil },
		},
		{
			key: "allowed_tools", label: "Allowed tools", kind: fieldText,
			get: func(j *store.Job) string { return j.AllowedTools },
			set: func(j *store.Job, v string) error { j.AllowedTools = strings.TrimSpace(v); return nil },
		},
		{
			key: "disallowed_tools", label: "Denied tools", kind: fieldText,
			get: func(j *store.Job) string { return j.DisallowedTools },
			set: func(j *store.Job, v string) error { j.DisallowedTools = strings.TrimSpace(v); return nil },
		},
		{
			key: "add_dirs", label: "Add dirs", kind: fieldText,
			help: "comma-separated absolute paths",
			get:  func(j *store.Job) string { return strings.Join(j.AddDirs, ", ") },
			set: func(j *store.Job, v string) error {
				var out []string
				for _, p := range strings.Split(v, ",") {
					if p = strings.TrimSpace(p); p != "" {
						out = append(out, p)
					}
				}
				j.AddDirs = out
				return nil
			},
		},
		{
			key: "extra_args", label: "Extra args", kind: fieldText,
			get: func(j *store.Job) string { return j.ExtraArgs },
			set: func(j *store.Job, v string) error { j.ExtraArgs = strings.TrimSpace(v); return nil },
		},
		{
			key: "max_budget", label: "Budget USD", kind: fieldText,
			get: func(j *store.Job) string { return j.MaxBudgetUSD },
			set: func(j *store.Job, v string) error {
				v = strings.TrimSpace(v)
				if v != "" {
					if _, err := strconv.ParseFloat(v, 64); err != nil {
						return fmt.Errorf("budget: must be a number")
					}
				}
				j.MaxBudgetUSD = v
				return nil
			},
		},
		{
			key: "autocompact", label: "Auto-compact", kind: fieldText,
			get: func(j *store.Job) string { return j.AutoCompact },
			set: func(j *store.Job, v string) error {
				// The agent's own bounds, enforced where somebody is typing
				// rather than in an unattended 04:00 run.
				v = strings.TrimSpace(v)
				if v == "" || v == "auto" {
					j.AutoCompact = v
					return nil
				}
				n, err := strconv.Atoi(v)
				if err != nil {
					return fmt.Errorf("auto-compact: \"auto\" or a token count like 200000")
				}
				if n < 100_000 || n > 1_000_000 {
					return fmt.Errorf("auto-compact: between 100000 and 1000000 tokens")
				}
				j.AutoCompact = v
				return nil
			},
		},
		{
			key: "enabled", label: "Enabled", kind: fieldBool,
			get: func(j *store.Job) string { return boolStr(j.Enabled) },
			set: func(j *store.Job, v string) error { j.Enabled = v == "true"; return nil },
		},
		{
			key: "favorite", label: "Favorite", kind: fieldBool,
			get: func(j *store.Job) string { return boolStr(j.Favorite) },
			set: func(j *store.Job, v string) error { j.Favorite = v == "true"; return nil },
		},
		{
			key: "persistent", label: "Persistent agent", kind: fieldBool,
			help: "reuse one agent per run; context cleared each time",
			get:  func(j *store.Job) string { return boolStr(j.Persistent) },
			set:  func(j *store.Job, v string) error { j.Persistent = v == "true"; return nil },
		},
	}
}

// validateJob checks the cross-field rules the individual setters cannot see.
func validateJob(j store.Job) error {
	switch j.Schedule {
	case store.ScheduleCron:
		if strings.TrimSpace(j.CronExpr) == "" {
			return fmt.Errorf("schedule is cron but no cron expression is set")
		}
	case store.ScheduleInterval:
		if j.IntervalSeconds <= 0 {
			return fmt.Errorf("schedule is interval but no interval is set")
		}
	case store.ScheduleOnce:
		if j.RunAt == nil {
			return fmt.Errorf("schedule is once but no run-at time is set")
		}
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return fmt.Errorf("prompt cannot be empty")
	}
	if strings.TrimSpace(j.CWD) == "" {
		return fmt.Errorf("working dir cannot be empty")
	}
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
