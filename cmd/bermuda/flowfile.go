package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/bon5co/bermuda/internal/flow"
	"github.com/bon5co/bermuda/internal/store"
)

// The flow file verbs: new, list, show, edit, rm.
//
// A flow is a YAML file, so these are deliberately thin. `flow new` writes a
// template and `flow edit` opens it — everything past that is a text editor,
// which is a better flow editor than anything that could be built here and is
// the one an agent already has.

// flowDir is where this installation keeps its flows.
func flowDir() string { return flow.Dir(stateDir()) }

// flowTemplate is what `flow new` writes.
//
// It is a working flow rather than an empty skeleton: the first thing anyone
// does with a new flow is run it to see what happens, and a template that parks
// immediately teaches nothing. The comments are the documentation an agent
// editing this file will actually have in front of it.
const flowTemplate = `# What this flow is for. Shown in ` + "`bermuda flow list`" + ` and on the board.
about: %s

# What the caller has to supply, described in a sentence rather than typed.
# The steps below reach it as {{input}}. Delete this line if the flow takes none.
input: what this flow should be run against

# Agent steps run with --dangerously-skip-permissions by default, because a
# flow step has nobody in its pane to answer a prompt: it waits, then parks.
# Uncomment to make this flow ask, or set skip_permissions on one step.
# skip_permissions: false

steps:
  # Each step is an agent prompt or a shell command, never both.
  # A step that cannot show it finished parks the flow and the rest never start.
  - id: assess
    agent: |
      Look at {{input}} and say in one line whether it is worth acting on.

  - id: act
    # {{previous}} is the note the step before published — not its context.
    # A step never inherits the previous agent's session.
    agent: |
      {{previous}}

      If that says it is worth acting on, do it. Otherwise stop and say why.

  - id: verify
    # A run step has no agent. It reads the same two values from the
    # environment, as $BERMUDA_INPUT and $BERMUDA_PREVIOUS.
    # Quoted because a bare YAML scalar cannot contain ": ".
    run: 'echo "verified: $BERMUDA_PREVIOUS"'
`

func flowNew(argv []string) error {
	fs := flag.NewFlagSet("flow new", flag.ExitOnError)
	about := fs.String("about", "", "what this flow is for")
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: bermuda flow new <id> [--about ...]")
	}
	id := argv[0]
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	parsed, err := flow.ParseID(id)
	if err != nil {
		return err
	}
	dir := flowDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, parsed+flow.Ext)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("flow %s already exists at %s", parsed, path)
	}
	text := *about
	if strings.TrimSpace(text) == "" {
		text = "what " + parsed + " does"
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(flowTemplate, text)), 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	fmt.Printf("edit it, then: bermuda flow run %s --input '...'\n", parsed)
	return nil
}

func flowList(argv []string) error {
	fs := flag.NewFlagSet("flow list", flag.ExitOnError)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	flows, bad := flow.List(flowDir())
	// Broken flows are named before the good ones are listed, because a flow
	// that will not parse is invisible everywhere else — it simply does not
	// appear, which reads as "I never made that one".
	for _, err := range bad {
		fmt.Fprintln(os.Stderr, "bermuda:", err)
	}
	if len(flows) == 0 {
		fmt.Println("no flows yet — `bermuda flow new <id>` writes one")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FLOW\tSTEPS\tINPUT\tABOUT")
	for _, f := range flows {
		input := f.Input
		if input == "" {
			input = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", f.ID, len(f.Steps), input, f.About)
	}
	return w.Flush()
}

func flowShow(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda flow show <id>")
	}
	f, err := flow.Load(flowDir(), argv[0])
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	// The file itself, not a rendering of it. Whoever asks to see a flow is
	// about to edit it, and a pretty-printed version they cannot paste back is
	// the wrong answer.
	fmt.Print(string(raw))
	return nil
}

func flowEdit(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda flow edit <id>")
	}
	f, err := flow.Load(flowDir(), argv[0])
	if err != nil {
		// A flow that does not parse is exactly the one somebody needs to open,
		// so a broken file is editable — the path is all that is needed.
		parsed, perr := flow.ParseID(argv[0])
		if perr != nil {
			return perr
		}
		path := filepath.Join(flowDir(), parsed+flow.Ext)
		if _, serr := os.Stat(path); serr != nil {
			return err
		}
		f.Path = path
	}
	editor := firstNonEmptyEnv("BERMUDA_EDITOR", "VISUAL", "EDITOR")
	if editor == "" {
		// No editor is not an error worth failing on: the path is the useful
		// half of this command, and an agent has no $EDITOR at all.
		fmt.Println(f.Path)
		return nil
	}
	cmd := exec.Command("sh", "-c", editor+" "+shellQuote(f.Path))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	// Re-read on the way out, so a syntax error is reported now rather than at
	// 04:00 by the scheduler.
	if _, err := flow.Load(flowDir(), argv[0]); err != nil {
		return err
	}
	return nil
}

func flowRemove(argv []string) error {
	fs := flag.NewFlagSet("flow rm", flag.ExitOnError)
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: bermuda flow rm <id>")
	}
	id := argv[0]
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	// A job pointing at a flow that no longer exists fails at its next fire,
	// with nothing said until then. Naming them here is the only chance to say
	// it while somebody is looking.
	if users, err := jobsUsingFlow(id); err == nil && len(users) > 0 {
		return fmt.Errorf("flow %s is used by job %s: change or remove the job first, "+
			"or it will fail at its next fire with nothing said until then",
			id, strings.Join(users, ", "))
	}
	if err := flow.Remove(flowDir(), id); err != nil {
		return err
	}
	fmt.Printf("removed flow %s\n", id)
	return nil
}

// jobsUsingFlow lists the ids of jobs that start this flow.
func jobsUsingFlow(id string) ([]string, error) {
	s, err := openStore()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	jobs, err := s.Jobs(context.Background())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, j := range jobs {
		if strings.EqualFold(strings.TrimSpace(j.Flow), strings.TrimSpace(id)) {
			out = append(out, j.ID)
		}
	}
	return out, nil
}

// firstNonEmptyEnv returns the first of these environment variables that is set.
func firstNonEmptyEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// shellQuote makes a path safe to interpolate into `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// flowJob is the configuration a directly-called flow runs under.
//
// A flow has no schedule, no working directory and no model of its own: those
// belong to whoever calls it, and baking them into the file would make a flow
// that only works in one repository. So a direct call builds a job on the spot
// from its flags, and a scheduled call uses the job that already exists.
// The defaults here are the ones `job add` applies, and they have to be applied
// here too because a directly-called flow never goes through `job add` or
// PutJob. Two bugs came from that gap in one sitting: an empty Kind, which herdr
// refuses outright, and permission prompts left enabled, which park every agent
// step at `blocked` eight seconds in. A flow of `run:` steps survives both, so
// neither showed up until a flow with an agent step was actually run.
func flowJob(f flow.Flow, cwd, model, kind string) store.Job {
	j := store.Job{
		ID:      f.ID,
		Name:    f.About,
		Flow:    f.ID,
		CWD:     cwd,
		Model:   model,
		Kind:    kind,
		Enabled: true,
		// A flow step has nobody sitting in its pane, so a permission prompt is
		// a step that waits forever and then parks. `job add` makes the same call
		// for the same reason: these runs are unattended by construction.
		SkipPermissions: true,
		PermissionMode:  defaultPermissionMode,
		Catchup:         store.CatchupLatest,
	}
	if strings.TrimSpace(j.Model) == "" {
		j.Model = store.DefaultModel
	}
	if strings.TrimSpace(j.Kind) == "" {
		j.Kind = store.DefaultKind
	}
	return j
}

// firstNonEmpty returns the first non-blank of its arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// defaultPermissionMode is what an unattended agent runs under. It matches the
// `job add` default, so a flow called by hand behaves like the same flow called
// by a schedule.
const defaultPermissionMode = "acceptEdits"
