package index

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// taskkill is provided by Windows and /T confines cleanup to this helper's tree.
// Keep cleanup bounded and fall back to stopping the direct process on failure.
func configureHelperProcess(cmd *exec.Cmd) func() {
	stop := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.Cancel = stop
	return func() { _ = stop() }
}
