package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/lockfile"
)

// acquireWithin is what keeps two processes from creating two workspaces on a
// fresh machine. Everything below is about the waiting, because the failure it
// prevents is silent: whoever gives up early creates a second space, and every
// run after that opens its tabs in whichever one it found.

// A free lock is taken at once, without spending the wait.
func TestAcquireWithinTakesAFreeLockImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.lock")

	start := time.Now()
	lock, err := acquireWithin(path, time.Second)
	if err != nil {
		t.Fatalf("acquireWithin on a free lock: %v", err)
	}
	defer lock.Release()

	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waited %s for a lock nobody held", elapsed)
	}
}

// A lock held by somebody else is waited for, not failed on. The caller only
// hears about it once the wait is spent, and the answer it gets has to be the
// held error itself: EnsureWorkspace tells "somebody is creating it" apart from
// "this lock is broken" by that error, and reporting the wrong one either
// creates a second space or refuses to create the first.
func TestAcquireWithinWaitsAndThenReportsWhoHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.lock")

	held, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatalf("seeding the lock: %v", err)
	}
	defer held.Release()

	wait := 300 * time.Millisecond
	start := time.Now()
	lock, err := acquireWithin(path, wait)
	elapsed := time.Since(start)

	if err == nil {
		lock.Release()
		t.Fatal("acquireWithin took a lock another holder still has")
	}
	var busy *lockfile.ErrHeld
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v (%T), want *lockfile.ErrHeld", err, err)
	}
	if elapsed < wait {
		t.Errorf("gave up after %s, before the %s wait was spent", elapsed, wait)
	}
}

// The wait is a bound, not a queue: a caller that waits forever is a daemon
// that never starts. Generous upper bound — this asserts that the deadline is
// honoured at all, not how precisely it is timed.
func TestAcquireWithinGivesUpAtItsDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.lock")

	held, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatalf("seeding the lock: %v", err)
	}
	defer held.Release()

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		if lock, err := acquireWithin(path, 100*time.Millisecond); err == nil {
			lock.Release()
		}
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed > 3*time.Second {
			t.Errorf("returned after %s: the wait is not bounded by its deadline", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquireWithin never returned: it waits past its deadline")
	}
}

// The point of waiting: a holder that finishes hands the lock over rather than
// making the waiter fail. This is the case that stops the second workspace from
// being created while the first one is halfway through being made.
func TestAcquireWithinTakesTheLockOnceTheHolderReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.lock")

	held, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatalf("seeding the lock: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		held.Release()
	}()

	lock, err := acquireWithin(path, 5*time.Second)
	if err != nil {
		t.Fatalf("acquireWithin gave up on a lock that was released: %v", err)
	}
	lock.Release()
}

// A lock that cannot be taken for a reason other than another holder is an
// error, not something to wait out. A directory in place of the lock file is
// the readily reproducible version of that: waiting the full deadline on it
// would stall every start on a machine where the state directory is wrong.
func TestAcquireWithinDoesNotWaitOutAnUnusableLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.lock")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("preparing an unusable lock path: %v", err)
	}

	start := time.Now()
	lock, err := acquireWithin(path, 3*time.Second)
	if err == nil {
		lock.Release()
		t.Fatal("acquireWithin claimed a lock whose path is a directory")
	}
	var busy *lockfile.ErrHeld
	if errors.As(err, &busy) {
		t.Errorf("err = %v: an unusable lock was reported as another holder", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("spent %s waiting out an error that will never clear", elapsed)
	}
}
