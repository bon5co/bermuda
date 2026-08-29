package lockfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireCreatesMissingDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "bermuda.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
}

func TestAcquireWritesOwnPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bermuda.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("lock file %q is not a pid: %v", b, err)
	}
	if pid != os.Getpid() {
		t.Fatalf("lock records pid %d, want %d", pid, os.Getpid())
	}
}

// A longer pid from a previous holder must not leave trailing digits behind:
// the next reader would parse a pid that never existed.
func TestAcquireTruncatesPreviousContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bermuda.lock")
	if err := os.WriteFile(path, []byte("99999999999999999999"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if got, want := string(b), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock contents %q, want exactly %q", got, want)
	}
}

func TestAcquireRefusesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bermuda.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer first.Release()

	second, err := Acquire(path)
	if err == nil {
		second.Release()
		t.Fatal("second Acquire succeeded while the lock was held")
	}
	var held *ErrHeld
	if !errors.As(err, &held) {
		t.Fatalf("second Acquire error is %T (%v), want *ErrHeld", err, err)
	}
	if held.Path != path {
		t.Errorf("ErrHeld.Path = %q, want %q", held.Path, path)
	}
	if held.PID != os.Getpid() {
		t.Errorf("ErrHeld.PID = %d, want the holder's pid %d", held.PID, os.Getpid())
	}
	if !strings.Contains(held.Error(), path) {
		t.Errorf("ErrHeld.Error() = %q, want it to mention the lock path", held.Error())
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bermuda.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// The file stays. Release used to unlink it, which let a probe delete the
	// lock another process was in the middle of opening — two processes then
	// held flocks on two different inodes and both believed they were the only
	// one. What says "nobody is running" is the flock, not the file.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file gone after Release: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	second.Release()
}

func TestReleaseIsIdempotentAndNilSafe(t *testing.T) {
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("Release on nil *Lock: %v", err)
	}
	path := filepath.Join(t.TempDir(), "bermuda.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bermuda.lock")
	if Held(path) {
		t.Fatal("Held reported true for an untouched path")
	}
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !Held(path) {
		t.Fatal("Held reported false while the lock was held")
	}
	lock.Release()
	if Held(path) {
		t.Fatal("Held reported true after Release")
	}
}

// Held must not disturb the file it probes: it acquires and releases, and its
// Release removes the file. A probe that deletes a live holder's lock would let
// a second daemon start.
func TestHeldDoesNotRemoveALiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bermuda.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	Held(path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Held removed the held lock file: %v", err)
	}
	if !Held(path) {
		t.Fatal("lock no longer reported held after a probe")
	}
}

// The whole reason for an advisory lock over a PID file: a holder that dies
// without cleaning up must not block the next start.
func TestLockIsReleasedWhenHolderDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bermuda.lock")

	holder := exec.Command(os.Args[0], "-test.run=TestHelperHoldLock")
	holder.Env = append(os.Environ(), "BERMUDA_TEST_LOCK_PATH="+path)
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := holder.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	buf := make([]byte, 16)
	if _, err := stdout.Read(buf); err != nil {
		holder.Process.Kill()
		holder.Wait()
		t.Fatalf("holder never signalled that it had the lock: %v", err)
	}

	if !Held(path) {
		holder.Process.Kill()
		holder.Wait()
		t.Fatal("lock not held while the child process holds it")
	}

	// SIGKILL: no defers run, no cleanup, exactly the crash case.
	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	holder.Wait()

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after the holder was killed: %v", err)
	}
	lock.Release()
}

// TestHelperHoldLock is not a test: it is the child process for
// TestLockIsReleasedWhenHolderDies. It holds the lock and blocks forever.
func TestHelperHoldLock(t *testing.T) {
	path := os.Getenv("BERMUDA_TEST_LOCK_PATH")
	if path == "" {
		t.Skip("helper process only")
	}
	if _, err := Acquire(path); err != nil {
		t.Fatalf("helper Acquire: %v", err)
	}
	os.Stdout.WriteString("locked\n")
	select {}
}
