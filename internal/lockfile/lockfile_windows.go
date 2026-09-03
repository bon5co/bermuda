//go:build windows

package lockfile

import (
	"os"

	"golang.org/x/sys/windows"
)

// errWouldBlock is the error a non-blocking lock attempt returns when another
// process already holds the lock. On Windows a LockFileEx that fails
// immediately reports ERROR_LOCK_VIOLATION.
var errWouldBlock error = windows.ERROR_LOCK_VIOLATION

// Windows file locks are mandatory, not advisory: a locked byte range cannot be
// read by another handle. The pid text is written at offset 0 and read
// cross-process for diagnostics and for `bermuda stop`, so the lock is placed
// on a single byte far past it, well beyond any pid ever written. Locking past
// end-of-file is allowed and needs no allocation.
const (
	lockOffsetLow  = 0
	lockOffsetHigh = 1 // byte 1<<32; the file itself never grows near this
	lockBytesLow   = 1
	lockBytesHigh  = 0
)

// lockFileNB takes an exclusive lock without blocking. Windows releases the
// lock when the owning process exits and its handle is closed, so — as with
// flock on Unix — a crashed daemon leaves no stale lock behind.
func lockFileNB(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockBytesLow, lockBytesHigh, ol,
	)
	if err == windows.ERROR_LOCK_VIOLATION {
		return errWouldBlock
	}
	return err
}

// unlockFile releases the lock held on f.
func unlockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockBytesLow, lockBytesHigh, ol)
}
