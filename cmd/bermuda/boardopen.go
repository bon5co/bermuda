package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
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
