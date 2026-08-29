package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/bon5co/bermuda/v3/internal/herdrcli"
	"github.com/bon5co/bermuda/v3/internal/runner"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// Bermuda's own row in Herdr's sidebar.
//
// The sidebar has two sections, agents and spaces, and no plugin surface to add
// a third: a plugin can publish panes and actions, and both need a key bound or
// a menu opened before anything of bermuda's is on screen. Close the board and
// nothing in the window says bermuda exists at all.
//
// The agents section is the way in, because `pane report-agent` takes the agent
// label as a free-form string. The board reports its own pane as the agent
// "bermuda", and Herdr draws it in the list above Spaces, where it is one click
// from wherever the person happens to be.
//
// The state is the half that earns its keep. A parked run is a run waiting on a
// human, and a human who cannot see it waiting is precisely the failure this
// harness exists to remove — so parked runs report blocked, which is the state
// Herdr highlights as needing attention. The row asks to be looked at on its
// own, without a notification anybody has to have configured.
const (
	// boardAgentLabel lives in herdrcli because two packages need to agree on
	// it: this one claims the pane under it, and mention delivery uses it to
	// refuse to prompt what it identifies.
	boardAgentLabel = herdrcli.BoardAgent
	// boardAgentInterval is how often the row's state is re-derived. It reads
	// two indexed queries against a local sqlite file and is left running for
	// as long as the board is open, so it is deliberately unhurried.
	boardAgentInterval = 5 * time.Second
	// boardAgentTimeout bounds a single report. A wedged herdr must not hold
	// the board's shutdown open.
	boardAgentTimeout = 3 * time.Second
)

// boardPresence keeps the board's pane in Herdr's agent list while it runs.
//
// A zero-value presence is a working no-op, which is how the board stays
// runnable outside Herdr: with no pane to report there is nothing to claim and
// nothing to release, and every method is still safe to call.
type boardPresence struct {
	herdr *herdrcli.Client
	store *store.Store
	pane  string
	seq   atomic.Uint64
	stop  chan struct{}
	done  chan struct{}
}

// startBoardPresence claims the board's pane and keeps the claim current.
//
// Failure here is reported to nobody on purpose: the row is a convenience, and
// a board that refused to draw because Herdr would not take a label would trade
// the thing the person asked for against its own decoration.
func startBoardPresence(s *store.Store, h *herdrcli.Client) *boardPresence {
	p := &boardPresence{herdr: h, store: s, pane: os.Getenv("HERDR_PANE_ID")}
	if p.pane == "" {
		return p
	}
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	p.report()
	go p.loop()
	return p
}

// loop re-reports the state until the board exits.
func (p *boardPresence) loop() {
	defer close(p.done)
	t := time.NewTicker(boardAgentInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.report()
		}
	}
}

// report sends one state to Herdr.
func (p *boardPresence) report() {
	ctx, cancel := context.WithTimeout(context.Background(), boardAgentTimeout)
	defer cancel()

	state, message := p.state(ctx)
	_ = p.herdr.ReportPaneAgent(ctx, p.pane, herdrcli.PaneAgent{
		Source:  runner.PluginSource,
		Agent:   boardAgentLabel,
		State:   state,
		Message: message,
		Seq:     p.seq.Add(1),
	})
}

// state is what the row should say right now.
func (p *boardPresence) state(ctx context.Context) (herdrcli.AgentStatus, string) {
	// A store that cannot be read is still worth a row: the board is open, and
	// unknown is the state Herdr has for "attached, state not established".
	parked, err := p.store.Runs(ctx, "parked", boardAgentCount)
	if err != nil {
		return herdrcli.StatusUnknown, ""
	}
	running, err := p.store.Runs(ctx, "running", boardAgentCount)
	if err != nil {
		return herdrcli.StatusUnknown, ""
	}
	return boardAgentState(len(parked), len(running))
}

// boardAgentCount bounds the two state queries. The row says how many, and a
// number this high is already "a lot" rather than a count anybody reads
// precisely, so the query is capped instead of asking for every run ever.
const boardAgentCount = 200

// boardAgentState maps what the store holds onto what the sidebar shows.
//
// The row stays idle — a green dot — whatever the store holds, and says the
// counts in words instead. Parked runs used to report blocked, which Herdr
// draws in red: correct on the day a run parks and useless a week later, when
// four parked runs nobody has got to yet leave the row permanently red and red
// stops meaning anything. A colour that is always on is not a signal.
//
// The counts are still the point of the row, so they stay: parked is named
// first because it is the one that needs a person, running after it.
func boardAgentState(parked, running int) (herdrcli.AgentStatus, string) {
	switch {
	case parked > 0:
		msg := fmt.Sprintf("%d parked", parked)
		if running > 0 {
			msg += fmt.Sprintf(" · %d running", running)
		}
		return herdrcli.StatusIdle, msg
	case running > 0:
		return herdrcli.StatusIdle, fmt.Sprintf("%d running", running)
	default:
		return herdrcli.StatusIdle, "no runs in flight"
	}
}

// Stop ends the loop and hands the pane's identity back.
//
// The release matters more than the reports do. A claim outlives the process
// that made it, so a board that exits without releasing leaves a bermuda row in
// the sidebar addressing a pane that is now somebody's shell.
func (p *boardPresence) Stop() {
	if p.pane == "" {
		return
	}
	close(p.stop)
	<-p.done

	ctx, cancel := context.WithTimeout(context.Background(), boardAgentTimeout)
	defer cancel()
	_ = p.herdr.ReleasePaneAgent(ctx, p.pane, runner.PluginSource, boardAgentLabel)
}
