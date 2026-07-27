package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bon5co/bermuda/internal/flow"
	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/runner"
	"github.com/bon5co/bermuda/internal/store"
)

// jobFlags is the full editable surface of a job, shared by add and edit so
// the two cannot drift apart.
type jobFlags struct {
	name        *string
	description *string
	tags        *string
	prompt      *string
	flowID      *string
	flowInput   *string
	cwd         *string
	kind        *string

	model           *string
	permissionMode  *string
	allowedTools    *string
	disallowedTools *string
	addDirs         *string
	extraArgs       *string
	skipPermissions *bool
	maxBudget       *string

	schedule *string
	interval *time.Duration
	cron     *string
	runAt    *string
	catchup  *string
	timeout  *time.Duration

	enabled    *bool
	favorite   *bool
	persistent *bool
}

func registerJobFlags(fs *flag.FlagSet) *jobFlags {
	return &jobFlags{
		name:        fs.String("name", "", "human-readable name"),
		description: fs.String("description", "", "what this job does and why"),
		tags:        fs.String("tags", "", "comma-delimited tags, e.g. marketing,daily"),
		prompt:      fs.String("prompt", "", "instruction for the agent"),
		flowID:      fs.String("flow", "", "id of a flow this job starts, instead of --prompt"),
		flowInput:   fs.String("input", "", "the input passed to that flow on every fire"),
		cwd:         fs.String("cwd", "", "working directory (default: current dir)"),
		kind:        fs.String("kind", store.DefaultKind, "herdr agent kind"),

		model:           fs.String("model", store.DefaultModel, "agent model, e.g. sonnet or opus"),
		permissionMode:  fs.String("permission-mode", defaultPermissionMode, "agent permission mode"),
		allowedTools:    fs.String("allowed-tools", "", "allowlist passed to the agent"),
		disallowedTools: fs.String("disallowed-tools", "", "denylist passed to the agent"),
		addDirs:         fs.String("add-dir", "", "extra accessible dirs, comma-separated"),
		extraArgs:       fs.String("extra-args", "", "raw passthrough args"),
		skipPermissions: fs.Bool("skip-permissions", true, "run with permission checks disabled (default: jobs are unattended)"),
		maxBudget:       fs.String("max-budget-usd", "", "per-run budget cap"),

		schedule: fs.String("schedule", "", "manual|interval|cron|once (inferred from --cron/--interval/--at)"),
		interval: fs.Duration("interval", 0, "run every interval, e.g. 1h"),
		cron:     fs.String("cron", "", "cron expression, e.g. '0 7 * * *'"),
		runAt:    fs.String("at", "", "one-shot run time, RFC3339 or '2006-01-02 15:04'"),
		catchup:  fs.String("catchup", "latest", "missed-fire policy: latest|all|skip"),
		timeout:  fs.Duration("timeout", 15*time.Minute, "run deadline"),

		enabled:    fs.Bool("enabled", true, "whether the job may run"),
		favorite:   fs.Bool("favorite", false, "pin to the top of the board"),
		persistent: fs.Bool("persistent", false, "reuse one agent across runs (context cleared each run)"),
	}
}

// apply writes the flags that were actually set onto j, so edit can change one
// field without resetting the rest to their defaults.
func (f *jobFlags) apply(fs *flag.FlagSet, j *store.Job) error {
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })

	assign := func(name string, fn func()) {
		if set[name] {
			fn()
		}
	}
	assign("name", func() { j.Name = *f.name })
	assign("description", func() { j.Description = *f.description })
	assign("tags", func() { j.Tags = store.SplitTags(*f.tags) })
	assign("prompt", func() { j.Prompt = *f.prompt })
	assign("cwd", func() { j.CWD = *f.cwd })
	assign("flow", func() { j.Flow = *f.flowID })
	assign("input", func() { j.Input = *f.flowInput })
	assign("kind", func() { j.Kind = *f.kind })
	assign("model", func() {
		// An explicit empty value still resolves to the stated default rather
		// than to the agent's own.
		if strings.TrimSpace(*f.model) == "" {
			j.Model = store.DefaultModel
		} else {
			j.Model = *f.model
		}
	})
	assign("permission-mode", func() { j.PermissionMode = *f.permissionMode })
	assign("allowed-tools", func() { j.AllowedTools = *f.allowedTools })
	assign("disallowed-tools", func() { j.DisallowedTools = *f.disallowedTools })
	assign("add-dir", func() { j.AddDirs = splitList(*f.addDirs) })
	assign("extra-args", func() { j.ExtraArgs = *f.extraArgs })
	assign("skip-permissions", func() { j.SkipPermissions = *f.skipPermissions })
	assign("max-budget-usd", func() { j.MaxBudgetUSD = *f.maxBudget })
	assign("catchup", func() { j.Catchup = *f.catchup })
	assign("timeout", func() { j.Timeout = *f.timeout })
	assign("enabled", func() { j.Enabled = *f.enabled })
	assign("favorite", func() { j.Favorite = *f.favorite })
	assign("persistent", func() { j.Persistent = *f.persistent })

	// Scheduling: the schedule type is inferred from whichever trigger was
	// given, so callers do not have to state it twice and cannot state it
	// inconsistently.
	if set["cron"] {
		j.CronExpr = *f.cron
		j.Schedule = store.ScheduleCron
	}
	if set["interval"] {
		j.IntervalSeconds = int(f.interval.Seconds())
		j.Schedule = store.ScheduleInterval
	}
	if set["at"] {
		t, err := parseWhen(*f.runAt)
		if err != nil {
			return err
		}
		j.RunAt = &t
		j.Schedule = store.ScheduleOnce
	}
	if set["schedule"] {
		j.Schedule = store.ScheduleType(*f.schedule)
	}

	switch j.Schedule {
	case store.ScheduleCron:
		if j.CronExpr == "" {
			return errors.New("cron schedule needs --cron")
		}
	case store.ScheduleInterval:
		if j.IntervalSeconds <= 0 {
			return errors.New("interval schedule needs --interval")
		}
	case store.ScheduleOnce:
		if j.RunAt == nil {
			return errors.New("once schedule needs --at")
		}
	case store.ScheduleManual, "":
		j.Schedule = store.ScheduleManual
	default:
		return fmt.Errorf("unknown schedule type %q", j.Schedule)
	}

	switch j.Catchup {
	case store.CatchupLatest, store.CatchupAll, store.CatchupSkip:
	default:
		return fmt.Errorf("--catchup must be latest, all, or skip")
	}

	// A job is one prompt or a list of steps, never both: a flow's steps
	// carry their own prompts, so a job-level one would be text nothing ever
	// sends.
	if j.IsFlow() && strings.TrimSpace(j.Prompt) != "" {
		return errors.New("a job has either --prompt or --flow, not both")
	}
	// The flow itself is not validated here. It is a file that anything can edit
	// after this job is written, so the only check that means anything happens
	// when the flow is read to run it.
	return nil
}

func jobAdd(argv []string) error {
	fs := flag.NewFlagSet("job add", flag.ExitOnError)
	id := fs.String("id", "", "job id (required)")
	f := registerJobFlags(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--id is required")
	}

	// Jobs run unattended, so permission prompts have nobody to answer them:
	// a job that stops to ask is a job that parks until a human notices.
	j := store.Job{ID: *id, Kind: store.DefaultKind, Enabled: true,
		SkipPermissions: true, Catchup: store.CatchupLatest,
		Model: store.DefaultModel, Timeout: 15 * time.Minute}
	if err := f.apply(fs, &j); err != nil {
		return err
	}
	if j.Prompt == "" && !j.IsFlow() {
		return errors.New("--prompt is required (or --flow to start a flow)")
	}
	if j.CWD == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		j.CWD = wd
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Add must not overwrite. The store upserts by id, so without this check a
	// mistyped or reused id silently replaces a live job's prompt and schedule
	// with no warning and no way back.
	exists, err := s.Exists(ctx, j.ID)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("job %s already exists; use `bermuda job edit %s` to change it", j.ID, j.ID)
	}
	taken, err := s.NameTaken(ctx, j.Name, j.ID)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("another job is already named %q", j.Name)
	}

	if err := s.PutJob(ctx, j); err != nil {
		return err
	}
	fmt.Printf("job %s saved (%s)\n", j.ID, j.ScheduleLabel())
	return nil
}

func jobEdit(argv []string) error {
	fs := flag.NewFlagSet("job edit", flag.ExitOnError)
	f := registerJobFlags(fs)
	if len(argv) == 0 {
		return errors.New("usage: bermuda job edit <id> [flags]")
	}
	id := argv[0]
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	j, err := s.Job(ctx, id)
	if err != nil {
		return err
	}
	if err := f.apply(fs, j); err != nil {
		return err
	}
	taken, err := s.NameTaken(ctx, j.Name, j.ID)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("another job is already named %q", j.Name)
	}
	if err := s.PutJob(ctx, *j); err != nil {
		return err
	}
	fmt.Printf("job %s updated (%s)\n", j.ID, j.ScheduleLabel())
	return nil
}

// jobShow prints a job's full configuration and recent runs.
func jobShow(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda job show <id>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	j, err := s.Job(ctx, argv[0])
	if err != nil {
		return err
	}
	runs, err := s.JobRuns(ctx, j.ID, 10)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	line := func(k string, v any) { fmt.Fprintf(w, "%s\t%v\n", k, v) }
	line("id", j.ID)
	if j.Name != "" {
		line("name", j.Name)
	}
	if j.Description != "" {
		line("description", j.Description)
	}
	if len(j.Tags) > 0 {
		line("tags", strings.Join(j.Tags, ", "))
	}
	line("schedule", j.ScheduleLabel())
	line("catchup", j.Catchup)
	line("enabled", j.Enabled)
	line("favorite", j.Favorite)
	line("persistent", j.Persistent)
	line("cwd", j.CWD)
	line("kind", j.Kind)
	line("timeout", j.Timeout)
	line("model", j.Model)
	if j.PermissionMode != "" {
		line("permission-mode", j.PermissionMode)
	}
	if j.SkipPermissions {
		line("skip-permissions", true)
	}
	if j.AllowedTools != "" {
		line("allowed-tools", j.AllowedTools)
	}
	if j.DisallowedTools != "" {
		line("disallowed-tools", j.DisallowedTools)
	}
	if len(j.AddDirs) > 0 {
		line("add-dir", strings.Join(j.AddDirs, ", "))
	}
	if j.ExtraArgs != "" {
		line("extra-args", j.ExtraArgs)
	}
	if j.MaxBudgetUSD != "" {
		line("max-budget-usd", j.MaxBudgetUSD)
	}
	line("agent argv", strings.Join(runner.BuildAgentArgs(*j), " "))
	if err := w.Flush(); err != nil {
		return err
	}

	if j.IsFlow() {
		// The steps are read from the flow file rather than from the job, so what
		// is printed here is what would actually run if the job fired now. A
		// missing or broken flow is worth saying plainly: the job looks fine and
		// will fail at its next fire.
		def, err := flow.Load(flowDir(), j.Flow)
		if err != nil {
			fmt.Printf("\nflow %s: %v\n", j.Flow, err)
			return nil
		}
		fmt.Printf("\nflow %s (%s)\n", def.ID, def.Path)
		if def.TakesInput() {
			fmt.Printf("input\t%s\n", def.Input)
		}
		fmt.Println("\nsteps:")
		sw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(sw, "#\tSTEP\tKIND\tMODEL\tAGENT/COMMAND")
		for i, st := range def.Steps {
			what := st.Run
			if st.IsAgent() {
				what = st.Agent
			}
			model := ""
			if st.IsAgent() {
				model = j.EffectiveModel(st)
				if st.Subagent != "" {
					model += " " + st.Subagent
				}
			}
			fmt.Fprintf(sw, "%d\t%s\t%s\t%s\t%s\n", i+1, st.ID, st.Label(),
				model, firstLine(what))
		}
		if err := sw.Flush(); err != nil {
			return err
		}
	} else {
		fmt.Printf("\nprompt:\n%s\n", j.Prompt)
	}

	if len(runs) == 0 {
		fmt.Println("\nno runs yet")
		return nil
	}
	fmt.Println("\nrecent runs:")
	rw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(rw, "RUN\tTRIGGER\tOUTCOME\tREASON\tDURATION\tNOTE")
	for _, r := range runs {
		fmt.Fprintf(rw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Trigger, r.Outcome,
			r.ParkReason, r.Duration().Round(time.Second), r.Note)
	}
	return rw.Flush()
}

func jobList(argv []string) error {
	fs := flag.NewFlagSet("job list", flag.ExitOnError)
	tag := fs.String("tag", "", "only jobs carrying this tag")
	all := fs.Bool("all", false, "include finished one-shots, which are hidden by default")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return listJobs(context.Background(), s, os.Stdout, *tag, *all)
}

// listJobs is `job list` with its store and output passed in, so the hiding
// rule can be tested without a process.
func listJobs(ctx context.Context, s *store.Store, out io.Writer, tag string, all bool) error {
	jobs, err := s.Jobs(ctx)
	if err != nil {
		return err
	}
	if tag != "" {
		var kept []store.Job
		for _, j := range jobs {
			if j.HasTag(tag) {
				kept = append(kept, j)
			}
		}
		jobs = kept
	}
	// A one-shot that ran to completion can never fire again, so it is put away
	// unless asked for -- but never silently: the count below says what is
	// missing, because a job that vanishes without a word looks deleted.
	finished := map[string]bool{}
	hidden := 0
	for _, j := range jobs {
		finished[j.ID] = j.Finished(lastRunOf(ctx, s, j.ID))
	}
	if !all {
		var kept []store.Job
		for _, j := range jobs {
			if finished[j.ID] {
				hidden++
				continue
			}
			kept = append(kept, j)
		}
		jobs = kept
	}
	if len(jobs) == 0 {
		fmt.Fprintln(out, "no jobs")
		if hidden > 0 {
			fmt.Fprintf(out, "%d finished hidden (use --all to show)\n", hidden)
		}
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	// STEPS says which jobs are flows and how long they are. A one-prompt
	// job shows a dash rather than a blank, so an empty column reads as "one
	// prompt" instead of as missing data.
	fmt.Fprintln(w, "ID\tNAME\tTAGS\tSTEPS\tSCHEDULE\tENABLED\tLAST")
	for _, j := range jobs {
		last := "never"
		if r, err := s.LastRun(ctx, j.ID); err == nil {
			last = r.Outcome
			if r.ParkReason != "" {
				last += " (" + r.ParkReason + ")"
			}
		}
		star := ""
		if j.Favorite {
			star = "* "
		}
		name := j.Name
		if finished[j.ID] {
			name += " (finished)"
		}
		fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\t%s\t%t\t%s\n", star, j.ID, name,
			strings.Join(j.Tags, ","), stepsLabel(j), j.ScheduleLabel(),
			j.Enabled, last)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if hidden > 0 {
		fmt.Fprintf(out, "%d finished hidden (use --all to show)\n", hidden)
	}
	return nil
}

// lastRunOf is a job's most recent run, or nil when it has never run or the
// store cannot say. Both answers mean the same thing to the finished rule: no
// evidence the job completed, so it stays visible.
func lastRunOf(ctx context.Context, s *store.Store, id string) *store.Run {
	r, err := s.LastRun(ctx, id)
	if err != nil {
		return nil
	}
	return r
}

// jobPrune deletes finished one-shots, and by default deletes nothing at all.
//
// This is the only command in bermuda that destroys configuration, so the dry
// run is the default and --yes is the whole of the confirmation: the one time a
// job was lost in this codebase it was to a write nobody asked for. Runs are
// left alone -- deleting a job already keeps its history -- so pruning loses
// the schedule, not the record of what happened.
func jobPrune(argv []string) error {
	fs := flag.NewFlagSet("job prune", flag.ExitOnError)
	yes := fs.Bool("yes", false, "actually delete; without this nothing is removed")
	// --once is the only filter there is, and also what happens with no filter
	// at all. It exists so the command can be typed as what it means.
	fs.Bool("once", true, "restrict to finished one-shots (the default)")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return pruneJobs(context.Background(), s, os.Stdout, *yes)
}

// pruneJobs is `job prune` with its store and output passed in, so both halves
// of it -- the dry run that must delete nothing, and the deletion that must
// keep the runs -- can be tested against a real database.
func pruneJobs(ctx context.Context, s *store.Store, out io.Writer, yes bool) error {
	jobs, err := s.Jobs(ctx)
	if err != nil {
		return err
	}

	var doomed []store.Job
	for _, j := range jobs {
		// Recurring jobs are never touched, whatever their last run says.
		if j.Finished(lastRunOf(ctx, s, j.ID)) {
			doomed = append(doomed, j)
		}
	}
	if len(doomed) == 0 {
		fmt.Fprintln(out, "nothing to prune")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSCHEDULE")
	for _, j := range doomed {
		fmt.Fprintf(w, "%s\t%s\t%s\n", j.ID, j.Name, j.ScheduleLabel())
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if !yes {
		fmt.Fprintf(out, "%d finished one-shot(s) would be deleted; nothing removed. Re-run with --yes.\n",
			len(doomed))
		return nil
	}
	for _, j := range doomed {
		if err := s.DeleteJob(ctx, j.ID); err != nil {
			return fmt.Errorf("delete %s: %w", j.ID, err)
		}
	}
	fmt.Fprintf(out, "%d finished one-shot(s) deleted; their runs are kept\n", len(doomed))
	return nil
}

func jobEnable(argv []string, enabled bool) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda job pause|resume <id>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.SetEnabled(context.Background(), argv[0], enabled); err != nil {
		return err
	}
	state := "paused"
	if enabled {
		state = "resumed"
	}
	fmt.Printf("job %s %s\n", argv[0], state)
	return nil
}

func jobRemove(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda job remove <id>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.DeleteJob(context.Background(), argv[0]); err != nil {
		return err
	}
	fmt.Printf("job %s removed\n", argv[0])
	return nil
}

// jobRun executes a stored job immediately.
func jobRun(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda job run <id>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	j, err := s.Job(ctx, argv[0])
	if err != nil {
		return err
	}
	run, execErr := Execute(ctx, s, *j, "manual")
	if run != nil {
		printRun(run)
	}
	if execErr != nil {
		return execErr
	}
	// A run that parked or failed is not an error — nothing went wrong with the
	// command — but it is not success either, and a script driving `job run`
	// has no other way to tell. `run-once` and `flow run` have always
	// exited 1 here; this one exited 0 because a failed run leaves execErr nil.
	exitUnlessDone(run)
	return nil
}

// stepsLabel says which flow a job starts, or "-" when it is a plain prompt.
//
// The flow id rather than a step count: the count lives in a file this column
// would have to open to know, and the id is the more useful answer anyway --
// it is what `bermuda flow show <id>` takes.
func stepsLabel(j store.Job) string {
	if !j.IsFlow() {
		return "-"
	}
	return j.Flow
}

// firstLine is a prompt reduced to what fits in a table cell.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	// By runes, not bytes: a prompt in any script that is not ASCII would
	// otherwise be cut mid-character and print a replacement glyph.
	if r := []rune(s); len(r) > 60 {
		s = string(r[:59]) + "…"
	}
	return s
}

// Execute runs a stored job and persists the result.
//
// A flow takes a different path: its steps are run in series, each in its
// own process, and the run row it produces is the sequence's, not one agent's.
// Everything that launches a job — the daemon, the board, `job run` — arrives
// here, so a flow cannot be started by accident as a single empty prompt.
func Execute(ctx context.Context, s *store.Store, j store.Job, trigger string) (*runner.Run, error) {
	if j.IsFlow() {
		runID := newRunID(j.ID)
		run, err := runFlow(ctx, s, j, store.Run{
			ID: runID, JobID: j.ID, Trigger: trigger, RunDir: runDirFor(runID),
			Outcome: "running", StartedAt: time.Now(),
		})
		disableOneShot(ctx, s, j, run)
		return run, err
	}
	return executePrompt(ctx, s, j, trigger)
}

// disableOneShot stops a one-shot job from firing a second time once it has
// actually run.
func disableOneShot(ctx context.Context, s *store.Store, j store.Job, run *runner.Run) {
	if j.Schedule != store.ScheduleOnce || run == nil || run.Outcome == "" {
		return
	}
	if err := s.SetEnabled(ctx, j.ID, false); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: disable one-shot job:", err)
	}
}

// executePrompt runs a single-prompt job as one agent.
func executePrompt(ctx context.Context, s *store.Store, j store.Job, trigger string) (*runner.Run, error) {
	r := &runner.Runner{Herdr: herdrcli.New(), StateDir: stateDir()}
	runID := newRunID(j.ID)

	// Record the run as started before doing any work. Without this a long run
	// is invisible on the board, and the scheduler's anchor stays at the
	// previous run, so a daemon restart mid-run would fire the job again.
	// The run directory is recorded up front, not only when the run finishes:
	// it is where the outcome will be written, so a row that names it can be
	// resolved later even if this process never gets to write the outcome.
	if err := s.PutRun(ctx, store.Run{
		ID: runID, JobID: j.ID, Trigger: trigger, RunDir: runDirFor(runID),
		Outcome: "running", StartedAt: time.Now(),
	}); err != nil {
		return nil, fmt.Errorf("record run start: %w", err)
	}

	run, execErr := r.Execute(ctx, runner.FromStore(j), runID)
	if run != nil {
		if err := persist(ctx, s, run, trigger, j.CWD); err != nil {
			fmt.Fprintln(os.Stderr, "bermuda: persist run:", err)
		}
	}
	// A one-shot job disables itself once it has actually run, so a restart
	// cannot fire it a second time.
	disableOneShot(ctx, s, j, run)
	return run, execErr
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseWhen accepts RFC3339 or a local "2006-01-02 15:04" timestamp.
func parseWhen(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q: use RFC3339 or '2006-01-02 15:04'", s)
}
