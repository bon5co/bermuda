package runner

import (
	"strings"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// FromStore converts a stored job into a runnable job.
//
// Agent flags are modelled as fields in the store and assembled here, so the
// board can display them and the daemon can validate them, rather than every
// caller hand-assembling argv.
func FromStore(j store.Job) Job {
	return Job{
		ID:         j.ID,
		Prompt:     j.Prompt,
		CWD:        j.CWD,
		Kind:       j.Kind,
		Timeout:    j.Timeout,
		AgentArgs:  BuildAgentArgs(j),
		Persistent: j.Persistent,
	}
}

// StepJob converts one flow step into a runnable job.
//
// The step inherits everything the job settled — working directory, timeout,
// permissions — and overrides only what it names. It is never persistent: each
// agent step is its own process, so the step that reviews something is not the
// agent that wrote it, and a step cannot be handed the previous step's context
// under a different charter.
func StepJob(j store.Job, s store.Step) Job {
	return Job{
		ID:         j.ID + "-" + s.ID,
		Prompt:     s.Agent,
		CWD:        j.CWD,
		Kind:       firstNonEmpty(s.Kind, j.Kind),
		Timeout:    j.Timeout,
		AgentArgs:  BuildStepArgs(j, s),
		Persistent: false,
	}
}

// BuildStepArgs assembles a step's agent arguments: the job's, with the step's
// overrides applied.
func BuildStepArgs(j store.Job, s store.Step) []string {
	cfg := j
	if m := strings.TrimSpace(s.Model); m != "" {
		cfg.Model = m
	}
	if k := strings.TrimSpace(s.Kind); k != "" {
		cfg.Kind = k
	}
	// A step may take the permission bypass back. The default is on, because a
	// flow step has nobody in its pane to answer a prompt — but a step that
	// touches something consequential should be able to say "not me", and the
	// only place that can be said is next to the step itself.
	if s.SkipPermissions != nil {
		cfg.SkipPermissions = *s.SkipPermissions
	}
	args := BuildAgentArgs(cfg)
	if cfg.Kind != "" && cfg.Kind != "claude" {
		// Only claude's flag spellings are modelled; another kind gets its
		// passthrough args rather than flags invented for it.
		return args
	}
	if e := strings.TrimSpace(s.Effort); e != "" {
		args = append(args, "--effort", e)
	}
	if a := strings.TrimSpace(s.Subagent); a != "" {
		// A named agent definition from ~/.claude/agents. This is a different
		// axis from starting a fresh agent: fresh gives the next step amnesia,
		// a subagent gives it a different job description.
		args = append(args, "--agent", a)
	}
	return args
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// BuildAgentArgs assembles the agent's command-line arguments from a job.
//
// Only claude's flag spellings are modelled today; other agent kinds get their
// passthrough args untouched rather than flags invented for them.
func BuildAgentArgs(j store.Job) []string {
	if j.Kind != "" && j.Kind != "claude" {
		return splitArgs(j.ExtraArgs)
	}
	var args []string
	// Always explicit: a job that names no model still runs on a stated one
	// rather than inheriting whatever the agent picks today.
	model := strings.TrimSpace(j.Model)
	if model == "" {
		model = store.DefaultModel
	}
	args = append(args, "--model", model)
	// A permission mode and a blanket bypass are mutually exclusive; the
	// bypass wins because it is the more explicit request.
	if j.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else if j.PermissionMode != "" {
		args = append(args, "--permission-mode", j.PermissionMode)
	}
	if t := strings.TrimSpace(j.AllowedTools); t != "" {
		args = append(args, "--allowedTools", t)
	}
	if t := strings.TrimSpace(j.DisallowedTools); t != "" {
		args = append(args, "--disallowedTools", t)
	}
	for _, d := range j.AddDirs {
		if d = strings.TrimSpace(d); d != "" {
			args = append(args, "--add-dir", d)
		}
	}
	if j.MaxBudgetUSD != "" {
		args = append(args, "--max-budget-usd", j.MaxBudgetUSD)
	}
	// Emitted before the passthrough so that a job which also spells
	// --autocompact into --extra-args still wins: claude takes the last
	// occurrence, and an explicit passthrough is the more specific request.
	if a := strings.TrimSpace(j.AutoCompact); a != "" {
		args = append(args, "--autocompact", a)
	}
	return append(args, splitArgs(j.ExtraArgs)...)
}

// splitArgs accepts either newline-separated arguments or a single
// space-separated line, matching how the flags are typed in practice.
func splitArgs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.Contains(raw, "\n") {
		var out []string
		for _, line := range strings.Split(raw, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
		return out
	}
	return strings.Fields(raw)
}
