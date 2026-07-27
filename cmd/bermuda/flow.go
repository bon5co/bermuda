package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

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
		return errors.New("usage: bermuda flow <run|status|resume>")
	}
	switch argv[0] {
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

func flowRun(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda flow run <job>")
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
	if !j.IsFlow() {
		return fmt.Errorf("job %s has no steps; run it with `bermuda job run %s`", j.ID, j.ID)
	}
	run, execErr := Execute(ctx, s, *j, "manual")
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
	j, err := s.Job(ctx, rec.JobID)
	if err != nil {
		// A run outlives its job, but a resume cannot: the steps to run are the
		// job's, and guessing them from the run's directories would resume a
		// flow nobody declared.
		return fmt.Errorf("job %s no longer exists, so run %s cannot be resumed", rec.JobID, rec.ID)
	}
	if !j.IsFlow() {
		return fmt.Errorf("job %s has no steps; there is nothing to resume", j.ID)
	}
	run, execErr := runFlow(ctx, s, *j, *rec)
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
	// Declare every step before any of them runs, so a flow that dies at
	// step one still says it had four.
	if err := s.SeedRunSteps(ctx, rec.ID, j.Steps); err != nil {
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
	wr, execErr := w.Execute(ctx, j, rec.ID, rec.RunDir)

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

// loadSteps reads a step list from a JSON file, or from stdin for "-".
//
// JSON rather than a config format of its own: the steps are stored as JSON,
// and a job is declared by whatever wrote it — usually another agent, which has
// a JSON encoder and no opinion about syntax.
func loadSteps(path string) ([]store.Step, error) {
	var (
		raw []byte
		err error
	)
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	var steps []store.Step
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, fmt.Errorf("read steps from %s: %w", path, err)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("read steps from %s: the list is empty", path)
	}
	return steps, nil
}
