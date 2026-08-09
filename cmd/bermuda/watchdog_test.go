package main

import (
	"path/filepath"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/lockfile"
)

// The pair only recovers if each side watches the other one.
//
// peerOf answers "who do I revive". If both roles ever answered the same name,
// the pair would still look healthy — two processes, two locks, one of them
// watched twice — while the other role stayed dead with nothing reporting it.
func TestEachRoleWatchesTheOther(t *testing.T) {
	if got := peerOf(roleDaemon); got != roleSentinel {
		t.Errorf("the daemon watches %q, want %q", got, roleSentinel)
	}
	if got := peerOf(roleSentinel); got != roleDaemon {
		t.Errorf("the sentinel watches %q, want %q", got, roleDaemon)
	}
	for _, role := range []string{roleDaemon, roleSentinel} {
		if peerOf(role) == role {
			t.Errorf("%s watches itself, so nothing would revive it", role)
		}
		if back := peerOf(peerOf(role)); back != role {
			t.Errorf("%s's peer watches %q, not %s: the watch is not mutual", role, back, role)
		}
	}
}

// Each role holds its own lock, in the state directory the process was told to
// use. A shared path would make the second role exit as a duplicate of the
// first, leaving one process where the design needs two.
func TestRolesHoldDistinctLocksUnderTheStateDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)

	daemon, sentinel := lockPath(roleDaemon), lockPath(roleSentinel)
	if daemon == sentinel {
		t.Fatalf("both roles lock %q, so only one of them can run", daemon)
	}
	for _, p := range []string{daemon, sentinel} {
		if got := filepath.Dir(p); got != dir {
			t.Errorf("lock %q lives in %q, not the state dir %q", p, got, dir)
		}
	}
}

// Running() reads the daemon's lock, not the sentinel's. The sentinel opens no
// database and evaluates no schedules, so a sentinel-only machine that reported
// itself as running would be a scheduler that silently runs nothing.
func TestRunningReportsTheDaemonNotTheSentinel(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	if Running() {
		t.Fatal("a fresh state dir reports the scheduler as running")
	}

	sentinel, err := lockfile.Acquire(lockPath(roleSentinel))
	if err != nil {
		t.Fatalf("take the sentinel lock: %v", err)
	}
	if Running() {
		t.Error("a sentinel alone reports the scheduler as running; nothing would evaluate a schedule")
	}
	if err := sentinel.Release(); err != nil {
		t.Fatalf("release the sentinel lock: %v", err)
	}

	daemon, err := lockfile.Acquire(lockPath(roleDaemon))
	if err != nil {
		t.Fatalf("take the daemon lock: %v", err)
	}
	defer daemon.Release()
	if !Running() {
		t.Error("the daemon lock is held and the scheduler still reads as down")
	}
}
