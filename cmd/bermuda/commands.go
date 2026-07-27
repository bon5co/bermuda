package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/bon5co/bermuda/internal/board"
	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/runner"
	"github.com/bon5co/bermuda/internal/store"
	tokens "github.com/bon5co/bermuda/internal/usage"
)

// openStore opens the store in the state directory.
func openStore() (*store.Store, error) {
	return store.Open(stateDir())
}

// boardCmd runs the TUI board.
//
// Asked for from a shell with no terminal — an agent's, a hook's — the board
// cannot be drawn here, but it can be drawn somewhere: it opens as a Herdr pane
// and this process exits. "Open the board" is the instruction either way.
//
// `--pin` is neither: it opens the board in bermuda's own workspace, in the
// background, and exits. It is what the startup hook runs, so that bermuda has
// a row in the sidebar before anybody has asked for one.
func boardCmd(argv []string) error {
	fs, pin := boardFlagSet()
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *pin {
		return pinBoard()
	}
	if !hasTTY() {
		return openBoardElsewhere()
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	// Opening the board is also a chance to revive a dead scheduler: if the
	// daemon died, the jobs on screen would silently never fire.
	if err := EnsureRunning(); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: ensure daemon:", err)
	}

	h := herdrcli.New()
	// A tab holding nothing but the board should say so.
	nameOwnTab(h)
	// And the sidebar should say the board is open, and what it is watching.
	presence := startBoardPresence(s, h)
	defer presence.Stop()

	return board.Run(s, h, board.Deps{
		Run: func(j store.Job, trigger string) error {
			_, err := Execute(context.Background(), s, j, trigger)
			return err
		},
		DaemonRunning: Running,
		EnsureDaemon:  EnsureRunning,
	})
}

// jobCmd dispatches job subcommands.
func jobCmd(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("usage: bermuda job <list|show|add|edit|remove|prune|pause|resume|run>")
	}
	switch argv[0] {
	case "list":
		return jobList(argv[1:])
	case "show":
		return jobShow(argv[1:])
	case "add":
		return jobAdd(argv[1:])
	case "edit":
		return jobEdit(argv[1:])
	case "remove", "rm":
		return jobRemove(argv[1:])
	case "prune":
		return jobPrune(argv[1:])
	case "pause":
		return jobEnable(argv[1:], false)
	case "resume":
		return jobEnable(argv[1:], true)
	case "run":
		return jobRun(argv[1:])
	default:
		return fmt.Errorf("unknown job subcommand %q", argv[0])
	}
}

// runCmd dispatches run subcommands.
func runCmd(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("usage: bermuda run <list|show>")
	}
	switch argv[0] {
	case "list":
		return runList(argv[1:])
	case "show":
		return runShow(argv[1:])
	default:
		return fmt.Errorf("unknown run subcommand %q", argv[0])
	}
}

// runListOpts is what `bermuda run list` accepts.
type runListOpts struct {
	state  *string
	limit  *int
	asJSON *bool
}

// runListFlagSet is split out for the same reason daemonFlagSet is: the plugin
// manifest invokes this command, and a test checks the flags it passes are real.
func runListFlagSet() (*flag.FlagSet, *runListOpts) {
	fs := flag.NewFlagSet("run list", flag.ExitOnError)
	return fs, &runListOpts{
		state:  fs.String("state", "", "filter by outcome: done|failed|parked|running"),
		limit:  fs.Int("limit", 20, "maximum rows"),
		asJSON: fs.Bool("json", false, "emit JSON"),
	}
}

func runList(argv []string) error {
	fs, opts := runListFlagSet()
	if err := fs.Parse(argv); err != nil {
		return err
	}
	state, limit, asJSON := opts.state, opts.limit, opts.asJSON
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	runs, err := s.Runs(context.Background(), *state, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(runs)
	}
	if len(runs) == 0 {
		fmt.Println("no runs")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tJOB\tTRIGGER\tOUTCOME\tREASON\tSTARTED\tNOTE")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.JobID, r.Trigger,
			r.Outcome, r.ParkReason, r.StartedAt.Format("2006-01-02 15:04"), r.Note)
	}
	return w.Flush()
}

// runShow prints one run and where to find its artifacts.
func runShow(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("usage: bermuda run show <run-id>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	// Looked up by id rather than scanned out of the recent list: a run that
	// has scrolled past the last few hundred is still a run somebody can name.
	r, err := s.Run(context.Background(), argv[0])
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "run\t%s\njob\t%s\ntrigger\t%s\noutcome\t%s\n",
		r.ID, r.JobID, r.Trigger, r.Outcome)
	if r.ParkReason != "" {
		fmt.Fprintf(w, "park reason\t%s\n", r.ParkReason)
	}
	fmt.Fprintf(w, "agent status\t%s\nagent\t%s\ntab\t%s\nstarted\t%s\nduration\t%s\n",
		r.Status, r.AgentName, r.TabID,
		r.StartedAt.Format(time.RFC3339), r.Duration().Round(time.Second))
	if r.Note != "" {
		fmt.Fprintf(w, "note\t%s\n", r.Note)
	}
	fmt.Fprintf(w, "tokens\t%s\n", tokens.Line(tokens.Usage{
		InputTokens:         r.InputTokens,
		OutputTokens:        r.OutputTokens,
		CacheReadTokens:     r.CacheReadTokens,
		CacheCreationTokens: r.CacheCreationTokens,
	}))
	if r.Model != "" {
		fmt.Fprintf(w, "model\t%s\n", r.Model)
	}
	fmt.Fprintf(w, "prompt\t%s/prompt.md\ntranscript\t%s/transcript.txt\nresult\t%s/result.json\n",
		r.RunDir, r.RunDir, r.RunDir)
	// A flow run's artifacts are one directory per step under that dir, and
	// its per-step record is a different command.
	if steps, err := s.RunSteps(context.Background(), r.ID); err == nil && len(steps) > 0 {
		fmt.Fprintf(w, "steps\t%d (bermuda flow status %s)\n", len(steps), r.ID)
	}
	return w.Flush()
}

// ensureCmd reconciles state after a Herdr restart or live handoff.
//
// It is the plugin's startup hook, so it must exit rather than supervise:
// scheduling belongs to the daemon, which the ensure hook starts detached and
// which then keeps itself alive with the sentinel. Nothing here runs under a
// system supervisor: bermuda installs as a plugin, not as a unit file.
func ensureCmd(argv []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()

	// Make sure a scheduler exists before anything else: the lock makes this a
	// no-op when one is already running, and without it a crashed daemon would
	// stay dead until the next Herdr restart.
	if err := EnsureRunning(); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: ensure daemon:", err)
	}

	n, err := reconcileRuns(ctx, s, herdrcli.New())
	if err != nil {
		return err
	}
	fmt.Printf("bermuda: reconciled %d run(s)\n", n)
	return nil
}

// persist writes a run to the store, including what it cost.
func persist(ctx context.Context, s *store.Store, run *runner.Run, trigger, cwd string) error {
	rec := store.Run{
		ID:         run.RunID,
		JobID:      run.JobID,
		Trigger:    trigger,
		Outcome:    string(run.Outcome),
		ParkReason: string(run.ParkReason),
		Status:     string(run.Status),
		RunDir:     run.RunDir,
		TabID:      run.TabID,
		AgentName:  run.AgentName,
		StartedAt:  run.StartedAt,
	}
	if run.Result != nil {
		rec.Note = run.Result.Note
	}
	if !run.EndedAt.IsZero() {
		t := run.EndedAt
		rec.EndedAt = &t
	}
	// Usage is bookkeeping: a transcript that cannot be read or parsed leaves
	// the counts at zero and the run keeps its real outcome.
	if home, err := os.UserHomeDir(); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: token usage:", err)
	} else if u, err := tokens.CollectSettled(home, cwd, run.RunDir, 10*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: token usage:", err)
	} else {
		rec.InputTokens = u.InputTokens
		rec.OutputTokens = u.OutputTokens
		rec.CacheReadTokens = u.CacheReadTokens
		rec.CacheCreationTokens = u.CacheCreationTokens
		rec.Model = u.Model
	}
	return s.PutRun(ctx, rec)
}
