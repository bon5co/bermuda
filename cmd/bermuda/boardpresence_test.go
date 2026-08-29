package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/runner"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// paneRecorder is a stand-in herdr binary that records every invocation and
// answers with an empty success envelope. The presence loop only ever asks
// herdr to remember something, so a recording of the calls is the whole of what
// a caller depends on: what was claimed, and whether it was given back.
type paneRecorder struct {
	t    *testing.T
	dir  string
	file string
}

func newPaneRecorder(t *testing.T) *paneRecorder {
	t.Helper()
	dir := t.TempDir()
	r := &paneRecorder{t: t, dir: dir, file: filepath.Join(dir, "calls")}
	script := "#!/bin/sh\n" +
		"printf '###\\n' >> " + r.file + "\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + r.file + "; done\n" +
		"printf '{\"id\":\"1\",\"result\":{}}\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake herdr: %v", err)
	}
	return r
}

func (r *paneRecorder) client() *herdrcli.Client {
	return &herdrcli.Client{Bin: filepath.Join(r.dir, "herdr")}
}

// calls returns the argument list of every invocation, in order.
func (r *paneRecorder) calls() [][]string {
	r.t.Helper()
	b, err := os.ReadFile(r.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		r.t.Fatalf("read calls: %v", err)
	}
	var out [][]string
	var cur []string
	started := false
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "###" {
			if started {
				out = append(out, cur)
			}
			started, cur = true, nil
			continue
		}
		cur = append(cur, line)
	}
	if started {
		out = append(out, cur)
	}
	return out
}

// flagValue returns the value that follows flag, or "" when it is absent.
func flagValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func seedRun(t *testing.T, s *store.Store, id, outcome string) {
	t.Helper()
	err := s.PutRun(context.Background(), store.Run{
		ID: id, JobID: "j-" + id, Outcome: outcome, StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The row is claimed the moment the board opens, not on the first tick five
// seconds later. A board that reported nothing until its first tick is a board
// that is missing from the sidebar for as long as somebody is likely to glance
// at it after opening one.
func TestBoardPresenceReportsBeforeItsFirstTick(t *testing.T) {
	rec := newPaneRecorder(t)
	s := newStore(t)
	seedRun(t, s, "r1", "parked")
	seedRun(t, s, "r2", "running")
	t.Setenv("HERDR_PANE_ID", "wA:p7")

	p := startBoardPresence(s, rec.client())
	defer p.Stop()

	calls := rec.calls()
	if len(calls) == 0 {
		t.Fatal("nothing was reported when the board opened")
	}
	first := calls[0]
	if len(first) < 3 || first[0] != "pane" || first[1] != "report-agent" || first[2] != "wA:p7" {
		t.Fatalf("first call = %v, want a report against the board's own pane", first)
	}
	if got := flagValue(first, "--agent"); got != herdrcli.BoardAgent {
		t.Errorf("--agent = %q, want %q", got, herdrcli.BoardAgent)
	}
	if got := flagValue(first, "--source"); got != runner.PluginSource {
		t.Errorf("--source = %q, want %q", got, runner.PluginSource)
	}
	msg := flagValue(first, "--message")
	if !strings.Contains(msg, "1 parked") || !strings.Contains(msg, "1 running") {
		t.Errorf("--message = %q, want both counts from the store", msg)
	}
}

// A claim on a pane outlives the process that made it, so the release is the
// half of this that has to happen: without it the sidebar keeps a bermuda row
// addressing a pane that has gone back to being somebody's shell.
func TestBoardPresenceReleasesThePaneOnStop(t *testing.T) {
	rec := newPaneRecorder(t)
	t.Setenv("HERDR_PANE_ID", "wA:p7")

	p := startBoardPresence(newStore(t), rec.client())
	p.Stop()

	calls := rec.calls()
	if len(calls) < 2 {
		t.Fatalf("got %d calls, want a report and a release", len(calls))
	}
	last := calls[len(calls)-1]
	if len(last) < 3 || last[0] != "pane" || last[1] != "release-agent" || last[2] != "wA:p7" {
		t.Fatalf("last call = %v, want the pane released", last)
	}
	if got := flagValue(last, "--agent"); got != herdrcli.BoardAgent {
		t.Errorf("released --agent = %q, want %q", got, herdrcli.BoardAgent)
	}
	if got := flagValue(last, "--source"); got != runner.PluginSource {
		t.Errorf("released --source = %q, want %q", got, runner.PluginSource)
	}
}

// Herdr keeps the highest sequence number it has seen for a pane and drops
// anything below it, so a report that reuses a number is a state change that
// never reaches the sidebar. The counter has to move on every report.
func TestBoardPresenceSequenceNumbersAdvance(t *testing.T) {
	rec := newPaneRecorder(t)
	t.Setenv("HERDR_PANE_ID", "wA:p7")

	p := startBoardPresence(newStore(t), rec.client())
	p.report()
	p.report()
	defer p.Stop()

	var seqs []string
	for _, c := range rec.calls() {
		if len(c) > 1 && c[1] == "report-agent" {
			seqs = append(seqs, flagValue(c, "--seq"))
		}
	}
	if len(seqs) < 3 {
		t.Fatalf("got %d reports, want 3", len(seqs))
	}
	for i, want := range []string{"1", "2", "3"} {
		if seqs[i] != want {
			t.Fatalf("report %d carried --seq %q, want %q (seqs=%v)", i+1, seqs[i], want, seqs)
		}
	}
}

// The counts are the point of the row, and they are two specific queries: a
// finished run is not something anybody needs to be told about, and counting it
// would leave the row claiming work in flight forever.
func TestBoardPresenceStateCountsOnlyUnfinishedRuns(t *testing.T) {
	s := newStore(t)
	seedRun(t, s, "a", "parked")
	seedRun(t, s, "b", "running")
	seedRun(t, s, "c", "running")
	seedRun(t, s, "d", "done")
	seedRun(t, s, "e", "failed")

	p := &boardPresence{store: s}
	state, msg := p.state(context.Background())
	if state != herdrcli.StatusIdle {
		t.Errorf("state = %q, want idle", state)
	}
	if !strings.Contains(msg, "1 parked") || !strings.Contains(msg, "2 running") {
		t.Errorf("message = %q, want 1 parked and 2 running", msg)
	}
}

// A store that cannot be read is not an empty store. Saying "no runs in flight"
// because the query failed is the silent wrong answer this row exists to avoid;
// unknown is what herdr has for "attached, state not established".
func TestBoardPresenceStateIsUnknownWhenTheStoreCannotBeRead(t *testing.T) {
	s := newStore(t)
	seedRun(t, s, "a", "parked")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	p := &boardPresence{store: s}
	state, msg := p.state(context.Background())
	if state != herdrcli.StatusUnknown {
		t.Errorf("state = %q, want unknown when the store is unreadable", state)
	}
	if msg != "" {
		t.Errorf("message = %q, want nothing said about counts nobody could read", msg)
	}
}

// Stop is called from the board's shutdown path, including after a start that
// found no pane to claim. It must be safe to call in that case and must not
// block on a loop that was never started.
func TestBoardPresenceStopIsSafeWithoutAPane(t *testing.T) {
	rec := newPaneRecorder(t)
	t.Setenv("HERDR_PANE_ID", "")

	p := startBoardPresence(newStore(t), rec.client())
	p.Stop()

	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("herdr was called %d times with no pane to claim: %v", len(calls), calls)
	}
}
