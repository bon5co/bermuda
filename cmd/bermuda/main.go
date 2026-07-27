// Command bermuda is an agent harness: it schedules work and runs each job as
// an interactive herdr agent that a human can inspect, interrupt, and answer.
//
// This first slice implements `bermuda run-once`, which executes a single
// ad-hoc job through the full runner lifecycle. The scheduler daemon, store,
// and TUI board build on the same runner.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/runner"
	"github.com/bon5co/bermuda/internal/version"
)

// commands is every subcommand bermuda answers to.
//
// A function rather than a literal inside main, so a test can ask whether the
// commands the plugin manifest invokes actually exist. Two of them did not, and
// nothing noticed until they failed on a user's machine.
//
// "room" is the pre-rename spelling of "thread", kept dispatchable and left out
// of the usage text: see roomCmd.
func commands() map[string]func([]string) error {
	return map[string]func([]string) error{
		"run-once":   runOnce,
		"board":      boardCmd,
		"job":        jobCmd,
		"run":        runCmd,
		"workflow":   workflowCmd,
		"thread":     threadCmd,
		"room":       roomCmd,
		"usage":      usageCmd,
		"ensure":     ensureCmd,
		"daemon":     daemonCmd,
		"sentinel":   sentinelCmd,
		"board-wide": boardWideCmd,
		"stop":       stopCmd,
		"start":      startCmd,
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmds := commands()
	switch os.Args[1] {
	case "-v", "--version", "version":
		fmt.Println(version.Full())
	case "-h", "--help", "help":
		usage()
	default:
		fn, ok := cmds[os.Args[1]]
		if !ok {
			fmt.Fprintf(os.Stderr, "bermuda: unknown command %q\n", os.Args[1])
			usage()
			os.Exit(2)
		}
		if err := fn(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bermuda:", err)
			os.Exit(1)
		}
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bermuda — agent harness

Usage:
  bermuda board                       Open the TUI board
  bermuda board --pin                 Open it in bermuda's workspace, unfocused
  bermuda job list                    List jobs
  bermuda job add --id <id> --prompt <text> [--schedule <cron>]
  bermuda job remove <id>             Remove a job
  bermuda job run <id>                Run a stored job now
  bermuda run list [--state parked]   List runs
  bermuda job add --id <id> --steps <file.json>   Declare a workflow
  bermuda workflow run <job>          Run a job's steps in series
  bermuda workflow status <run>       Per-step outcome and duration
  bermuda workflow resume <run>       Restart at the step that parked

  A workflow's steps run in order, each agent step in its own process with its
  own result.json. A step that fails, or that ends without writing one, parks
  the workflow there and the steps after it never start. Resume picks up at that
  step: what the completed ones wrote is still on disk and is not redone.
  bermuda thread list                 Every thread, its size and last activity
  bermuda thread new <id> [--about]   Start a separate conversation
  bermuda thread rm <id> [--force]    Delete a thread and its messages
  bermuda thread status               Who holds which resource, and until when
  bermuda thread log [--since 1h]     One thread, oldest first (--all for every one)
  bermuda thread event <text>         Record that the world changed
  bermuda thread post <text>          Leave a note for the next agent
  bermuda thread claim <resource> --ttl 20m --why '...'
  bermuda thread release <resource>
  bermuda thread with <resource> --ttl 20m -- <cmd>...
  bermuda thread whoami [--as <name>]  Who this shell claims as, and its pid

  Every thread subcommand takes --thread <id>, defaulting to $BERMUDA_THREAD and
  then to global. Claims are global whatever thread they were taken from.
  An interactive identity is name + pid, so two agents both called ada are
  two holders; $BERMUDA_PID forces the pid when it resolves to the wrong thing.
  An @name in a posted message is delivered to every live agent answering to it
  (its herdr name, its pane label, or its working directory); @all is everybody
  but you. Reaching nobody is reported, never an error.
  bermuda usage [--since 7d]          Token totals per job
  bermuda daemon [--tick 5s]          Run the scheduler loop
  bermuda daemon --detach             Start it in the background and return
  bermuda stop                        Stop the scheduler, and keep it stopped
  bermuda start                       Undo a stop and bring it back

  A stop is remembered. The daemon and the sentinel revive each other, and the
  plugin hook and the board start them too, so a stop that was not written down
  would last about five seconds; only start undoes it.
  bermuda ensure                      Reconcile orphaned runs (plugin startup hook)

  bermuda run-once --prompt <text>    Run an ad-hoc job
  bermuda --version                   Print the running build

run-once options:
  --prompt <text>    Instruction for the agent (required)
  --id <name>        Job id, used to name the tab and agent (default "adhoc")
  --kind <kind>      herdr agent kind (default "claude")
  --cwd <path>       Working directory for the agent (default: current dir)
  --timeout <dur>    Deadline for the run, e.g. 15m (default 15m)
  --agent-args <s>   Space-separated args passed through to the agent binary
`)
}

func runOnce(argv []string) error {
	fs := flag.NewFlagSet("run-once", flag.ExitOnError)
	prompt := fs.String("prompt", "", "instruction for the agent")
	id := fs.String("id", "adhoc", "job id")
	kind := fs.String("kind", "claude", "herdr agent kind")
	cwd := fs.String("cwd", "", "working directory")
	timeout := fs.Duration("timeout", 15*time.Minute, "run deadline")
	agentArgs := fs.String("agent-args", "", "args passed through to the agent")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *prompt == "" {
		return fmt.Errorf("--prompt is required")
	}
	if *cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		*cwd = wd
	}

	r := &runner.Runner{
		Herdr:    herdrcli.New(),
		StateDir: stateDir(),
	}
	job := runner.Job{
		ID:      *id,
		Prompt:  *prompt,
		CWD:     *cwd,
		Kind:    *kind,
		Timeout: *timeout,
	}
	if *agentArgs != "" {
		job.AgentArgs = strings.Fields(*agentArgs)
	}

	run, err := r.Execute(context.Background(), job, newRunID(*id))
	if run != nil {
		printRun(run)
	}
	if err != nil {
		return err
	}
	if run.Outcome == runner.OutcomeFailed {
		os.Exit(1)
	}
	return nil
}

func printRun(run *runner.Run) {
	out := map[string]any{
		"run_id":  run.RunID,
		"job_id":  run.JobID,
		"outcome": run.Outcome,
		"status":  run.Status,
		"run_dir": run.RunDir,
		"tab_id":  run.TabID,
		"agent":   run.AgentName,
		"seconds": int(run.EndedAt.Sub(run.StartedAt).Seconds()),
	}
	if run.ParkReason != "" {
		out["park_reason"] = run.ParkReason
	}
	if run.Result != nil {
		out["result"] = run.Result
	}
	if run.Err != nil {
		out["error"] = run.Err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// stateDir resolves where the store and run directories live: ~/.bermuda,
// holding bermuda.db, bermuda.log, the lock files and every run directory.
//
// One visible directory rather than the XDG split, for the same reason bermuda
// ships as one binary with no unit file: the whole of it should be somewhere a
// person can find by looking. A run directory is something you read — it is
// where an agent's prompt and result live — so burying it under
// ~/.local/state costs more than the tidiness is worth.
//
// It deliberately ignores HERDR_PLUGIN_STATE_DIR. That variable is only set
// for commands Herdr launches, so honouring it would give the board opened as
// a plugin pane a different database from the one the daemon writes — the
// scheduler must run with no Herdr server at all, so the store cannot live in
// Herdr-managed state. BERMUDA_STATE_DIR overrides, which is how the tests get
// a private directory.
func stateDir() string {
	if d := os.Getenv("BERMUDA_STATE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".bermuda")
}

// newRunID builds a sortable, filesystem-safe run id.
func newRunID(jobID string) string {
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), jobID)
}
