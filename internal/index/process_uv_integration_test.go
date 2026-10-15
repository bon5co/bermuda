//go:build unix

package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run alone: temporarily replaces the embedded source in memory, never a
// production helper. Uses the actual resolved uv argv, Python and MiniLM ONNX
// inference. The only added behavior is a PID handshake and repeat inference
// so cancellation deterministically lands while the child is working.
func TestRealUvCancellationStopsInferenceAndReturns(t *testing.T) {
	if os.Getenv("BERMUDA_REAL_INDEX_TEST") != "1" {
		t.Skip("set BERMUDA_REAL_INDEX_TEST=1 for real uv/Chroma/MiniLM cancellation")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix process liveness assertion")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Fatal("real uv required: ", err)
	}
	t.Setenv("BERMUDA_INDEX_PYTHON", "")
	t.Setenv("BERMUDA_INDEX_HELPER_SELFTEST", "")
	original := helperSource
	cacheDir := filepath.Join(os.Getenv("HOME"), ".cache")
	defer func() { helperSource = original }()
	probe := strings.Replace(string(original), `if __name__ == "__main__":`, "if False:", 1)
	probe += `
req = json.load(sys.stdin)
embedding = bounded_embedding_function()
embedding(["Cancellation readiness probe."])
Path(req["pid_file"]).write_text(str(os.getpid()))
sys.stderr.write("inference child ready\n")
sys.stderr.flush()
while True:
    embedding(["Repeated real MiniLM inference until parent cancellation."] * 32)
`
	helperSource = []byte(probe)
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent_cancel", true: "call_deadline"}[deadline], func(t *testing.T) {
			dir := t.TempDir()
			isolatedHome := t.TempDir()
			t.Setenv("HOME", isolatedHome)
			// uv and the pretrained model are read-only dependencies, outside
			// the isolated index. Keep their established cache locations.
			if err := os.Symlink(cacheDir, filepath.Join(isolatedHome, ".cache")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("UV_CACHE_DIR", filepath.Join(cacheDir, "uv"))
			t.Setenv("BERMUDA_STATE_DIR", filepath.Join(dir, "state"))
			pidPath := filepath.Join(dir, "child.pid")
			c := New(filepath.Join(dir, "index"), filepath.Join(dir, "vault"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 2 * time.Minute
			if deadline {
				timeout = 15 * time.Second
			}
			done := make(chan error, 1)
			go func() {
				_, err := c.call(ctx, timeout, map[string]any{"op": "query", "pid_file": pidPath})
				done <- err
			}()
			pid := 0
			defer func() {
				cancel()
				if pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("call remained stuck after test-owned inference child was killed")
				}
			}()
			readyBy := time.Now().Add(60 * time.Second)
			for time.Now().Before(readyBy) {
				if b, err := os.ReadFile(pidPath); err == nil {
					pid, err = strconv.Atoi(string(b))
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				select {
				case err := <-done:
					done <- err
					t.Fatalf("setup failed before real inference handshake: %v", err)
				default:
				}
				time.Sleep(20 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("no inference handshake")
			}
			if ps, err := exec.Command("ps", "-o", "pid=,ppid=,stat=,comm=", "-p", fmt.Sprint(pid)).CombinedOutput(); err == nil {
				t.Logf("before cancellation: %s", ps)
			}
			started := time.Now()
			if deadline {
				// The call deadline starts before import/model initialization.
				// Wait its entire ceiling plus one second, not a guessed import time.
				started = time.Now()
			} else {
				cancel()
			}
			grace := time.Second
			if deadline {
				grace += timeout
			}
			select {
			case err := <-done:
				done <- err
				if err == nil {
					t.Error("cancelled inference reported success")
				}
			case <-time.After(grace):
				t.Errorf("call has not returned %s after cancellation/deadline allowance; stderr pipe still held by Python", time.Since(started))
			}
			// A killed grandchild may remain a zombie until init reaps it.
			// Kill(pid, 0) alone races that normal cleanup; check execution
			// state, which still rejects the baseline's orphaned Rl process.
			state, psErr := exec.Command("ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
			stateText := strings.TrimSpace(string(state))
			if psErr == nil && stateText != "" && !strings.HasPrefix(stateText, "Z") {
				t.Errorf("real uv-launched inference child survives cancellation: pid %d state %s", pid, stateText)
			}
		})
	}
}
