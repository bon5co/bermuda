package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/runner"
	"github.com/charmbracelet/x/term"
)

// The board is published as a Herdr plugin pane, which is how it can be asked
// for from a shell that could not draw it itself.
const (
	boardPlugin     = "bon5co.bermuda"
	boardEntrypoint = "board"
)

// hasTTY reports whether this process can draw a terminal UI.
//
// The check is the one Bubble Tea makes when it starts: stdin if it is a
// terminal, otherwise /dev/tty. Testing the file mode instead would call an
// agent's shell a terminal, since its stdin is /dev/null and that is a
// character device like any other.
func hasTTY() bool {
	if term.IsTerminal(os.Stdin.Fd()) {
		return true
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// inHerdr reports whether this shell was started by a running Herdr server.
func inHerdr() bool {
	return os.Getenv("HERDR_SOCKET_PATH") != "" || os.Getenv("HERDR_ENV") != ""
}

// boardPaneFor decides where a board asked for from elsewhere should be drawn.
//
// A split is the nicer layout but divides a named pane, so it is only available
// when the caller sits in one. A tab needs nothing, which is what makes it the
// fallback: an agent that inherited no pane id still gets a board rather than
// an error about how it should have been invoked.
func boardPaneFor(paneID, workspace string) herdrcli.PluginPane {
	p := herdrcli.PluginPane{Plugin: boardPlugin, Entrypoint: boardEntrypoint}
	if paneID != "" {
		p.Placement = "split"
		p.Direction = "down"
		p.TargetPane = paneID
		return p
	}
	p.Placement = "tab"
	p.Workspace = workspace
	return p
}

// boardFlagSet is the board's flags, shared with the test that reads the
// manifest: the hooks Herdr runs are only ever exercised on somebody else's
// machine, so what they pass is checked against the command that parses it.
func boardFlagSet() (*flag.FlagSet, *bool) {
	fs := flag.NewFlagSet("board", flag.ExitOnError)
	pin := fs.Bool("pin", false, "open the board in bermuda's workspace, unfocused, and exit")
	return fs, pin
}

// pinBoard opens the board in bermuda's own workspace, unfocused, once.
//
// This is what puts bermuda in the sidebar without anybody opening anything:
// the row above Spaces belongs to the board's pane, so no pane means no row,
// and a harness nobody can see is a harness nobody checks. The board goes in
// bermuda's workspace rather than wherever the session happens to start, which
// keeps a startup hook from putting a pane in the middle of somebody's work.
//
// Already open is the common case — a live handoff runs the startup hooks again
// against a session that never stopped — so an existing bermuda row is the
// signal to do nothing rather than to open a second board.
func pinBoard() error {
	if !inHerdr() {
		return fmt.Errorf("no herdr session to pin the board in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h := herdrcli.New()
	agents, err := h.AgentList(ctx)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.Agent == boardAgentLabel {
			return nil
		}
	}

	ws, err := bermudaWorkspace(ctx, h)
	if err != nil {
		return err
	}
	return h.OpenPluginPane(ctx, herdrcli.PluginPane{
		Plugin:     boardPlugin,
		Entrypoint: boardEntrypoint,
		Placement:  "tab",
		Workspace:  ws,
		Background: true,
	})
}

// bermudaWorkspace is the id of the workspace bermuda owns, creating it if this
// is a session where nothing has run yet.
//
// Runs create it on their way past, so most of the time this only looks it up.
// Creating it here is what makes the pinned board work on a fresh machine,
// where the alternative is a board opened into the user's workspace — and it is
// the same space the runs will use, because both go through the record bermuda
// keeps of the one it made.
func bermudaWorkspace(ctx context.Context, h *herdrcli.Client) (string, error) {
	ws, err := runner.EnsureWorkspace(ctx, h, stateDir(), os.Getenv("HOME"))
	if err != nil {
		return "", err
	}
	return ws.WorkspaceID, nil
}

// openBoardElsewhere draws the board in a Herdr pane on behalf of a shell that
// has no terminal of its own — an agent's `bermuda board`, most often.
//
// Failing here with Bubble Tea's "could not open a new TTY" told the caller
// only that something was wrong with a device it never asked about, so an agent
// told to open the board would try it again, differently, forever.
func openBoardElsewhere() error {
	if !inHerdr() {
		return fmt.Errorf("board draws a terminal UI and this shell has no TTY: run it in a terminal, or from a herdr session, where it opens in its own pane")
	}
	pane := boardPaneFor(os.Getenv("HERDR_PANE_ID"), os.Getenv("HERDR_WORKSPACE_ID"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := herdrcli.New().OpenPluginPane(ctx, pane); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "bermuda: no TTY here — opened the board as a herdr %s instead\n", pane.Placement)
	return nil
}
