package main

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/internal/herdrcli"
)

// A board asked for from a shell that has a pane divides that pane; one asked
// for from a shell that has none still has to land somewhere, and a tab is the
// placement Herdr will open without a target.
func TestBoardPaneFor(t *testing.T) {
	split := boardPaneFor("w1:p3", "w1")
	if split.Placement != "split" || split.Direction != "down" {
		t.Errorf("want a downward split, got %q %q", split.Placement, split.Direction)
	}
	if split.TargetPane != "w1:p3" {
		t.Errorf("target pane = %q, want w1:p3", split.TargetPane)
	}
	if split.Workspace != "" {
		t.Errorf("workspace = %q, want it left to the target pane", split.Workspace)
	}

	tab := boardPaneFor("", "w1")
	if tab.Placement != "tab" {
		t.Errorf("placement = %q, want tab", tab.Placement)
	}
	if tab.TargetPane != "" {
		t.Errorf("target pane = %q, want none", tab.TargetPane)
	}
	if tab.Workspace != "w1" {
		t.Errorf("workspace = %q, want w1", tab.Workspace)
	}

	for name, pane := range map[string]herdrcli.PluginPane{"split": split, "tab": tab} {
		if pane.Plugin != boardPlugin || pane.Entrypoint != boardEntrypoint {
			t.Errorf("%s pane names %q/%q", name, pane.Plugin, pane.Entrypoint)
		}
	}
}

// Outside a Herdr session there is no pane to open the board in, so it says
// what is missing rather than asking a server that is not running.
func TestOpenBoardElsewhereOutsideHerdr(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_ENV", "")
	if inHerdr() {
		t.Fatal("inHerdr with both variables cleared")
	}
	err := openBoardElsewhere()
	if err == nil {
		t.Fatal("want an error with no herdr to open a pane in")
	}
	if got := err.Error(); !strings.Contains(got, "no TTY") {
		t.Errorf("error %q does not say what is missing", got)
	}
}
