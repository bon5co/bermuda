//go:build unix

package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Model the uv -> Python boundary without optional Python/model dependencies.
// The child inherits stderr, so stopping only the wrapper leaves Run blocked.
func TestHelperCancellationTerminatesProcessGroup(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent_cancel", true: "deadline"}[deadline], func(t *testing.T) {
			dir := t.TempDir()
			pidPath := filepath.Join(dir, "child.pid")
			child, wrapper := filepath.Join(dir, "child"), filepath.Join(dir, "wrapper")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			if err := os.WriteFile(child, []byte("#!/bin/sh\necho $$ > "+quote(pidPath)+"\nexec sleep 30\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nsh "+quote(child)+" &\nwait\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BERMUDA_INDEX_PYTHON", wrapper)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := time.Minute
			if deadline {
				timeout = 2 * time.Second
			}
			done := make(chan error, 1)
			go func() {
				_, err := New(filepath.Join(dir, "index"), dir).call(ctx, timeout, map[string]any{"op": "stats"})
				done <- err
			}()
			pid := 0
			defer func() {
				cancel()
				if pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}()
			ready := time.Now().Add(time.Second)
			for time.Now().Before(ready) {
				if b, err := os.ReadFile(pidPath); err == nil {
					pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("helper child never started")
			}
			want := context.DeadlineExceeded
			if !deadline {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("helper error = %v, want %v", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("helper remained blocked on descendant's stderr")
			}
			state, err := exec.Command("ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
			if err == nil && strings.TrimSpace(string(state)) != "" && !strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
				t.Fatalf("cancelled helper child still executing: pid %d, state %s", pid, state)
			}
		})
	}
}
