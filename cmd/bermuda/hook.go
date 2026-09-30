package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bon5co/bermuda/v3/internal/statefs"
	"github.com/bon5co/bermuda/v3/internal/store"
)

const hookTimeout = 30 * time.Second

// runSettledHook invokes one installed hook with a frozen event. It never
// changes a run row. A missing or non-executable hook is a deliberate skip.
func runSettledHook(ctx context.Context, dir string, e store.RunEvent, timeout time.Duration) (bool, error) {
	path, args, found, err := settledHookCommand(dir)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	if timeout <= 0 {
		timeout = hookTimeout
	}
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(hookCtx, path, args...)
	configureHookProcess(cmd)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(append(append([]byte(nil), e.Payload...), '\n'))
	var p struct {
		Run struct {
			JobID      string `json:"job_id"`
			Outcome    string `json:"outcome"`
			ParkReason string `json:"park_reason"`
			Ref        string `json:"ref"`
			RunDir     string `json:"run_dir"`
		} `json:"run"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return false, fmt.Errorf("event %d payload: %w", e.ID, err)
	}
	cmd.Env = append(os.Environ(),
		"BERMUDA_STATE_DIR="+dir,
		"BERMUDA_EVENT_ID="+strconv.FormatInt(e.ID, 10),
		"BERMUDA_RUN_ID="+e.RunID,
		"BERMUDA_JOB_ID="+p.Run.JobID,
		"BERMUDA_RUN_OUTCOME="+p.Run.Outcome,
		"BERMUDA_PARK_REASON="+p.Run.ParkReason,
		"BERMUDA_REF="+p.Run.Ref,
		"BERMUDA_RUN_DIR="+p.Run.RunDir)
	log, err := os.OpenFile(filepath.Join(dir, "hooks", "run-settled.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, statefs.File)
	if err != nil {
		return false, err
	}
	defer log.Close()
	if _, err := fmt.Fprintf(log, "event %d run %s at %s\n", e.ID, e.RunID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return false, err
	}
	cmd.Stdout, cmd.Stderr = log, log
	err = cmd.Run()
	if errors.Is(hookCtx.Err(), context.DeadlineExceeded) {
		return false, fmt.Errorf("hook timed out after %s: %w", timeout, hookCtx.Err())
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return false, ctx.Err()
	}
	if err != nil {
		return false, fmt.Errorf("hook event %d: %w", e.ID, err)
	}
	return false, nil
}
