package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// homeAt points $HOME at a directory of the test's own, so the canonical store
// under test is never the real ~/.bermuda. Every test here writes to that
// store by design, which is exactly the file the live scheduler reads.
func homeAt(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// aliveIs replaces the liveness check for the duration of a test. The real one
// asks the kernel about a pid, and a test that wanted a live stray would
// otherwise have to start a process to have a pid that answers.
func aliveIs(t *testing.T, live map[int]bool) {
	t.Helper()
	prev := alive
	alive = func(pid int) bool { return live[pid] }
	t.Cleanup(func() { alive = prev })
}

func TestNonCanonicalStore(t *testing.T) {
	home := homeAt(t)
	canonical := filepath.Join(home, ".bermuda")
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"the canonical store itself", canonical, false},
		// Pinned in the direction that would hurt most: an unclean spelling of
		// the real store classified as a stray would put the machine's own
		// scheduler in the alarm on every start, and an alarm that always
		// fires is one nobody reads.
		{"an unclean spelling of it", filepath.Join(home, ".bermuda", "..", ".bermuda"), false},
		{"the scratch store the real leak used", filepath.Join(home, "scratchpad", "bermuda-hidetest"), true},
		{"a temp store", filepath.Join(t.TempDir(), "store"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nonCanonicalStore(tc.dir); got != tc.want {
				t.Fatalf("nonCanonicalStore(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}

// A start against the canonical store is the normal case and must leave no
// trace: a log that grows on every ordinary daemon start says nothing when it
// matters.
func TestRecordStrayIgnoresTheCanonicalStore(t *testing.T) {
	home := homeAt(t)
	recordStray(roleDaemon, 4242, filepath.Join(home, ".bermuda"))
	if _, err := os.Stat(strayLog()); !os.IsNotExist(err) {
		t.Fatalf("canonical start wrote %s (err %v)", strayLog(), err)
	}
}

// The record goes into the canonical store, not the one being served — that is
// the whole point. A note inside a scratch store disappears with it, and the
// pair it describes does not.
func TestRecordStrayWritesToTheCanonicalStore(t *testing.T) {
	homeAt(t)
	scratch := filepath.Join(t.TempDir(), "store")
	recordStray(roleSentinel, 4242, scratch)

	f, err := os.Open(strayLog())
	if err != nil {
		t.Fatalf("stray log: %v", err)
	}
	defer f.Close()
	recs := readStrays(f)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0].PID != 4242 || recs[0].Role != roleSentinel {
		t.Fatalf("got %+v, want pid 4242 sentinel", recs[0])
	}
	if recs[0].StateDir != resolvePath(scratch) {
		t.Fatalf("store recorded as %q, want %q", recs[0].StateDir, resolvePath(scratch))
	}
	if recs[0].Parent != os.Getpid() {
		t.Fatalf("parent recorded as %d, want %d", recs[0].Parent, os.Getpid())
	}
}

// The log is appended to by processes that can be killed mid-write, so a
// truncated last line is a normal state of the file. One bad line must not
// hide the records around it.
func TestReadStraysSkipsUnparseableLines(t *testing.T) {
	in := strings.Join([]string{
		`{"pid":1,"role":"daemon"}`,
		`{"pid":2,"role":"sent`,
		``,
		`{"pid":3,"role":"sentinel"}`,
	}, "\n")
	recs := readStrays(strings.NewReader(in))
	if len(recs) != 2 || recs[0].PID != 1 || recs[1].PID != 3 {
		t.Fatalf("got %+v, want records for pids 1 and 3", recs)
	}
}

func TestReportStraysWithNoLog(t *testing.T) {
	homeAt(t)
	var buf bytes.Buffer
	if err := reportStrays(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no scheduler has been detached") {
		t.Fatalf("got %q", buf.String())
	}
}

// A dead pid is history, not an alarm. The log is append-only, so every stray
// ever recorded stays in it; only the ones still running are a problem.
func TestReportStraysIgnoresDeadRecords(t *testing.T) {
	homeAt(t)
	recordStray(roleDaemon, 111, filepath.Join(t.TempDir(), "store"))
	aliveIs(t, map[int]bool{})

	var buf bytes.Buffer
	if err := reportStrays(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no stray scheduler is running") {
		t.Fatalf("got %q", buf.String())
	}
	if strings.Contains(buf.String(), "111") {
		t.Fatalf("dead pid reported: %q", buf.String())
	}
}

func TestReportStraysNamesTheLiveOnesAndTheKill(t *testing.T) {
	homeAt(t)
	scratch := filepath.Join(t.TempDir(), "store")
	recordStray(roleDaemon, 111, scratch)
	recordStray(roleSentinel, 222, scratch)
	recordStray(roleDaemon, 333, filepath.Join(t.TempDir(), "other"))
	aliveIs(t, map[int]bool{111: true, 222: true})

	var buf bytes.Buffer
	if err := reportStrays(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"2 stray scheduler processes are running", "pid 111", "pid 222", "kill 111 222", resolvePath(scratch)} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
	// Both halves must be killed in one signal, so the remedy has to list them
	// together — a report that named one pid would be advice that cannot work.
	if strings.Contains(out, "pid 333") {
		t.Fatalf("dead pid 333 reported:\n%s", out)
	}
}

// The command has to be dispatchable, or the report is unreachable however
// well it reads.
func TestDoctorIsACommand(t *testing.T) {
	if _, ok := commands()["doctor"]; !ok {
		t.Fatal("bermuda doctor is not in the command table")
	}
	if !strings.Contains(usageText(t), "bermuda doctor") {
		t.Fatal("bermuda doctor is not in the usage text")
	}
}

func usageText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
