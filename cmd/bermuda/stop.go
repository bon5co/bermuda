package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bon5co/bermuda/v2/internal/lockfile"
)

// Stopping bermuda used to mean killing two processes inside the same five
// seconds, because each revives the other on a timer. Anything slower brought
// back whichever one you killed first. Software that starts itself from a
// plugin hook and spawns detached children has to have an off switch.
//
// The switch is a file. Both roles refuse to start while it exists, so the
// answer survives the board's refresh tick, the plugin startup hook, and a
// Herdr restart — the three things that would otherwise put the scheduler
// straight back.

// stopFile is the marker that says bermuda was stopped on purpose.
func stopFile() string { return filepath.Join(stateDir(), "stopped") }

// stopped reports whether the scheduler has been stopped by hand.
func stopped() bool {
	_, err := os.Stat(stopFile())
	return err == nil
}

// stopCmd stops the scheduler and keeps it stopped.
func stopCmd(argv []string) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	// Written first: whatever is signalled below must find it already there,
	// or a peer noticing the death mid-shutdown would revive it.
	if err := os.WriteFile(stopFile(), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}

	var signalled []string
	for _, role := range []string{roleSentinel, roleDaemon} {
		pid := lockfile.PIDOf(lockPath(role))
		if pid <= 0 || !lockfile.Held(lockPath(role)) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stop %s (pid %d): %w", role, pid, err)
		}
		signalled = append(signalled, fmt.Sprintf("%s (pid %d)", role, pid))
	}

	if len(signalled) == 0 {
		fmt.Println("bermuda: nothing was running; it will stay stopped")
		return nil
	}
	for _, s := range signalled {
		fmt.Println("bermuda: stopping", s)
	}

	// A daemon shutting down waits for its running jobs, and a job can take as
	// long as its timeout — so this reports what is still going rather than
	// waiting on it or pretending it is gone.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !lockfile.Held(lockPath(roleDaemon)) && !lockfile.Held(lockPath(roleSentinel)) {
			fmt.Println("bermuda: stopped")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("bermuda: still finishing a run; it will exit when that run does, and will not restart")
	return nil
}

// clearStop forgets a stop, so the pair may start again.
func clearStop() error {
	if err := os.Remove(stopFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// startCmd undoes a stop and brings the pair back.
func startCmd(argv []string) error {
	if err := clearStop(); err != nil {
		return err
	}
	if err := EnsureRunning(); err != nil {
		return err
	}
	fmt.Println("bermuda: scheduler running")
	return nil
}
