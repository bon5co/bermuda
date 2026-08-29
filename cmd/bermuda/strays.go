package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bon5co/bermuda/v3/internal/statefs"
)

// A detached pair against a store that is not the canonical one is invisible.
//
// `bermuda stop` writes its flag into the store named by the *caller's*
// environment, so it cannot address a pair serving some other directory, and
// nothing that enumerates jobs can see that pair's jobs either: an inventory
// reads ~/.bermuda, and a job in another store is outside it by construction.
// That is not hypothetical — a scratch store under ~/scratchpad kept a
// scheduler launching a real agent run daily for four days, and the only
// reason anybody found it was a hand-written scan of /proc.
//
// Two halves of that leak are already closed: watchPeer exits when the store
// it serves is deleted, and scratchRefusal refuses to detach against a temp
// directory at all. This is the half neither can reach — a scratch store that
// is *not* under a temp root, like the ~/scratchpad path the real leak used. A
// refusal wide enough to catch an arbitrary directory under $HOME would catch
// real stores too, and the cost of refusing a real store is a machine with no
// scheduler. So this does not refuse. It records.
//
// A pair detaching against a non-canonical store writes one line into the
// *canonical* store, which is the one place that outlives the store being
// served, and `bermuda doctor` reads those lines back. Alarm plus a documented
// kill, rather than a wider refusal.

// strayRecord is one detached role started against a non-canonical store.
type strayRecord struct {
	Started  time.Time `json:"started"`
	Role     string    `json:"role"`
	PID      int       `json:"pid"`
	Parent   int       `json:"parent"`
	StateDir string    `json:"state_dir"`
	Exe      string    `json:"exe"`
}

// canonicalStateDir is where a store lives when nothing overrides it: the same
// path stateDir() falls back to. It is resolved fresh rather than cached so a
// test can point $HOME somewhere of its own.
func canonicalStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".bermuda")
}

// nonCanonicalStore reports whether dir is a store other than ~/.bermuda.
//
// Symlinks are resolved on both sides for the same reason scratchStore does
// it: on a machine where $HOME is itself a link, comparing unresolved paths
// would report the real store as a stray and every actual stray as normal.
// An unresolvable home is treated as canonical — an alarm that fires on every
// start is one nobody reads.
func nonCanonicalStore(dir string) bool {
	canonical := canonicalStateDir()
	if canonical == "" {
		return false
	}
	return resolvePath(dir) != resolvePath(canonical)
}

// strayLog is the file the records go in, inside the canonical store.
func strayLog() string { return filepath.Join(canonicalStateDir(), "strays.jsonl") }

// recordStray notes that a detached role now serves a store nothing else can
// address.
//
// Best-effort by design, and it returns no error to its caller: the spawn has
// already happened by the time this runs, and failing to write a note about a
// running process must not be reported as a failure to start one. A canonical
// store that does not exist yet is created, because the first thing a fresh
// machine might do is exactly the mistake this watches for.
func recordStray(role string, pid int, dir string) {
	if !nonCanonicalStore(dir) {
		return
	}
	canonical := canonicalStateDir()
	if canonical == "" {
		return
	}
	if err := os.MkdirAll(canonical, statefs.Dir); err != nil {
		return
	}
	exe, _ := os.Executable()
	line, err := json.Marshal(strayRecord{
		Started:  time.Now().UTC(),
		Role:     role,
		PID:      pid,
		Parent:   os.Getpid(),
		StateDir: resolvePath(dir),
		Exe:      exe,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(strayLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, statefs.File)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\n", line)
}

// readStrays returns every record the log holds, oldest first.
//
// A line that will not parse is skipped rather than failing the read. The log
// is append-only from processes that may be killed mid-write, so a truncated
// last line is a normal state of the file, and one bad line must not hide the
// records around it.
func readStrays(r io.Reader) []strayRecord {
	var out []strayRecord
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec strayRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// alive reports whether a pid still exists.
//
// Signal 0 asks the kernel that question without delivering anything. It
// cannot tell a recycled pid from the original process, which is why the
// report below prints the store and the binary alongside: the pid is the
// handle, the store is the evidence.
var alive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// liveStrays keeps the records whose process is still running.
func liveStrays(recs []strayRecord) []strayRecord {
	var out []strayRecord
	for _, rec := range recs {
		if alive(rec.PID) {
			out = append(out, rec)
		}
	}
	return out
}

// doctorCmd reports detached schedulers serving a store nothing can address.
//
// It reads the log rather than the process table on purpose: /proc is Linux
// only and bermuda builds for macOS too, and a scan by name would have to
// decide which of `bermuda board`, `bermuda job list` and a plugin invocation
// counts as a scheduler. The log answers the narrower question it was written
// for — which detached pairs were started here, against what.
func doctorCmd(argv []string) error {
	return reportStrays(os.Stdout)
}

func reportStrays(w io.Writer) error {
	canonical := canonicalStateDir()
	if canonical == "" {
		return fmt.Errorf("cannot resolve a home directory, so there is no canonical store to compare against")
	}
	f, err := os.Open(strayLog())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "bermuda: no scheduler has been detached against a store other than %s\n", canonical)
			return nil
		}
		return err
	}
	defer f.Close()

	live := liveStrays(readStrays(f))
	if len(live) == 0 {
		fmt.Fprintf(w, "bermuda: no stray scheduler is running; every detached pair serves %s\n", canonical)
		return nil
	}

	fmt.Fprintf(w, "bermuda: %d stray scheduler %s running against a store this machine cannot address:\n",
		len(live), plural(len(live), "process is", "processes are"))
	for _, rec := range live {
		fmt.Fprintf(w, "  pid %d  %-8s  store %s  (%s)\n", rec.PID, rec.Role, rec.StateDir, rec.Exe)
	}
	fmt.Fprint(w, strayRemedy(live))
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// strayRemedy is the documented kill.
//
// The order matters and is the whole reason this text exists rather than a
// bare list of pids. Each half of a pair revives the other every five seconds,
// so killing them one at a time never ends one: the survivor brings the other
// back inside a tick. Killing them in one signal does, and removing the store
// first means a build carrying the watchPeer exit ends the pair on its own.
func strayRemedy(live []strayRecord) string {
	var pids, stores []string
	seen := map[string]bool{}
	for _, rec := range live {
		pids = append(pids, fmt.Sprint(rec.PID))
		if !seen[rec.StateDir] {
			seen[rec.StateDir] = true
			stores = append(stores, rec.StateDir)
		}
	}
	var b strings.Builder
	b.WriteString("\nEach half of a pair revives the other every 5s, so kill them together, not one by one:\n")
	b.WriteString("  kill " + strings.Join(pids, " ") + "\n")
	b.WriteString("If the store is gone and they are still up, they are running a build older than the\n")
	b.WriteString("exit-when-abandoned fix; the same kill ends them. Stores involved:\n")
	for _, store := range stores {
		b.WriteString("  " + store + "\n")
	}
	return b.String()
}
