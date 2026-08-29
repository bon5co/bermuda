package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	roleDaemon   = "daemon"
	roleSentinel = "sentinel"
)

// watchInterval is how often each process checks on the other. This is a
// liveness check against a lock file, not real work, so it stays cheap. It is
// a variable rather than a constant so the tests can watch the pair behave
// without waiting five seconds for every tick.
var watchInterval = 5 * time.Second

func lockPath(role string) string {
	return filepath.Join(stateDir(), role+".lock")
}

// stateDirPresent reports whether the store this process was started against
// still exists.
//
// A pair whose state directory has been removed is unreachable: `bermuda stop`
// writes its flag into the store named by the *caller's* environment, so the
// off switch for a deleted store cannot be written at all, and the pair revives
// itself every watchInterval forever. That is not a hypothetical — a scratch
// BERMUDA_STATE_DIR left behind by a test run kept a scheduler launching real
// agent runs for days, and nothing on the machine could stop it.
func stateDirPresent() bool {
	st, err := os.Stat(stateDir())
	return err == nil && st.IsDir()
}

// abandoned decides whether a running role should give up.
//
// The check is deliberately one-way: only a store that this process has
// actually seen counts as gone. A daemon started a moment before anything
// created the directory must not read its own head start as abandonment.
func abandoned(seenPresent, presentNow bool) bool { return seenPresent && !presentNow }

// peerOf returns the role each process is responsible for reviving.
func peerOf(role string) string {
	if role == roleDaemon {
		return roleSentinel
	}
	return roleDaemon
}

// watchPeer revives this process's counterpart whenever it goes missing, and
// shuts this one down once the store they both serve is gone.
//
// Both sides run this, which is what makes the pair mutually recovering: kill
// either one and the survivor brings it back. The same mutual revival is what
// makes an abandoned store dangerous, so the exit is checked before the
// revival: whichever half notices first stops reviving the other, and the other
// notices within a tick.
//
// quit cancels this process's own work — the daemon's run loop, or the
// sentinel's watch — so an abandoned pair leaves rather than being killed.
func watchPeer(ctx context.Context, role string, quit context.CancelFunc) {
	newPeerWatch(role).run(ctx, quit)
}

// peerWatch is one role's view of the pair: who it revives, and whether it has
// ever seen the store they serve.
//
// Constructing it is separate from running it so that the first reading of the
// store is taken at a known moment rather than whenever a goroutine happens to
// be scheduled — which a test deleting the directory would otherwise race.
type peerWatch struct {
	role      string
	seenStore bool
}

func newPeerWatch(role string) *peerWatch {
	return &peerWatch{role: role, seenStore: stateDirPresent()}
}

func (w *peerWatch) run(ctx context.Context, quit context.CancelFunc) {
	role := w.role
	peer := peerOf(role)
	seenStore := w.seenStore
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			present := stateDirPresent()
			if abandoned(seenStore, present) {
				fmt.Fprintf(os.Stderr,
					"bermuda: %s: state directory %s is gone; stopping rather than running unreachable\n",
					role, stateDir())
				quit()
				return
			}
			seenStore = seenStore || present

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
// It refuses two things outright: a start while `bermuda stop` is in force,
// and a start against a scratch store. It is a no-op when that role is already
// running: the child takes the lock
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
	if err := scratchRefusal(role); err != nil {
		return err
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
	// Recorded after the start, because the pid is the handle a person needs
	// and there is no pid until there is a process.
	recordStray(role, cmd.Process.Pid, stateDir())
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
	watchPeer(ctx, roleSentinel, stop)
	fmt.Println("bermuda: sentinel stopped")
	return nil
}

// scratchRefusal reports why a role may not be started detached against the
// store this process was given, or nil if it may.
//
// A detached pair outlives whatever started it, and a throwaway store is by
// definition about to be removed — which leaves the pair with no off switch at
// all, since `bermuda stop` can only write its flag into a directory that still
// exists. This is the half of that leak the pair itself cannot close: the exit
// in watchPeer ends an abandoned pair, but only a refusal here stops one being
// created.
func scratchRefusal(role string) error {
	if !scratchStore(stateDir()) || os.Getenv(allowScratchDaemonEnv) == "1" {
		return nil
	}
	return fmt.Errorf("refusing to start %s detached against the scratch store %s: "+
		"set %s=1 if a background pair in a temp directory is really what you want",
		role, stateDir(), allowScratchDaemonEnv)
}

// allowScratchDaemonEnv lets a caller that really means it detach against a
// throwaway store. It exists so the refusal below can be strict: anything that
// genuinely wants a background pair in a temp directory says so once, in the
// environment, rather than the refusal being softened for everyone.
const allowScratchDaemonEnv = "BERMUDA_ALLOW_SCRATCH_DAEMON"

// scratchStore reports whether a state directory is a throwaway one — under
// the system temp directory, or under /tmp or /var/tmp.
//
// Symlinks are resolved on both sides because macOS reports its temp directory
// under /var/folders, which is itself a symlink into /private/var. Comparing
// the unresolved paths there would classify every temp store as permanent.
func scratchStore(dir string) bool {
	resolved := resolvePath(dir)
	for _, root := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		root = resolvePath(root)
		if root == "" || root == string(filepath.Separator) {
			continue
		}
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolvePath cleans a path and follows symlinks as far as the filesystem
// allows. A path that does not exist yet still cleans, which matters: the
// refusal has to hold for a store directory that is about to be created.
func resolvePath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}
