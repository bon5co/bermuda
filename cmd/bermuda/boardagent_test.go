package main

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
)

// The row carries its counts in words and stays idle whatever they are. A
// colour that is on permanently — which is what blocked became, with parked
// runs from last week still parked — is not a signal, so the state is one thing
// this mapping is not allowed to vary.
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
			name:          "a run in flight says so, and stays idle",
			running:       2,
			wantState:     herdrcli.StatusIdle,
			wantContained: "2 running",
		},
		{
			// This is the case that used to report blocked, and the reason it
			// no longer does: parked runs accumulate and never clear
			// themselves, so the red dot was permanent within a week.
			name:          "a parked run is counted, not coloured",
			parked:        1,
			wantState:     herdrcli.StatusIdle,
			wantContained: "1 parked",
		},
		{
			// Parked is named first: it is the one that needs a person, and
			// work still moving does not make a run that stopped for a question
			// any less stopped.
			name:          "parked is named before running",
			parked:        1,
			running:       3,
			wantState:     herdrcli.StatusIdle,
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

// Whatever the store holds, the dot is green. Stated as its own test because it
// is a decision about what the sidebar is for — a place to look, not an alarm —
// and a future change to the counts must not quietly reintroduce the colour.
func TestBoardAgentStateIsNeverAnAlarm(t *testing.T) {
	for _, c := range [][2]int{{0, 0}, {0, 5}, {9, 0}, {9, 5}} {
		state, _ := boardAgentState(c[0], c[1])
		if state != herdrcli.StatusIdle {
			t.Errorf("parked=%d running=%d reported %q, want idle", c[0], c[1], state)
		}
	}
}
