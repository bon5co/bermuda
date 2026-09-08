// Package lockfile provides a single-instance guard.
//
// It uses an advisory file lock rather than a bare PID file: the kernel drops
// the lock when the holder exits for any reason, so a crashed or killed daemon
// cannot leave a stale lock that blocks every future start.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bon5co/bermuda/v3/internal/statefs"
)

// Lock is a held single-instance lock.
type Lock struct {
	f *os.File
}

// ErrHeld reports that another process holds the lock.
type ErrHeld struct {
	Path string
	PID  int
}

func (e *ErrHeld) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("already running (pid %d, lock %s)", e.PID, e.Path)
	}
	return fmt.Sprintf("already running (lock %s)", e.Path)
}

// Acquire takes the lock without blocking, returning *ErrHeld if another
// process holds it. The caller's PID is written for diagnostics only; the lock
// itself is what enforces exclusivity.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), statefs.Dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, statefs.File)
	if err != nil {
		return nil, err
	}
	if err := flockWithRetry(f); err != nil {
		pid := readPID(path)
		f.Close()
		if !errors.Is(err, errWouldBlock) {
			// errWouldBlock is the only error that means "somebody has it".
			// Reporting anything else (a lock-table full, an interrupted call)
			// as "already running" would silently leave the machine with no
			// scheduler at all.
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		return nil, &ErrHeld{Path: path, PID: pid}
	}
	// Truncate before writing: a shorter pid must not leave trailing digits
	// from a previous holder.
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. The file stays.
//
// Removing it is what made this dangerous: the lock lives on the inode, so
// unlinking it while another process was opening the same path left the two of
// them locking different inodes — both holders of "the" lock, which is the one
// thing this package exists to prevent. An empty lock file left behind costs
// nothing; the flock is what says whether anyone is running.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	l.f.Close()
	l.f = nil
	return err
}

// PIDOf reports the pid recorded in a lock file, or 0.
//
// For diagnostics and for signalling — never for deciding whether anything is
// running, which is what Held and the flock itself are for: a pid outlives the
// process that wrote it.
func PIDOf(path string) int { return readPID(path) }

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(b))
	return pid
}

// flockWithRetry takes the lock, retrying briefly.
//
// Held probes by taking the lock and dropping it again, and both watchdog
// processes probe every five seconds, so a start that collides with a probe
// would see EWOULDBLOCK against a lock nobody actually holds — and a daemon
// told "already running" exits, leaving nothing running. A few milliseconds of
// retry costs nothing on the path that matters, where the lock is genuinely
// held and stays held.
func flockWithRetry(f *os.File) error {
	var err error
	for attempt := range lockAttempts {
		if attempt > 0 {
			time.Sleep(lockRetryDelay)
		}
		err = lockFileNB(f)
		if err == nil || !errors.Is(err, errWouldBlock) {
			return err
		}
	}
	return err
}

const (
	lockAttempts   = 4
	lockRetryDelay = 25 * time.Millisecond
)

// Held reports whether some other process currently holds the lock.
//
// It answers by trying to take the lock and dropping it again, which is what
// makes it independent of any PID a crashed process may have left behind: the
// kernel drops an flock when its holder dies, whatever killed it.
//
// The probe is not free of consequence — for the moment it holds the lock, a
// concurrent Acquire would fail — which is why Acquire retries. It no longer
// deletes the lock file, which is what previously turned a probe into a way to
// end up with two holders.
func Held(path string) bool {
	// The directory has to exist before the lock file can. Acquire creates it;
	// Held has to as well, or the first probe on a fresh state directory fails
	// to open anything, reports "held", and the daemon it was asked about is
	// never started.
	if err := os.MkdirAll(filepath.Dir(path), statefs.Dir); err != nil {
		return true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, statefs.File)
	if err != nil {
		// Nothing can be said about a lock that cannot be opened; claiming it
		// is free would start a second daemon.
		return true
	}
	defer f.Close()
	if err := lockFileNB(f); err != nil {
		return errors.Is(err, errWouldBlock)
	}
	_ = unlockFile(f)
	return false
}
