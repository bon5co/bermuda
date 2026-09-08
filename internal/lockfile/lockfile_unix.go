//go:build unix

package lockfile

import (
	"os"
	"syscall"
)

// errWouldBlock is the error a non-blocking lock attempt returns when another
// process already holds the lock. On Unix that is EWOULDBLOCK from flock(2).
var errWouldBlock error = syscall.EWOULDBLOCK

// lockFileNB takes an exclusive advisory lock without blocking. The kernel
// drops it when the holding process exits for any reason, which is what lets a
// crashed daemon never leave a stale lock behind.
func lockFileNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile releases the lock held on f.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
