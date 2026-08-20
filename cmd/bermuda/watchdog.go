package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bon5co/bermuda/v2/internal/lockfile"
	"github.com/bon5co/bermuda/v2/internal/statefs"
)

// Bermuda keeps two processes alive that watch each other: the scheduler,
// which runs jobs, and the sentinel, which does nothing but watch. Each holds
// its own lock and revives the other when that lock goes free.
//
// Two processes rather than one supervised by the system, because bermuda has
// to be self-contained: installing it must not mean installing a unit file.
// The pair covers what actually happens in practice — one process crashing,
// being killed, or wedging — while the case a system supervisor would add
// (nothing running at all after a reboot) is already covered by the plugin
// startup hook, and is not a real gap anyway: bermuda runs jobs *inside*
// Herdr, so with no Herdr there is nothing for a scheduler to do.
//
// The sentinel is deliberately tiny. It opens no database and evaluates no
// schedules, so the bugs that could take down the scheduler are not present in
// the process whose job is to restart it.

const (
	// watchInterval is how often each process checks on the other. This is a
	// liveness check against a lock file, not real work, so it stays cheap.
	watchInterval = 5 * time.Second

	roleDaemon   = "daemon"
	roleSentinel = "sentinel"
)

func lockPath(role string) string {
	return filepath.Join(stateDir(), role+".lock")
}

// peerOf returns the role each process is responsible for reviving.
func peerOf(role string) string {
	if role == roleDaemon {
		return roleSentinel
	}
	return roleDaemon
}

// watchPeer revives this process's counterpart whenever it goes missing.
//
// Both sides run this, which is what makes the pair mutually recovering: kill
// either one and the survivor brings it back.
func watchPeer(ctx context.Context, role string) {
	peer := peerOf(role)
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if lockfile.Held(lockPath(peer)) {
				continue
			}
			if err := spawnRole(peer); err != nil {
				fmt.Fprintf(os.Stderr, "bermuda: revive %s: %v\n", peer, err)
				continue
			}
			fmt.Printf("bermuda: %s was not running; restarted it\n", peer)
		}
	}
}

// spawnRole starts one of bermuda's background roles, detached.
//
// It is a no-op when that role is already running: the child takes the lock
// itself and exits immediately if it cannot, so a race between both processes
// noticing the same death cannot produce two survivors.
func spawnRole(role string, args ...string) error {
	if stopped() {
		// `bermuda stop` outranks every automatic start: the plugin hook on
		// each Herdr start, the board's refresh tick, and the peer watch. It
		// is undone by `bermuda start`, and by nothing else.
		return nil
	}
	if lockfile.Held(lockPath(role)) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(stateDir(), "bermuda.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, statefs.File)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, append([]string{role}, args...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// A new session, so the child outlives whatever spawned it: a startup hook
	// that Herdr reaps, a board pane that closes, or a peer that is itself
	// about to die.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// EnsureRunning starts both roles if they are not already up.
//
// Safe to call as often as anything likes: the locks decide. This is what the
// plugin startup hook, the board, and every peer check ultimately call.
func EnsureRunning() error { return ensureRunning() }

// ensureRunning is EnsureRunning with the scheduler's own flags, which only
// `daemon --detach` has: it is passing on what it was asked for rather than
// letting the background copy fall back to the defaults.
func ensureRunning(daemonArgs ...string) error {
	var firstErr error
	for _, role := range []string{roleDaemon, roleSentinel} {
		var args []string
		if role == roleDaemon {
			args = daemonArgs
		}
		if err := spawnRole(role, args...); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Running reports whether the scheduler is alive.
func Running() bool { return lockfile.Held(lockPath(roleDaemon)) }

// sentinelCmd runs the watcher process.
//
// It holds a lock so only one exists, watches the scheduler, and does nothing
// else. Keeping it this small is the point: it is the process that has to
// survive whatever kills the other one.
func sentinelCmd(argv []string) error {
	lock, err := lockfile.Acquire(lockPath(roleSentinel))
	if err != nil {
		var held *lockfile.ErrHeld
		if errors.As(err, &held) {
			fmt.Println("bermuda: sentinel", held.Error())
			return nil
		}
		return err
	}
	defer lock.Release()

	ctx, stop := signalContext()
	defer stop()

	fmt.Printf("bermuda: sentinel started (pid %d, watching %s)\n", os.Getpid(), roleDaemon)
	watchPeer(ctx, roleSentinel)
	fmt.Println("bermuda: sentinel stopped")
	return nil
}
