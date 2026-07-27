package main

import (
	"context"
	"os"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
)

// tabTitle is what a bermuda-only tab is called.
const tabTitle = "Bermuda"

// nameOwnTab labels the tab as Bermuda when the board has it to itself.
//
// Herdr passes the tab and workspace through the environment for plugin
// commands, so this only does anything when the board was opened as a plugin
// pane. It renames only when the board is the sole pane in that tab: a tab
// shared with the user's own work is not bermuda's to rename, and a split-pane
// board sits in a tab that belongs to whatever else is in it.
func nameOwnTab(h *herdrcli.Client) {
	tabID := os.Getenv("HERDR_TAB_ID")
	paneID := os.Getenv("HERDR_PANE_ID")
	if tabID == "" || paneID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	panes, err := h.PaneList(ctx, os.Getenv("HERDR_WORKSPACE_ID"))
	if err != nil {
		return // cosmetic: a label is never worth failing the board over
	}
	sharing := 0
	for _, p := range panes {
		if p.TabID == tabID {
			sharing++
		}
	}
	if sharing != 1 {
		return
	}
	_ = h.TabRename(ctx, tabID, tabTitle)
}
