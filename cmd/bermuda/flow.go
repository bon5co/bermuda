package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bon5co/bermuda/internal/flow"
	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/runner"
	"github.com/bon5co/bermuda/internal/store"
)

// The flow verbs. A flow is a job with steps instead of a prompt, so it
// is run, inspected, and resumed by run id like any other run — these commands
// exist because a parked flow needs one thing an ordinary run does not: a
// way to start again at the step that stopped it, without redoing the ones that
// already cost money.

func flowCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda flow <new|list|show|edit|rm|run|status|resume>")
	}
	switch argv[0] {
	case "new":
		return flowNew(argv[1:])
	case "list":
		return flowList(argv[1:])
	case "show", "cat":
		return flowShow(argv[1:])
	case "edit":
		return flowEdit(argv[1:])
	case "rm":
		return flowRemove(argv[1:])
	case "run":
		return flowRun(argv[1:])
	case "status":
		return flowStatus(argv[1:])
	case "resume":
		return flowResume(argv[1:])
	default:
		return fmt.Errorf("unknown flow subcommand %q", argv[0])
	}
}

// workflowCmd is the old name for `bermuda flow`, kept working and kept out of
// the usage text, the same way `room` is kept for `thread`.
//
// Stored jobs, cron entries and launcher scripts hold `bermuda workflow ...`,
// and none of them get re-read when a feature is renamed. A scheduled flow that
// stops running because its verb moved fails silently at 04:00, which is the
// hour this feature exists for. The notice goes to stderr so `workflow status
// --json` still pipes into a parser unchanged.
func workflowCmd(argv []string) error {
	fmt.Fprintln(os.Stderr, "bermuda: `workflow` was renamed to `flow`; the old name still works")
	return flowCmd(argv)
}

// flowRun calls a flow with an input.
//
// This is the whole point of a flow being its own thing: one command, called
// the same way by a person typing it and by an agent shelling out. There is no
// agent-only path and no human-only path, so anything an agent can start, a
// human can start identically, and both land in the same run list.
func flowRun(argv []string) error {
	fs := flag.NewFlagSet("flow run", flag.ExitOnError)
	input := fs.String("input", "", "the x this flow is called with")
	cwd := fs.String("cwd", "", "working directory for every step (default: here)")
	model := fs.String("model", "", "model for agent steps that do not name one")
	kind := fs.String("kind", "", "herdr agent kind")
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: bermuda flow run <flow> [--input ...] [--cwd ...] [--model ...]")
	}
	id := argv[0]
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}

	def, err := flow.Load(flowDir(), id)
	if err != nil {
		return err
	}
	// A flow that declares an input and is called without one is refused rather
	// than run with a blank. Every prompt saying {{input}} would otherwise get a
	// hole where its subject should be, and an agent handed that will invent
	// something to fill it.
	if def.TakesInput() && strings.TrimSpace(*input) == "" {
		return fmt.Errorf("flow %s needs an input: %s\n  bermuda flow run %s --input '...'",
			def.ID, def.Input, def.ID)
	}
	if !def.TakesInput() && strings.TrimSpace(*input) != "" {
		// Accepted, not refused — but said out loud, because a flow whose steps
		// never mention {{input}} will silently ignore it.
		fmt.Fprintf(os.Stderr, "bermuda: flow %s declares no input, so --input is only "+
			"visible to run steps as $BERMUDA_INPUT\n", def.ID)
	}

	dir := *cwd
	if strings.TrimSpace(dir) == "" {
		if here, err := os.Getwd(); err == nil {
			dir = here
		}
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	j := flowJob(def, dir, *model, *kind)
	rec := store.Run{
		ID: newRunID(def.ID), JobID: def.ID, Trigger: "manual",
		Outcome: "running", StartedAt: time.Now(),
		Flow: def.ID, Input: *input,
	}
	run, execErr := runFlow(ctx, s, j, rec)
	if run != nil {
		printRun(run)
	}
	if execErr != nil {
		return execErr
	}
	exitUnlessDone(run)
	return nil
}

// exitUnlessDone makes a flow that did not finish visible to whatever ran
// it. A parked flow is not an error — it is the harness doing its job — but
// a script that treats it as success is back to proceeding on an unfinished
// sequence.
func exitUnlessDone(run *runner.Run) {
	if run != nil && run.Outcome != runner.OutcomeDone {
		os.Exit(1)
	}
}

func flowResume(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda flow resume <run>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	rec, err := s.Run(ctx, argv[0])
	if err != nil {
		return err
	}
	if strings.TrimSpace(rec.Flow) == "" {
		return fmt.Errorf("run %s is not a flow run; there is nothing to resume", rec.ID)
	}
	// The job is optional now. A flow called directly has no job at all, and one
	// called by a job may outlive it — but the run itself records which flow ran
	// and what it was called with, so neither case needs the job to still exist.
	j := store.Job{ID: rec.JobID, Flow: rec.Flow, Enabled: true,
		Model: store.DefaultModel, Kind: store.DefaultKind}
	if stored, err := s.Job(ctx, rec.JobID); err == nil {
		j = *stored
	}
	run, execErr := runFlow(ctx, s, j, *rec)
	if run != nil {
		printRun(run)
	}
	if execErr != nil {
		return execErr
	}
	exitUnlessDone(run)
	return nil
}

// flowStatus prints one run step by step: what each was, how it went, and
// how long it took.
func flowStatus(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda flow status <run>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	rec, err := s.Run(ctx, argv[0])
	if err != nil {
		return err
	}
	steps, err := s.RunSteps(ctx, rec.ID)
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		return fmt.Errorf("run %s has no steps; it is not a flow run", rec.ID)
	}

	done := 0
	for _, st := range steps {
		if st.Outcome == store.StepDone {
			done++
		}
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "run\t%s\njob\t%s\noutcome\t%s\n", rec.ID, rec.JobID, rec.Outcome)
	if rec.ParkReason != "" {
		fmt.Fprintf(w, "park reason\t%s\n", rec.ParkReason)
	}
	fmt.Fprintf(w, "progress\t%d/%d steps\nstarted\t%s\n", done, len(steps),
		rec.StartedAt.Format(time.RFC3339))
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	sw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(sw, "STEP\tKIND\tOUTCOME\tREASON\tDURATION\tNOTE")
	for _, st := range steps {
		dur := ""
		if st.EndedAt != nil {
			// Blank means "has not run": a step that finished shows a duration
			// even when it was under a second.
			dur = st.Duration().Round(time.Second).String()
		}
		fmt.Fprintf(sw, "%s\t%s\t%s\t%s\t%s\t%s\n", st.StepID, st.Kind,
			st.Outcome, st.ParkReason, dur, st.Note)
	}
	if err := sw.Flush(); err != nil {
		return err
	}
	if rec.Outcome == "parked" || rec.Outcome == "failed" {
		fmt.Printf("\nresume with: bermuda flow resume %s\n", rec.ID)
	}
	return nil
}

// runFlow executes a job's steps into an existing run row.
//
// The same call serves a first run and a resume: the run row and the run
// directory are reused, so completed steps are found where the earlier attempt
// left them and are not run again.
func runFlow(ctx context.Context, s *store.Store, j store.Job, rec store.Run) (*runner.Run, error) {
	if rec.RunDir == "" {
		rec.RunDir = runDirFor(rec.ID)
	}
	// Read at the moment it runs, never earlier. The file is edited by people and
	// by agents between one run and the next, so the only definition worth acting
	// on is the one on disk right now.
	def, err := flow.Load(flowDir(), firstNonEmpty(rec.Flow, j.Flow))
	if err != nil {
		return nil, err
	}
	// Declare every step before any of them runs, so a flow that dies at
	// step one still says it had four.
	if err := s.SeedRunSteps(ctx, rec.ID, def.Steps); err != nil {
		return nil, fmt.Errorf("record steps: %w", err)
	}
	rec.Outcome, rec.ParkReason, rec.EndedAt = "running", "", nil
	if err := s.PutRun(ctx, rec); err != nil {
		return nil, fmt.Errorf("record run start: %w", err)
	}

	r := &runner.Runner{Herdr: herdrcli.New(), StateDir: stateDir()}
	w := &runner.Flow{
		Launch: r.ExecuteIn,
		// Persisted as each step starts and settles, so a flow that is
		// three hours into its second step says so on the board rather than
		// looking untouched until the end.
		Report: func(sr runner.StepRun) { persistStep(ctx, s, rec.ID, sr) },
	}
	wr, execErr := w.Execute(ctx, j, def, rec.Input, rec.ID, rec.RunDir)

	rec.Outcome, rec.ParkReason, rec.Note = string(wr.Outcome), string(wr.ParkReason), wr.Note()
	ended := wr.EndedAt
	rec.EndedAt = &ended
	if err := s.PutRun(ctx, rec); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: persist flow run:", err)
	}

	// A flow is reported as a run, because that is what every caller — the
	// daemon, the board, `run list` — already knows how to read.
	status := "ok"
	if wr.Outcome != runner.OutcomeDone {
		status = "error"
	}
	return &runner.Run{
		JobID: j.ID, RunID: rec.ID, RunDir: rec.RunDir,
		Outcome: wr.Outcome, ParkReason: wr.ParkReason,
		Result:    &runner.Result{Status: status, Note: wr.Note()},
		StartedAt: wr.StartedAt, EndedAt: wr.EndedAt, Err: wr.Err,
	}, execErr
}

// persistStep mirrors one step's progress into the store.
func persistStep(ctx context.Context, s *store.Store, runID string, sr runner.StepRun) {
	rec := store.RunStep{
		RunID: runID, Index: sr.Index, StepID: sr.ID, Kind: sr.Kind,
		Outcome: string(sr.Outcome), ParkReason: string(sr.ParkReason),
		Note: sr.Note, StepDir: sr.Dir, AgentName: sr.AgentName,
		StartedAt: sr.StartedAt,
	}
	if rec.Note == "" && sr.Err != nil {
		rec.Note = sr.Err.Error()
	}
	if !sr.EndedAt.IsZero() {
		t := sr.EndedAt
		rec.EndedAt = &t
	}
	if err := s.PutRunStep(ctx, rec); err != nil {
		// Bookkeeping: the step itself already happened, and result.json on
		// disk is what resume reads, so a failed write here must not stop the
		// flow.
		fmt.Fprintln(os.Stderr, "bermuda: persist step:", err)
	}
}
