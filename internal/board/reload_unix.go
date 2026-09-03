//go:build unix

package board

import (
	"os"
	"syscall"
)

// restart execs the new binary in place, preserving arguments and environment.
// It only returns if the exec fails.
func (w *binaryWatcher) restart() error {
	return syscall.Exec(w.path, os.Args, os.Environ())
}
