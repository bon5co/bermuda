package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bon5co/bermuda/internal/flow"
	"github.com/bon5co/bermuda/internal/store"
)

// What the board's FLOWS tab does, kept here in the command layer where flows
// already live so the board never imports this package.
//
// They are deliberately the same two operations `bermuda flow run` and
// `bermuda flow resume` perform, against the same store and the same run
// directories: a flow started from the board has to be indistinguishable from
// one started by hand, or the board becomes a second way of doing it with its
// own bugs.

// startFlowFromBoard calls a flow with an input.
func startFlowFromBoard(s *store.Store, flowID, input string) error {
	def, err := flow.Load(flowDir(), flowID)
	if err != nil {
		return err
	}
	// The same refusal `flow run` makes, and the one that counts: the board asks
	// for an input before it gets here, but a flow whose prompts say {{input}}
	// started with a blank hands every agent in the sequence a hole where its
	// subject should be, and an agent handed that will invent something.
	if def.TakesInput() && strings.TrimSpace(input) == "" {
		return fmt.Errorf("flow %s needs an input: %s", def.ID, def.Input)
	}
	// The board's own working directory, which is the directory the pane was
	// opened in — the same thing `flow run` with no --cwd uses. A flow declares
	// no directory of its own on purpose: that belongs to whoever calls it.
	dir, err := os.Getwd()
	if err != nil {
		dir = ""
	}
	rec := store.Run{
		ID: newRunID(def.ID), JobID: def.ID, Trigger: "manual",
		Outcome: "running", StartedAt: time.Now(),
		Flow: def.ID, Input: input,
	}
	_, err = runFlow(context.Background(), s, flowJob(def, dir, "", ""), rec)
	return err
}

// resumeFlowRun picks a parked flow run up at the step that stopped it.
func resumeFlowRun(s *store.Store, runID string) error {
	ctx := context.Background()
	rec, err := s.Run(ctx, runID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(rec.Flow) == "" {
		return fmt.Errorf("run %s is not a flow run; there is nothing to resume", rec.ID)
	}
	// The job is optional. A flow called from the board has none at all, and one
	// called by a job may outlive it — but the run records which flow ran and
	// what it was called with, so neither case needs the job to still exist.
	j := store.Job{ID: rec.JobID, Flow: rec.Flow, Enabled: true, Model: store.DefaultModel}
	if stored, err := s.Job(ctx, rec.JobID); err == nil {
		j = *stored
	}
	_, err = runFlow(ctx, s, j, *rec)
	return err
}
