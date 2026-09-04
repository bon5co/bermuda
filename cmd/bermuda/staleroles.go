package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bon5co/bermuda/v3/internal/lockfile"
	"github.com/bon5co/bermuda/v3/internal/statefs"
	"github.com/bon5co/bermuda/v3/internal/store"
	"github.com/bon5co/bermuda/v3/internal/version"
)

// A rebuilt bermuda does not reach the fleet until the pair is restarted, and
// nothing restarted it.
//
// `bermuda ensure` starts a role only when its lock is free, which is the right
// rule for "is anything running" and the wrong one for "is what is running the
// build on disk". The two questions had the same answer for as long as bermuda
// was only ever started fresh; they stopped having it the moment a fix was
// merged under a live scheduler.
//
// The incident: the daemon and sentinel started on 2026-08-29 19:26 and were
// still serving that build on 2026-09-05, seven days and eight merges later.
// Two of those merges were the ones that write a park's reason into its note —
// so when the account's session limit stopped three scheduled runs on 09-04,
// each parked with an empty note, and the self-heal job listed three healthy
// jobs as broken. The fix was on disk the whole time. Nothing was executing it.
//
// So each role stamps what it is running beside its lock, and ensure compares
// that against itself. A role whose stamp is missing or names another build is
// restarted — but only while the fleet is idle: a restart with runs in flight
// strands them, which is a worse fault than the stale build it cures.

// buildStamp is what a running role records about the build serving it.
type buildStamp struct {
	// Version is version.String() for the running binary — a tag, or the
	// commit it was built from.
	Version string `json:"version"`
	// Exe is the path the role was started from. Compared so that a bermuda
	// run out of a worktree or a scratch build never restarts the installed
	// pair: two different installs are not two builds of one install.
	Exe string `json:"exe"`
	// PID is the process to signal. The lock file carries one too; this one is
	// kept so a stamp read on its own says who wrote it.
	PID int `json:"pid"`
	// Started is when that process took its lock.
	Started time.Time `json:"started"`
}

// buildStampPath is where a role records its build, beside its lock.
func buildStampPath(role string) string {
	return filepath.Join(stateDir(), role+".build")
}

// currentStamp describes the build making the call.
//
// An executable path that cannot be resolved is left empty rather than guessed:
// an empty path compares unequal to every recorded one, which makes this side
// of the comparison decline to act instead of acting on a wrong answer.
func currentStamp() buildStamp {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	return buildStamp{
		Version: version.String(),
		Exe:     resolvePath(exe),
		PID:     os.Getpid(),
		Started: time.Now().UTC(),
	}
}

// recordBuildStamp is called by a role once it holds its lock.
//
// Best-effort and silent, for the reason writeErrFile is: a role that is
// running must not fail to start because a note about it could not be written.
// A missing stamp is read as "some build that did not stamp", which is the
// truth and is exactly the build this whole mechanism was added to evict.
func recordBuildStamp(role string) {
	b, err := json.Marshal(currentStamp())
	if err != nil {
		return
	}
	// The state directory is created by whatever opens the store, and a role
	// can hold its lock before that has happened on a fresh machine.
	if err := os.MkdirAll(stateDir(), statefs.Dir); err != nil {
		return
	}
	_ = os.WriteFile(buildStampPath(role), append(b, '\n'), statefs.File)
}

// readBuildStamp reports what a role recorded, and whether it recorded anything.
func readBuildStamp(role string) (buildStamp, bool) {
	b, err := os.ReadFile(buildStampPath(role))
	if err != nil {
		return buildStamp{}, false
	}
	var s buildStamp
	if err := json.Unmarshal(b, &s); err != nil {
		return buildStamp{}, false
	}
	return s, true
}

// staleAgainst reports whether a running role's stamp calls for a restart, and
// says why in the words the operator will read.
//
// The three cases are deliberately not one:
//   - no stamp: the role predates stamping, so it is by definition an older
//     build than the one asking. This is the case that evicts the pair once,
//     the first time a stamping build runs ensure.
//   - a different install: not this build's business. A worktree build or a
//     `go run` must never restart the installed scheduler, because it would be
//     replacing the fleet's binary with one nobody deployed.
//   - a different version from the same path: the build was replaced in place,
//     which is what `make build` does.
func staleAgainst(running buildStamp, found bool, current buildStamp) (bool, string) {
	if !found {
		return true, "running a build that recorded no version (started before bermuda stamped one)"
	}
	if current.Exe == "" || running.Exe != current.Exe {
		// Not stale — just not ours. Said out loud because the alternative is
		// an ensure that silently does nothing on a machine with two installs.
		return false, fmt.Sprintf("started from %s, not %s; leaving it alone", running.Exe, current.Exe)
	}
	if running.Version != current.Version {
		return true, fmt.Sprintf("running %s, on disk is %s", running.Version, current.Version)
	}
	return false, ""
}

// staleRole is a running role that is not the build on disk.
type staleRole struct {
	role string
	pid  int
	why  string
}

// staleRoles lists the running roles whose build is not this one.
func staleRoles() []staleRole {
	current := currentStamp()
	var out []staleRole
	for _, role := range []string{roleDaemon, roleSentinel} {
		if !lockfile.Held(lockPath(role)) {
			continue
		}
		running, found := readBuildStamp(role)
		stale, why := staleAgainst(running, found, current)
		if !stale {
			continue
		}
		out = append(out, staleRole{role: role, pid: lockfile.PIDOf(lockPath(role)), why: why})
	}
	return out
}

// runsInFlight counts the runs the store believes are still going.
//
// The idle check, and the reason this is not simply a kill: a scheduler
// stopped mid-run leaves its agent unwatched and the row stuck at "running"
// until reconcile corrects it. The stale build costs one wrong note per park;
// stranding a live run costs the run. So the restart waits, and the two-minute
// ensure timer means waiting is cheap — the next idle moment is minutes away,
// not days.
func runsInFlight(ctx context.Context, s *store.Store) (int, error) {
	runs, err := s.Runs(ctx, string(store.StepRunning), 200)
	if err != nil {
		return 0, err
	}
	return len(runs), nil
}

// restartTimeout bounds waiting for a signalled role to drop its lock. The
// roles exit on a cancelled context between two five-second watch ticks, so
// this is several times the expected wait and still short enough that an
// ensure invocation cannot hang a startup hook.
const restartTimeout = 30 * time.Second

// restartStaleRoles brings the running pair onto the build on disk.
//
// Both roles are signalled before either is restarted, because they revive each
// other: restarting the daemon while the old sentinel still watches would have
// the old build spawn the replacement, which is the fault this is curing.
//
// It reports what it did rather than returning an error for a fleet it chose
// not to touch. A busy fleet, a foreign install and a pair already on the right
// build are all correct outcomes, and none of them is a failure of ensure.
func restartStaleRoles(ctx context.Context, s *store.Store, w *os.File) error {
	stale := staleRoles()
	if len(stale) == 0 {
		return nil
	}
	for _, sr := range stale {
		fmt.Fprintf(w, "bermuda: %s is stale — %s\n", sr.role, sr.why)
	}
	n, err := runsInFlight(ctx, s)
	if err != nil {
		return err
	}
	if n > 0 {
		fmt.Fprintf(w, "bermuda: %d run(s) in flight; leaving the stale pair alone until the fleet is idle\n", n)
		return nil
	}
	for _, sr := range stale {
		if sr.pid <= 0 {
			continue
		}
		if err := syscall.Kill(sr.pid, syscall.SIGTERM); err != nil {
			fmt.Fprintf(w, "bermuda: stop %s (pid %d): %v\n", sr.role, sr.pid, err)
		}
	}
	for _, sr := range stale {
		if !awaitLockFree(lockPath(sr.role), restartTimeout) {
			return fmt.Errorf("%s (pid %d) still holds its lock %s after %s",
				sr.role, sr.pid, lockPath(sr.role), restartTimeout)
		}
	}
	if err := EnsureRunning(); err != nil {
		return err
	}
	for _, sr := range stale {
		fmt.Fprintf(w, "bermuda: restarted %s on %s\n", sr.role, version.String())
	}
	return nil
}

// awaitLockFree waits for a signalled role to let go of its lock.
func awaitLockFree(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if !lockfile.Held(path) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(lockPollInterval)
	}
}

// lockPollInterval is how often the wait above re-probes. Held takes the lock
// and drops it again, so this is not free; a tenth of a second is far below
// the shutdown it is waiting on and far above the cost of the probe.
const lockPollInterval = 100 * time.Millisecond
