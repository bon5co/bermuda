package main

import (
	"context"
	"os"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
)

// boardWideCmd opens the board as a full-width horizontal split.
//
// Herdr's manifest pane entry cannot express a split direction, so the wide
// layout is an action that asks Herdr to open the pane downward instead. Bind
// a key to it to get the wide board in one keystroke.
//
// The split divides this pane, which is why $HERDR_PANE_ID is the argument that
// decides: given a workspace alone Herdr refuses the split outright, so a shell
// with no pane of its own gets the board in a tab rather than nothing.
func boardWideCmd(argv []string) error {
	h := herdrcli.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return h.OpenPluginPane(ctx, boardPaneFor(os.Getenv("HERDR_PANE_ID"), os.Getenv("HERDR_WORKSPACE_ID")))
}
