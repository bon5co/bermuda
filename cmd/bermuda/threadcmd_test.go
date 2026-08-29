package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// The thread command surface, driven the way it is typed.
//
// These cover the wrappers rather than the store calls under them: the guards
// that matter to a caller -- rm refusing to take a conversation with messages
// in it, log staying inside one thread unless told otherwise, whoami showing
// where the pid came from -- live in the wrappers and nowhere else. A slip in
// any of them is silent: the wrong thread is read, or a record is deleted, and
// nothing errors.

// threadCmdEnv isolates the command surface from the machine it runs on.
//
// On top of claimEnv's identity variables it points herdr at a binary that is
// not there. `thread list` asks herdr for workspace labels, and a test must not
// depend on whether a herdr server happens to be up on this machine -- nor
// spend a lookup timeout waiting to find out.
func threadCmdEnv(t *testing.T) context.Context {
	t.Helper()
	claimEnv(t)
	t.Setenv("HERDR_BIN_PATH", filepath.Join(t.TempDir(), "no-such-herdr"))
	return context.Background()
}

func TestThreadCmdRefusesAnUnknownSubcommand(t *testing.T) {
	if err := threadCmd(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("threadCmd with no arguments returned %v, want the usage line", err)
	}
	err := threadCmd([]string{"lsit"})
	if err == nil || !strings.Contains(err.Error(), "lsit") {
		t.Errorf("threadCmd(lsit) returned %v, want an error naming the typo", err)
	}
}

// `room` is the pre-rename name, and scheduled jobs and shell history still
// hold it. It has to keep working and it has to keep its notice on stderr, so
// `room log --json` still pipes into a parser unchanged.
func TestRoomCmdStillWorksAndComplainsOnStderr(t *testing.T) {
	threadCmdEnv(t)
	var out string
	notice := captureStderr(t, func() {
		var err error
		out, err = captureStdout(t, func() error { return roomCmd([]string{"whoami", "--as", "ada"}) })
		if err != nil {
			t.Errorf("room whoami: %v", err)
		}
	})
	if !strings.Contains(notice, "renamed to `thread`") {
		t.Errorf("stderr = %q, want the rename notice", notice)
	}
	if !strings.Contains(out, "ada") {
		t.Errorf("stdout = %q, want the identity room whoami resolved", out)
	}
}

// What `thread list --json` says is what a picker and any other agent reads, so
// the count and the last-message time have to come from the messages actually
// in the thread rather than from the thread row.
func TestThreadListReportsEveryThreadAndHowBusyItIs(t *testing.T) {
	ctx := threadCmdEnv(t)
	if err := threadNew([]string{"webapp", "--about", "the site"}); err != nil {
		t.Fatal(err)
	}
	if err := threadNew([]string{"quiet"}); err != nil {
		t.Fatal(err)
	}
	s := storeForEnv(t)
	for _, body := range []string{"first", "second"} {
		if _, err := s.ThreadPost(ctx, store.ThreadMessage{
			Thread: "webapp", Kind: store.KindNote,
			By: store.Identity{Name: "ada", PID: "1"}, Body: body}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error { return threadList([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var threads []store.Thread
	if err := json.Unmarshal([]byte(out), &threads); err != nil {
		t.Fatalf("thread list --json emitted something that is not JSON: %v", err)
	}
	byID := map[string]store.Thread{}
	for _, th := range threads {
		byID[th.ID] = th
	}
	for _, want := range []string{store.GlobalThread, "webapp", "quiet"} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("thread %s is missing from the list %v", want, byID)
		}
	}
	if got := byID["webapp"]; got.Messages != 2 || got.LastAt.IsZero() || got.About != "the site" {
		t.Errorf("webapp = %+v, want 2 messages, a last-message time, and its about text", got)
	}
	// A thread nobody has written in is the case the table renders as "never",
	// so the zero time has to survive the round trip rather than becoming a
	// timestamp from 1970.
	if got := byID["quiet"]; got.Messages != 0 || !got.LastAt.IsZero() {
		t.Errorf("quiet = %+v, want no messages and no last-message time", got)
	}

	// A thread that was never closed must not appear under --closed: the list is
	// read to choose where to write, and a closed thread is not somewhere
	// anything can be written.
	out, err = captureStdout(t, func() error { return threadList([]string{"--json", "--closed"}) })
	if err != nil {
		t.Fatal(err)
	}
	var closed []store.Thread
	if err := json.Unmarshal([]byte(out), &closed); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Errorf("--closed listed %v, want nothing: no workspace has gone away", closed)
	}
}

// The table form has to say something for every column, because a blank in a
// table reads as missing data rather than as an answer.
func TestThreadListTableNamesAThreadNobodyHasWrittenIn(t *testing.T) {
	threadCmdEnv(t)
	if err := threadNew([]string{"quiet", "--about", "not started yet"}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return threadList(nil) })
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "quiet") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("quiet is not in the table:\n%s", out)
	}
	if !strings.Contains(line, "never") {
		t.Errorf("row %q does not say the thread has never been written in", line)
	}
	if !strings.Contains(line, "not started yet") {
		t.Errorf("row %q drops the about text", line)
	}
}

// The space column is what tells a reader whether `@all` will reach anyone, so
// each of its four answers has to be distinguishable.
func TestSpaceColumnNamesTheWorkspaceOrSaysThereIsNone(t *testing.T) {
	gone := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	labels := map[string]string{"w7": "Better Lingo"}
	cases := []struct {
		name string
		t    store.Thread
		want string
	}{
		{"made by hand", store.Thread{ID: "webapp"}, "-"},
		{"labelled space", store.Thread{ID: "bl", WorkspaceID: "w7"}, "Better Lingo"},
		{"herdr cannot be asked", store.Thread{ID: "x", WorkspaceID: "w9"}, "w9"},
		{"the window was closed", store.Thread{ID: "bl", WorkspaceID: "w7", ClosedAt: &gone}, "(gone)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spaceColumn(c.t, labels); got != c.want {
				t.Errorf("spaceColumn = %q, want %q", got, c.want)
			}
		})
	}
}

// Creating a thread that already exists must fail rather than reset it, and an
// id that is not one must be refused rather than slugged: two agents a
// keystroke apart would otherwise be in two conversations, each seeing half.
func TestThreadNewRefusesADuplicateAndAMalformedID(t *testing.T) {
	ctx := threadCmdEnv(t)
	if err := threadNew([]string{"webapp", "--about", "the site"}); err != nil {
		t.Fatal(err)
	}
	err := threadNew([]string{"webapp", "--about", "something else"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("re-creating webapp returned %v, want a refusal", err)
	}

	s := storeForEnv(t)
	threads, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.ID == "webapp" && th.About != "the site" {
			t.Errorf("about = %q, the refused create still overwrote it", th.About)
		}
	}

	if err := threadNew([]string{"Better Lingo"}); err == nil {
		t.Error("thread new accepted an id that is not one")
	}
	if err := threadNew(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("thread new with no id returned %v, want the usage line", err)
	}
}

// Deleting a conversation deletes the record of what is true on this machine,
// so a thread with messages in it is refused without --force, and global is
// refused with it.
func TestThreadRemoveKeepsAThreadThatStillHoldsMessages(t *testing.T) {
	ctx := threadCmdEnv(t)
	if err := threadNew([]string{"webapp"}); err != nil {
		t.Fatal(err)
	}
	s := storeForEnv(t)
	if _, err := s.ThreadPost(ctx, store.ThreadMessage{
		Thread: "webapp", Kind: store.KindNote,
		By: store.Identity{Name: "ada", PID: "1"}, Body: "camoufox is installed"}); err != nil {
		t.Fatal(err)
	}

	err := threadRemove([]string{"webapp"})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("removing a thread with messages returned %v, want a refusal pointing at --force", err)
	}
	msgs, err := s.ThreadLog(ctx, store.ThreadFilter{Thread: "webapp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("the thread holds %d messages after a refused rm, want 1", len(msgs))
	}

	out, err := captureStdout(t, func() error { return threadRemove([]string{"webapp", "--force"}) })
	if err != nil {
		t.Fatalf("thread rm --force: %v", err)
	}
	if !strings.Contains(out, "1 messages") {
		t.Errorf("output %q does not say how many messages went with the thread", out)
	}
	threads, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.ID == "webapp" {
			t.Error("webapp survived thread rm --force")
		}
	}

	// Global holds every message written before threads existed and every one
	// that names no thread. It is not deletable, force or not.
	if err := threadRemove([]string{store.GlobalThread, "--force"}); err == nil {
		t.Error("global was deleted")
	}
	if err := threadRemove([]string{"never-existed"}); err == nil {
		t.Error("removing a thread that does not exist reported success")
	}
	if err := threadRemove(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("thread rm with no id returned %v, want the usage line", err)
	}
}

// whoami exists because the pid is what decides whether a release is accepted,
// and it is resolved from the environment rather than stated. It has to show
// the working: the value and where it came from.
func TestThreadWhoamiShowsThePidAndWhereItCameFrom(t *testing.T) {
	threadCmdEnv(t)
	t.Setenv("BERMUDA_PID", "4242")

	out, err := captureStdout(t, func() error { return threadWhoami([]string{"--as", "ada"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ada", "4242", "$BERMUDA_PID", "BERMUDA_PID=<value>"} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami output does not mention %q:\n%s", want, out)
		}
	}
}

// A bermuda run is told apart by its run id and carries no pid, so whoami has
// to say that rather than leave the reader hunting for a field that is absent
// on purpose.
func TestThreadWhoamiExplainsThatARunHasNoPid(t *testing.T) {
	threadCmdEnv(t)
	t.Setenv("BERMUDA_JOB_ID", "brief")
	t.Setenv("BERMUDA_RUN_DIR", filepath.Join(t.TempDir(), "20260814T000000Z-brief"))

	out, err := captureStdout(t, func() error { return threadWhoami(nil) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"brief", "20260814T000000Z-brief", "run id"} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami output does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pid source") {
		t.Errorf("a run's identity reported a pid source:\n%s", out)
	}
}

// An agent that cannot be named must not be given one. whoami is the command
// that says so before a claim fails in a way nobody can explain.
func TestThreadWhoamiRefusesToGuessAnIdentity(t *testing.T) {
	threadCmdEnv(t)
	_, err := captureStdout(t, func() error { return threadWhoami(nil) })
	if err == nil || !strings.Contains(err.Error(), "--as") {
		t.Fatalf("whoami with nothing set returned %v, want a refusal pointing at --as", err)
	}
}

// The log is one conversation unless --all is passed. Reading somebody else's
// project is the cost the whole feature exists to remove, so the narrowing is
// the behaviour a caller depends on.
func TestThreadLogStaysInOneThreadUntilAllIsPassed(t *testing.T) {
	ctx := threadCmdEnv(t)
	s := storeForEnv(t)
	post := func(thread, body string) {
		t.Helper()
		if _, err := s.ThreadPost(ctx, store.ThreadMessage{
			Thread: thread, Kind: store.KindNote,
			By: store.Identity{Name: "ada", PID: "1"}, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if err := threadNew([]string{"webapp"}); err != nil {
		t.Fatal(err)
	}
	post("webapp", "webapp message")
	post(store.GlobalThread, "global message")

	decode := func(argv []string) []store.ThreadMessage {
		t.Helper()
		out, err := captureStdout(t, func() error { return threadLog(argv) })
		if err != nil {
			t.Fatal(err)
		}
		var msgs []store.ThreadMessage
		if err := json.Unmarshal([]byte(out), &msgs); err != nil {
			t.Fatalf("thread log --json emitted something that is not JSON: %v\n%s", err, out)
		}
		return msgs
	}

	one := decode([]string{"--json", "--thread", "webapp"})
	if len(one) != 1 || one[0].Body != "webapp message" {
		t.Fatalf("--thread webapp read %v, want only the message in it", one)
	}
	all := decode([]string{"--json", "--all"})
	if len(all) != 2 {
		t.Fatalf("--all read %d messages, want every thread's", len(all))
	}
}

// The kind filter is how an agent reads only what changed without the claims
// around it, and an unknown kind has to be refused rather than silently
// matching nothing -- a filter that quietly returns an empty log looks like a
// quiet thread.
func TestThreadLogFiltersByKindAndRefusesAnUnknownOne(t *testing.T) {
	ctx := threadCmdEnv(t)
	s := storeForEnv(t)
	for _, m := range []store.ThreadMessage{
		{Thread: store.GlobalThread, Kind: store.KindNote, Body: "a note"},
		{Thread: store.GlobalThread, Kind: store.KindEvent, Body: "an event"},
	} {
		m.By = store.Identity{Name: "ada", PID: "1"}
		if _, err := s.ThreadPost(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error {
		return threadLog([]string{"--json", "--kind", "event"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []store.ThreadMessage
	if err := json.Unmarshal([]byte(out), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Kind != store.KindEvent {
		t.Fatalf("--kind event read %v, want only the event", msgs)
	}

	if _, err := captureStdout(t, func() error {
		return threadLog([]string{"--json", "--kind", "shout"})
	}); err == nil {
		t.Error("an unknown --kind was accepted, so the log silently matched nothing")
	}
}

// An empty log names the thread it read, because "nothing happened" about a
// conversation the caller did not mean to be reading is the same output as
// nothing having happened in the one they did.
func TestThreadLogNamesTheThreadItFoundEmpty(t *testing.T) {
	threadCmdEnv(t)
	if err := threadNew([]string{"webapp"}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return threadLog([]string{"--thread", "webapp"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "webapp") {
		t.Errorf("output %q does not name the thread that was empty", out)
	}

	out, err = captureStdout(t, func() error { return threadLog([]string{"--all"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "every thread") {
		t.Errorf("output %q does not say the read covered every thread", out)
	}
}

// A read that was cut short has to say so on stderr, and only on stderr: the
// notice is what stops an agent acting on a thread whose load-bearing message
// was the one past the limit, and a notice on stdout would break `--json`.
func TestThreadLogAnnouncesATruncatedReadOnStderr(t *testing.T) {
	ctx := threadCmdEnv(t)
	s := storeForEnv(t)
	for i := 0; i < 4; i++ {
		if _, err := s.ThreadPost(ctx, store.ThreadMessage{
			Thread: store.GlobalThread, Kind: store.KindNote,
			By: store.Identity{Name: "ada", PID: "1"}, Body: "message"}); err != nil {
			t.Fatal(err)
		}
	}

	var out string
	notice := captureStderr(t, func() {
		var err error
		out, err = captureStdout(t, func() error {
			return threadLog([]string{"--json", "--limit", "2"})
		})
		if err != nil {
			t.Errorf("thread log: %v", err)
		}
	})
	var msgs []store.ThreadMessage
	if err := json.Unmarshal([]byte(out), &msgs); err != nil {
		t.Fatalf("the notice landed on stdout and broke the JSON: %v\n%s", err, out)
	}
	if len(msgs) != 2 {
		t.Fatalf("--limit 2 read %d messages", len(msgs))
	}
	if !strings.Contains(notice, "showing the last 2 of 4") {
		t.Errorf("stderr = %q, want it to say the read was cut short", notice)
	}
}

// A limit past the ceiling is clamped rather than obeyed, and the caller is
// told: a request that was quietly reduced is one whose answer looks complete.
func TestThreadLogSaysWhenItClampedTheWindow(t *testing.T) {
	threadCmdEnv(t)
	notice := captureStderr(t, func() {
		if _, err := captureStdout(t, func() error {
			// 30 days, spelled in hours: --since takes a Go duration, which has
			// no day unit.
			return threadLog([]string{"--json", "--limit", "9999", "--since", "720h"})
		}); err != nil {
			t.Errorf("thread log: %v", err)
		}
	})
	if !strings.Contains(notice, "above the ceiling") {
		t.Errorf("stderr = %q, want the limit clamp to be announced", notice)
	}
	if !strings.Contains(notice, "past the ceiling") {
		t.Errorf("stderr = %q, want the age clamp to be announced", notice)
	}
}

// `thread with` hands the command's exit status back to whoever ran it, because
// that status is what a script wrapping itself in a lease branches on. Losing
// it would turn every failed command under a lease into a success.
func TestRunUnderClaimReportsTheCommandsStatus(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		want    int
	}{
		{"a command that worked", []string{"/bin/sh", "-c", "exit 0"}, 0},
		{"a command that failed", []string{"/bin/sh", "-c", "exit 3"}, 3},
		{"a command killed by a signal", []string{"/bin/sh", "-c", "kill -TERM $$"}, 143},
		{"a command that is not there", []string{"/no/such/command"}, 127},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got int
			// The failure paths print to stderr; capturing keeps the test output
			// readable and pins that the status, not the message, is the answer.
			captureStderr(t, func() { got = runUnderClaim(c.command) })
			if got != c.want {
				t.Errorf("runUnderClaim(%v) = %d, want %d", c.command, got, c.want)
			}
		})
	}
}
