package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A stop has to outlive the thing that would undo it.
//
// Every automatic start goes through spawnRole — the plugin hook on each Herdr
// start, the board's refresh tick, and each process watching the other every
// five seconds. If the marker did not stop it there, `bermuda stop` would last
// until the next tick.
func TestSpawnRoleRefusesWhileStopped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)

	if stopped() {
		t.Fatal("a fresh state dir reads as stopped")
	}
	if err := os.WriteFile(stopFile(), []byte("2026-07-27T00:00:00Z\n"), 0o644); err != nil {
		t.Fatalf("write stop file: %v", err)
	}
	if !stopped() {
		t.Fatal("stop file written, but stopped() says otherwise")
	}

	// No process is started, and no error is raised: a refusal here is the
	// normal case, not a failure.
	if err := spawnRole(roleDaemon); err != nil {
		t.Fatalf("spawnRole while stopped: %v", err)
	}
	if _, err := os.Stat(lockPath(roleDaemon)); err == nil {
		t.Error("a daemon was started despite the stop marker")
	}
	if err := EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning while stopped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bermuda.log")); err == nil {
		t.Error("EnsureRunning started something despite the stop marker")
	}
}

// start forgets the stop, and spawning works again afterwards.
//
// This exercises what start does to the marker rather than start itself: the
// command goes on to spawn two detached processes, and a test that leaves
// those behind pointed at a deleted temp directory is worse than one that
// stops short of them.
func TestClearingTheStopMarkerLetsSpawningResume(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	if err := os.WriteFile(stopFile(), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write stop file: %v", err)
	}

	if err := clearStop(); err != nil {
		t.Fatalf("clearStop: %v", err)
	}
	if stopped() {
		t.Fatal("the marker survived clearStop")
	}
	if err := clearStop(); err != nil {
		t.Errorf("clearing an already-cleared stop should be a no-op: %v", err)
	}
}

// Stopping something that is not running still records the stop, so a later
// Herdr start does not quietly bring it back.
func TestStopWithNothingRunningStillSticks(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	if err := stopCmd(nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !stopped() {
		t.Error("stop with nothing running did not record anything")
	}
}
