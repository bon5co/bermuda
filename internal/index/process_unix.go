//go:build unix

package index

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// A helper gets its own process group, including uv's Python child. Cancellation
// must stop the whole group before waiting for inherited output pipes to close.
func configureHelperProcess(cmd *exec.Cmd) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stop := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = stop
	return func() { _ = stop() }
}
