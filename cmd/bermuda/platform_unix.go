//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// forwardedSignals are the signals `bermuda thread` relays to the child it
// supervises: an interrupt, a termination, and a hangup all mean "wind down",
// and each must reach the process actually doing the work.
var forwardedSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// terminatePID asks a process to shut down. SIGTERM lets the daemon finish the
// jobs it is running before it exits. A process that is already gone (ESRCH) is
// success, not failure.
func terminatePID(pid int) error {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// pidAlive reports whether a pid still names a live process. Signal 0 asks the
// kernel that question without delivering anything.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// detachSysProcAttr puts a spawned child in a new session so it outlives
// whatever started it.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// sessionLeaderPID reports the session leader's pid, which is stable across the
// several short-lived commands one login session runs.
func sessionLeaderPID() (int, bool) {
	if sid, err := unix.Getsid(0); err == nil && sid > 0 {
		return sid, true
	}
	return 0, false
}
