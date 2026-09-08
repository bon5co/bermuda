//go:build windows

package board

import (
	"os"
	"os/exec"
)

// restart launches the rebuilt binary and ends this process. Windows has no
// exec-that-replaces-the-image, so the honest equivalent is to hand the current
// stdio, arguments, and environment to a fresh process and exit — the open pane
// comes back running new code. The terminal is already restored by the time
// this is called (see board.go), so os.Exit here leaves nothing half-drawn.
func (w *binaryWatcher) restart() error {
	cmd := exec.Command(w.path, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
