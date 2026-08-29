package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A pair whose state directory has been deleted has no off switch: `bermuda
// stop` writes its flag into the store the *caller* names, so the flag for a
// deleted store cannot be written, and each half revives the other every tick
// regardless. The only thing that can end such a pair is the pair itself.
func TestWatchPeerExitsOnceItsStateDirIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the state dir: %v", err)
	}
	t.Setenv("BERMUDA_STATE_DIR", dir)
	shortenWatchInterval(t)

	// Hold the peer's lock so the watch has no reason to spawn anything: this
	// test is about the exit, not about revival.
	peerLock, err := lockfile.Acquire(lockPath(roleDaemon))
	if err != nil {
		t.Fatalf("take the daemon lock: %v", err)
	}
	defer peerLock.Release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quit := make(chan struct{})

	watch := newPeerWatch(roleSentinel)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove the state dir: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watch.run(ctx, func() { close(quit) })
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the watch kept running after its state directory was deleted: nothing could ever stop this pair")
	}
	select {
	case <-quit:
	default:
		t.Error("the watch returned without cancelling its own process, so the daemon's run loop would carry on")
	}
}

// The exit must not fire on a store that has never existed. Bermuda's first
// start races whatever creates the directory, and a process that read its own
// head start as abandonment would refuse to run on a fresh machine.
func TestWatchPeerStaysUpWhenTheStateDirWasNeverThere(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created-yet")
	t.Setenv("BERMUDA_STATE_DIR", missing)
	shortenWatchInterval(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quit := make(chan struct{})

	watch := newPeerWatch(roleSentinel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watch.run(ctx, func() { close(quit) })
	}()

	select {
	case <-done:
		t.Fatal("the watch gave up on a state dir that had never been created; a fresh install would never start")
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case <-quit:
		t.Error("the watch cancelled its process over a directory that was never there")
	default:
	}
	cancel()
	<-done
}

// abandoned() is the whole rule in one place: gone only counts once seen.
func TestAbandonedOnlyCountsAStoreThatWasSeen(t *testing.T) {
	cases := []struct {
		name                string
		seen, present, want bool
	}{
		{"store still there", true, true, false},
		{"store seen, then deleted", true, false, true},
		{"never seen, still absent", false, false, false},
		{"never seen, now created", false, true, false},
	}
	for _, tc := range cases {
		if got := abandoned(tc.seen, tc.present); got != tc.want {
			t.Errorf("%s: abandoned(%v, %v) = %v, want %v", tc.name, tc.seen, tc.present, got, tc.want)
		}
	}
}

// shortenWatchInterval makes the pair tick fast enough to observe in a test,
// and restores the production interval afterwards.
func shortenWatchInterval(t *testing.T) {
	t.Helper()
	previous := watchInterval
	watchInterval = 10 * time.Millisecond
	t.Cleanup(func() { watchInterval = previous })
}

// Nothing may detach a pair against a throwaway store.
//
// This is the other half of the leak whose exit is tested above. A detached
// pair outlives its caller, and a store under a temp directory is removed the
// moment that caller finishes — leaving two processes that revive each other
// forever and cannot be reached by `bermuda stop`, which writes its flag into
// a directory that no longer exists. The pair can now end itself once the
// store is deleted; this is what stops one being created in the first place.
func TestSpawnRoleRefusesAScratchStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)

	err := spawnRole(roleDaemon)
	if err == nil {
		t.Fatal("a detached daemon was started against a temp state dir: nothing would ever stop it")
	}
	if !strings.Contains(err.Error(), allowScratchDaemonEnv) {
		t.Errorf("the refusal %q does not say how to override it", err)
	}
	if lockfile.Held(lockPath(roleDaemon)) {
		t.Error("something is holding the daemon lock, so a process was started after all")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "bermuda.log")); statErr == nil {
		t.Error("the refusal still opened the log, so it happened after the side effects")
	}

	// Both halves are refused, not just the one that runs jobs: a lone sentinel
	// against a deleted store revives a daemon every tick and is as unreachable.
	if err := spawnRole(roleSentinel); err == nil {
		t.Error("a detached sentinel was started against a temp state dir")
	}
}

// A caller that means it says so once, in the environment, and is obeyed.
func TestScratchRefusalCanBeOverridden(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	if scratchRefusal(roleDaemon) == nil {
		t.Fatal("a temp state dir was not refused")
	}
	t.Setenv(allowScratchDaemonEnv, "1")
	if err := scratchRefusal(roleDaemon); err != nil {
		t.Errorf("%s=1 was set and the start was still refused: %v", allowScratchDaemonEnv, err)
	}
}

// The real store is never refused. This is the failure that would matter most:
// a classification that caught ~/.bermuda would stop bermuda running at all.
func TestScratchRefusalLetsTheRealStoreThrough(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	t.Setenv("BERMUDA_STATE_DIR", filepath.Join(home, ".bermuda"))
	if err := scratchRefusal(roleDaemon); err != nil {
		t.Errorf("the real store was refused: %v", err)
	}
}

// The classification itself, stated as a table so the boundary is visible: a
// real home-directory store must never read as scratch, or the refusal would
// stop bermuda running at all.
func TestScratchStoreClassification(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"the real store", filepath.Join(home, ".bermuda"), false},
		{"a project directory", filepath.Join(home, "Projects", "bermuda", "state"), false},
		{"a go test temp dir", t.TempDir(), true},
		{"the temp root itself", os.TempDir(), true},
		{"under /tmp", "/tmp/bermuda-scratch", true},
		{"under /var/tmp", "/var/tmp/bermuda-scratch", true},
		{"a path that only looks like it", filepath.Join(home, "tmp", "bermuda"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scratchStore(tc.dir); got != tc.want {
				t.Errorf("scratchStore(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}
