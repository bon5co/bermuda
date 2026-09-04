package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/lockfile"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// The incident this file exists for: the daemon and sentinel ran a build from
// 2026-08-29 until 2026-09-05 while eight merges sat unexecuted on disk, two of
// them the ones that write a park's reason into its note. Three scheduled runs
// then parked on the account's session limit with empty notes and the self-heal
// job spent a shift diagnosing three healthy jobs.

// A role that recorded no build at all is the exact shape of that incident: it
// was started by a bermuda too old to stamp, so it cannot be the build asking.
func TestARoleWithNoStampIsStale(t *testing.T) {
	stale, why := staleAgainst(buildStamp{}, false, buildStamp{Version: "v3.1.0", Exe: "/opt/bermuda"})
	if !stale {
		t.Fatal("a role that recorded no version was not treated as stale")
	}
	if !strings.Contains(why, "no version") {
		t.Errorf("why = %q, want it to say the running build recorded no version", why)
	}
}

func TestStaleAgainst(t *testing.T) {
	current := buildStamp{Version: "v3.1.0", Exe: "/home/r/Projects/bermuda/bin/bermuda"}
	cases := []struct {
		name    string
		running buildStamp
		want    bool
	}{
		{
			"the same build from the same path",
			buildStamp{Version: "v3.1.0", Exe: current.Exe},
			false,
		},
		{
			"an older build replaced in place, which is what make build does",
			buildStamp{Version: "ab326db", Exe: current.Exe},
			true,
		},
		{
			// A worktree build reports "dev" and lives somewhere else. It must
			// never evict the installed pair: that would deploy a binary
			// nobody merged, from a shell nobody was watching.
			"a different install",
			buildStamp{Version: "dev", Exe: "/home/r/Projects/bermuda_worktrees/wip/bin/bermuda"},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, why := staleAgainst(tc.running, true, current); got != tc.want {
				t.Fatalf("staleAgainst = %v (%s), want %v", got, why, tc.want)
			}
		})
	}
}

// An executable this process cannot name is not evidence about anything, so the
// comparison declines rather than guessing — restarting the fleet on a wrong
// answer is worse than leaving a stale build one more ensure tick.
func TestAnUnknownExecutableRestartsNothing(t *testing.T) {
	running := buildStamp{Version: "old", Exe: "/opt/bermuda"}
	if stale, _ := staleAgainst(running, true, buildStamp{Version: "new", Exe: ""}); stale {
		t.Fatal("a caller that cannot name its own executable restarted the pair anyway")
	}
}

func TestBuildStampRoundTrips(t *testing.T) {
	homeAt(t)
	if _, found := readBuildStamp(roleDaemon); found {
		t.Fatal("read a stamp before one was written")
	}
	recordBuildStamp(roleDaemon)
	got, found := readBuildStamp(roleDaemon)
	if !found {
		t.Fatalf("no stamp at %s after recording one", buildStampPath(roleDaemon))
	}
	want := currentStamp()
	if got.Version != want.Version || got.Exe != want.Exe {
		t.Errorf("stamp = %+v, want version %q exe %q", got, want.Version, want.Exe)
	}
	if got.PID != os.Getpid() {
		t.Errorf("stamp pid = %d, want %d", got.PID, os.Getpid())
	}
}

// staleRoles reads locks, so a role nothing holds is not a role to restart:
// ensure's existing start path already covers "not running".
func TestStaleRolesIgnoresARoleThatIsNotRunning(t *testing.T) {
	homeAt(t)
	if got := staleRoles(); len(got) != 0 {
		t.Fatalf("staleRoles() = %+v with nothing running, want none", got)
	}
}

func TestStaleRolesFindsAHeldRoleWithNoStamp(t *testing.T) {
	homeAt(t)
	lock, err := lockfile.Acquire(lockPath(roleDaemon))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	got := staleRoles()
	if len(got) != 1 || got[0].role != roleDaemon {
		t.Fatalf("staleRoles() = %+v, want the daemon alone", got)
	}
	if got[0].pid != os.Getpid() {
		t.Errorf("pid = %d, want the lock holder %d", got[0].pid, os.Getpid())
	}
}

// A role holding its lock on this very build is current, and restarting it
// would be a scheduler outage every two minutes for no reason at all.
func TestStaleRolesLeavesTheCurrentBuildAlone(t *testing.T) {
	homeAt(t)
	lock, err := lockfile.Acquire(lockPath(roleSentinel))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	recordBuildStamp(roleSentinel)

	if got := staleRoles(); len(got) != 0 {
		t.Fatalf("staleRoles() = %+v for the running build, want none", got)
	}
}

// The rule that keeps this safe: a stale build costs one wrong note per park,
// and a restart with runs in flight costs the runs. So a busy fleet is left
// alone and told about, and the two-minute ensure timer tries again.
func TestRestartWaitsWhileRunsAreInFlight(t *testing.T) {
	homeAt(t)
	lock, err := lockfile.Acquire(lockPath(roleDaemon))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	s := openTestStore(t)
	ctx := context.Background()
	if err := s.PutRun(ctx, store.Run{
		ID: "r1", JobID: "j", Outcome: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	out, w := capture(t)
	if err := restartStaleRoles(ctx, s, w); err != nil {
		t.Fatal(err)
	}
	if !lockfile.Held(lockPath(roleDaemon)) {
		t.Fatal("the daemon was stopped with a run in flight")
	}
	if got := out(); !strings.Contains(got, "in flight") {
		t.Errorf("output = %q, want it to say why the pair was left alone", got)
	}
}

// Nothing stale means nothing said. ensure runs every two minutes, and a line
// on every healthy tick is a log nobody reads.
func TestRestartSaysNothingWhenTheBuildIsCurrent(t *testing.T) {
	homeAt(t)
	s := openTestStore(t)
	out, w := capture(t)
	if err := restartStaleRoles(context.Background(), s, w); err != nil {
		t.Fatal(err)
	}
	if got := out(); got != "" {
		t.Errorf("output = %q for a healthy pair, want nothing", got)
	}
}

// openTestStore opens a store in the test's own home.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".bermuda")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "bermuda.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// capture gives restartStaleRoles somewhere to write and a way to read it back.
func capture(t *testing.T) (func() string, *os.File) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(bytes.TrimSpace(b))
	}, f
}

// Two rows on this machine had said "running" since 2026-07-31 and 2026-08-04:
// their launcher died, so no result was ever written and no agent name was ever
// recorded, and reconcile has nothing left to judge them by. Counting those as
// in flight would make the fleet permanently busy and the restart would never
// happen — the fault this file cures, one level up.
func TestARowStuckRunningPastItsTimeoutIsNotInFlight(t *testing.T) {
	homeAt(t)
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.PutJob(ctx, store.Job{ID: "shorts", Prompt: "x", Timeout: 30 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(ctx, store.Run{
		ID: "old", JobID: "shorts", Outcome: "running",
		StartedAt: time.Now().Add(-32 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(ctx, store.Run{
		ID: "live", JobID: "shorts", Outcome: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := runsInFlight(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("runsInFlight = %d, want 1: only the run that started just now can still be running", n)
	}
}

// The grace is what keeps the check from calling a run dead the second its
// timeout passes: the row settles a moment after the kill, and clocks differ.
func TestARunJustPastItsTimeoutIsStillInFlight(t *testing.T) {
	homeAt(t)
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.PutJob(ctx, store.Job{ID: "j", Prompt: "x", Timeout: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(ctx, store.Run{
		ID: "r", JobID: "j", Outcome: "running",
		StartedAt: time.Now().Add(-11 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := runsInFlight(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("runsInFlight = %d, want 1 within the grace after a timeout", n)
	}
}

// A job that has since been removed leaves runs behind. They are bounded by the
// generous default rather than counted forever, for the same reason as above.
func TestARunWhoseJobIsGoneUsesTheDefaultBound(t *testing.T) {
	homeAt(t)
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.PutRun(ctx, store.Run{
		ID: "orphan", JobID: "deleted", Outcome: "running",
		StartedAt: time.Now().Add(-defaultJobTimeout - 2*inFlightGrace),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(ctx, store.Run{
		ID: "recent", JobID: "deleted", Outcome: "running",
		StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := runsInFlight(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("runsInFlight = %d, want 1: only the recent run is still plausibly running", n)
	}
}
