package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/runner"
	"github.com/bon5co/bermuda/internal/store"
)

// reconcileRuns resolves runs the store still believes are in flight.
//
// A run's outcome is recorded by the process that launched it, so anything that
// kills that process — a crash, an OOM, or a job that stops the scheduler —
// leaves the row stuck at "running" even though the work finished. That is not
// hypothetical: a scheduled job was told to stop the daemon to observe the
// board's unhealthy state, and killed the very process supervising its own run.
//
// The fix is to take the outcome from where it actually lives. result.json is
// written by the agent and outlives every process involved, so a stuck row can
// always be resolved after the fact:
//
//   - result.json readable  -> adopt its verdict, done or failed
//   - no result, agent gone -> park it as orphaned for a human
//   - no result, agent live -> genuinely still running; leave it alone
//
// The same argument applies one state later, to runs already filed as parked
// for want of a result: see reconcileParked.
func reconcileRuns(ctx context.Context, s *store.Store, h *herdrcli.Client) (int, error) {
	runs, err := s.Runs(ctx, "running", 200)
	if err != nil {
		return 0, err
	}

	fixed := 0
	for _, r := range runs {
		resolved, ok := resolveRun(ctx, r, h)
		if !ok {
			continue
		}
		if err := s.PutRun(ctx, resolved); err != nil {
			return fixed, err
		}
		fixed++
	}

	n, err := reconcileParked(ctx, s)
	return fixed + n, err
}

// reconcileParked re-reads the runs that were parked for want of a result.
//
// A park is a verdict about a moment: nothing was on disk when the process
// supervising the run looked. The agent outlives that process and keeps
// writing, so the verdict can be wrong by the time anyone reads it — one run
// was filed as "parked (no_result)" thirty seconds in while its agent went on
// working for another ten minutes and then wrote result.json. The file is the
// authority, so a park that the disk contradicts is corrected here rather than
// standing forever.
//
// Only parks that mean "no result was seen" are revisited. A park that means a
// human is needed — blocked — is left alone: the file appearing later does not
// undo the question the agent asked.
func reconcileParked(ctx context.Context, s *store.Store) (int, error) {
	runs, err := s.Runs(ctx, "parked", 200)
	if err != nil {
		return 0, err
	}
	fixed := 0
	for _, r := range runs {
		switch r.ParkReason {
		case "no_result", string(runner.ParkAgentLost), "timeout", "orphaned":
		default:
			continue
		}
		changed, err := reconcileOneParked(ctx, s, r)
		if err != nil {
			return fixed, err
		}
		if changed {
			fixed++
		}
	}
	return fixed, nil
}

// reconcileOneParked corrects a single parked run from what is on disk.
//
// A workflow run is judged by its steps, since that is where its results live:
// each step directory is re-read, any step the disk says succeeded is corrected,
// and the run itself only becomes done when every declared step has. A run
// parked with a step still genuinely unfinished stays parked — the steps after
// it never ran, and calling that done would be the same wrong answer in the
// other direction.
func reconcileOneParked(ctx context.Context, s *store.Store, r store.Run) (bool, error) {
	dir := r.RunDir
	if dir == "" {
		dir = runDirFor(r.ID)
	}

	steps, err := s.RunSteps(ctx, r.ID)
	if err != nil {
		return false, err
	}
	if len(steps) == 0 {
		res, err := runner.ReadResult(dir)
		if err != nil {
			return false, nil
		}
		r.RunDir = dir
		r.Outcome = "done"
		if res.Status != "ok" {
			r.Outcome = "failed"
		}
		r.ParkReason = ""
		if res.Note != "" {
			r.Note = res.Note
		}
		r.EndedAt = endedAt(dir)
		return true, s.PutRun(ctx, r)
	}

	changed := false
	done, latest := 0, time.Time{}
	for _, st := range steps {
		stepDir := st.StepDir
		if stepDir == "" {
			stepDir = runner.StepDir(dir, st.StepID)
		}
		res, readErr := runner.ReadResult(stepDir)
		ok := readErr == nil && res.Status == "ok"
		if ok {
			done++
			if end := endedAt(stepDir); end != nil && end.After(latest) {
				latest = *end
			}
		}
		if !ok || st.Outcome == store.StepDone {
			continue
		}
		// The disk says this step finished and the row says it did not. The row
		// is the one that was written by a process guessing.
		st.Outcome, st.ParkReason, st.StepDir = store.StepDone, "", stepDir
		if res.Note != "" {
			st.Note = res.Note
		}
		st.EndedAt = endedAt(stepDir)
		if err := s.PutRunStep(ctx, st); err != nil {
			return changed, err
		}
		changed = true
	}

	if done < len(steps) {
		return changed, nil
	}
	r.RunDir = dir
	r.Outcome, r.ParkReason = "done", ""
	r.Note = fmt.Sprintf("%d/%d steps ok", done, len(steps))
	if !latest.IsZero() {
		r.EndedAt = &latest
	}
	return true, s.PutRun(ctx, r)
}

// resolveRun decides what a stuck run really was, or reports that it is still
// legitimately in flight.
func resolveRun(ctx context.Context, r store.Run, h *herdrcli.Client) (store.Run, bool) {
	// Rows written before the run directory was recorded up front carry an
	// empty one, so fall back to where the runner always puts it.
	dir := r.RunDir
	if dir == "" {
		dir = runDirFor(r.ID)
	}
	if dir != "" {
		if res, err := runner.ReadResult(dir); err == nil {
			r.RunDir = dir
			r.Outcome = "done"
			if res.Status != "ok" {
				r.Outcome = "failed"
			}
			if res.Note != "" {
				r.Note = res.Note
			}
			// The result file's timestamp is when the work actually ended,
			// which is closer to the truth than the moment anyone noticed.
			r.EndedAt = endedAt(dir)
			return r, true
		}
	}

	// No result. If the agent is gone the run cannot finish, and cannot be
	// judged either — park it rather than guessing or silently rerunning.
	if r.AgentName == "" {
		return r, false
	}
	if _, err := h.AgentGet(ctx, r.AgentName); err != nil {
		now := time.Now()
		r.Outcome, r.ParkReason, r.EndedAt = "parked", "orphaned", &now
		return r, true
	}
	return r, false
}

// runDirFor is where a run's artifacts live, derived the same way the runner
// derives it.
func runDirFor(runID string) string {
	return filepath.Join(stateDir(), "runs", runID)
}

// endedAt is when the result file was written, falling back to now.
func endedAt(runDir string) *time.Time {
	if fi, err := os.Stat(filepath.Join(runDir, "result.json")); err == nil {
		t := fi.ModTime()
		return &t
	}
	t := time.Now()
	return &t
}

// reconcileOnStart resolves stuck runs as the daemon comes up.
//
// The sentinel restarts a dead daemon within seconds, so this is the first thing
// to run after the death that stranded a row — without it, a stuck run waits for
// the next Herdr restart to be noticed.
func reconcileOnStart(s *store.Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := reconcileRuns(ctx, s, herdrcli.New())
	if err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: reconcile:", err)
		return
	}
	if n > 0 {
		fmt.Printf("bermuda: resolved %d run(s) left in flight by a previous process\n", n)
	}
}
