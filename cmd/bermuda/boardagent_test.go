package main

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/internal/herdrcli"
)

// The state the row reports is the whole reason it is worth having: an idle
// board and a board with a run waiting on a human look the same in the sidebar
// unless this mapping says otherwise.
func TestBoardAgentState(t *testing.T) {
	tests := []struct {
		name          string
		parked        int
		running       int
		wantState     herdrcli.AgentStatus
		wantContained string
	}{
		{
			name:          "nothing in flight is idle",
			wantState:     herdrcli.StatusIdle,
			wantContained: "no runs",
		},
		{
			name:          "a run in flight is working",
			running:       2,
			wantState:     herdrcli.StatusWorking,
			wantContained: "2 running",
		},
		{
			// blocked is the state herdr highlights, and a parked run is the
			// one case where the harness cannot continue without a person.
			name:          "a parked run asks for attention",
			parked:        1,
			wantState:     herdrcli.StatusBlocked,
			wantContained: "1 parked",
		},
		{
			// Parked outranks running: work still moving does not make a run
			// that stopped for a question any less stopped.
			name:          "parked outranks running",
			parked:        1,
			running:       3,
			wantState:     herdrcli.StatusBlocked,
			wantContained: "1 parked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, msg := boardAgentState(tt.parked, tt.running)
			if state != tt.wantState {
				t.Errorf("state = %q, want %q", state, tt.wantState)
			}
			if !strings.Contains(msg, tt.wantContained) {
				t.Errorf("message %q does not mention %q", msg, tt.wantContained)
			}
		})
	}
}

// A parked run and a running run at once must both be visible, so somebody
// reading the row knows whether answering the parked one is the only thing left.
func TestBoardAgentStateReportsBoth(t *testing.T) {
	_, msg := boardAgentState(2, 4)
	if !strings.Contains(msg, "2 parked") || !strings.Contains(msg, "4 running") {
		t.Errorf("message %q should carry both counts", msg)
	}
}

// Outside herdr there is no pane to claim. The board still has to run: this is
// the plain `bermuda board` in a terminal, which is how it works with no plugin
// installed at all.
func TestBoardPresenceWithoutPaneIsInert(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	p := startBoardPresence(nil, nil)
	if p.pane != "" {
		t.Fatalf("claimed pane %q with no HERDR_PANE_ID", p.pane)
	}
	// Stop must not touch the nil client, and must not block on a loop that
	// was never started.
	p.Stop()
}
