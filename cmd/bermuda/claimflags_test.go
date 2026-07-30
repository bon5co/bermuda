package main

import (
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// A claim is the one thing in bermuda that stops two agents driving the same
// browser, and every field of it is assembled here. `thread claim` and `thread
// with` both go through this function precisely so they cannot disagree about
// what a lease means, so what it builds is worth pinning field by field.

// claimEnv isolates a parse from whatever the test process was started with.
//
// HERDR_PANE_ID is cleared for the same reason the thread tests clear the
// workspace: with a pane id set, resolveIdentity registers the name with the
// real herdr on this machine, and a unit test must not rename Handler's live
// agents. It is also a pid source, so leaving it set would make the identity
// depend on where the suite was run from.
func claimEnv(t *testing.T) {
	t.Helper()
	noWorkspace(t)
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("BERMUDA_THREAD", "")
	t.Setenv("BERMUDA_JOB_ID", "")
	t.Setenv("BERMUDA_RUN_DIR", "")
	t.Setenv("BERMUDA_THREAD_AGENT", "")
	t.Setenv("BERMUDA_ROOM_AGENT", "")
	t.Setenv("BERMUDA_PID", "4242")
}

// parseClaim runs the real parser under a flag set that reports errors instead
// of exiting. The callers pass flag.ExitOnError, which would take the test
// binary down with it on the cases that are supposed to fail.
func parseClaim(t *testing.T, argv ...string) (store.ClaimRequest, time.Duration, error) {
	t.Helper()
	fs := flag.NewFlagSet("thread claim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return parseClaimFlags(fs, argv, "usage: bermuda thread claim <resource>")
}

// The whole request, from the form the command is actually typed in.
func TestParseClaimFlagsBuildsTheWholeRequest(t *testing.T) {
	claimEnv(t)

	req, wait, err := parseClaim(t, "browser", "--ttl", "20m", "--why", "posting listings",
		"--wait", "5m", "--as", "ada", "--thread", "webapp")
	if err != nil {
		t.Fatalf("parseClaimFlags: %v", err)
	}
	if req.Resource != "browser" {
		t.Errorf("resource = %q, want the positional argument", req.Resource)
	}
	if req.TTL != 20*time.Minute {
		t.Errorf("ttl = %v, want 20m", req.TTL)
	}
	if req.Why != "posting listings" {
		t.Errorf("why = %q, want the reason as typed", req.Why)
	}
	if req.By.Name != "ada" {
		t.Errorf("holder = %q, want --as", req.By.Name)
	}
	// --wait is returned beside the request rather than inside it: it says how
	// long this caller will block, which is nothing to do with the lease that
	// gets written down. A wait that leaked into the record would read as a TTL.
	if wait != 5*time.Minute {
		t.Errorf("wait = %v, want 5m", wait)
	}
}

// The resource comes first, positionally, and there is no flag for it.
//
// A caller who types the flags first loses the resource entirely, and a claim
// on "" is a lease on nothing that still reports success — so it has to fail
// here rather than reach the store.
func TestParseClaimFlagsNeedsTheResourceFirst(t *testing.T) {
	claimEnv(t)

	for _, argv := range [][]string{
		{},
		{"--ttl", "20m", "browser"},
		{"--as", "ada"},
	} {
		if req, _, err := parseClaim(t, argv...); err == nil {
			t.Errorf("%v parsed, resource = %q: a claim with no resource must be refused",
				argv, req.Resource)
		}
	}
}

// An unnamed holder is refused outright.
//
// This is the one resolution in bermuda with no fallback, and it must stay that
// way: guessing wrongly makes two agents into one lease holder, which is how a
// release hands the browser to somebody who is still using it.
func TestParseClaimFlagsRefusesAnUnidentifiableCaller(t *testing.T) {
	claimEnv(t)

	_, _, err := parseClaim(t, "browser", "--ttl", "20m")
	if err == nil {
		t.Fatal("a claim was assembled for a caller with no name")
	}
	if !strings.Contains(err.Error(), "--as") {
		t.Errorf("error = %q, want it to say how to name yourself", err)
	}
}

// A run claims as its job, and carries the run it came from.
//
// The run id is what makes two runs of one job two holders; without it the
// second run of a nightly job can release the first one's lease.
func TestParseClaimFlagsTakesItsIdentityFromTheRun(t *testing.T) {
	claimEnv(t)
	t.Setenv("BERMUDA_JOB_ID", "listings-sweep")
	t.Setenv("BERMUDA_RUN_DIR", filepath.Join(t.TempDir(), "20260731T090000Z-listings-sweep"))

	req, _, err := parseClaim(t, "browser", "--ttl", "20m")
	if err != nil {
		t.Fatalf("parseClaimFlags: %v", err)
	}
	if req.By.JobID != "listings-sweep" || req.By.Name != "listings-sweep" {
		t.Errorf("holder = %+v, want the job id", req.By)
	}
	if req.By.RunID != "20260731T090000Z-listings-sweep" {
		t.Errorf("run = %q, want the run directory's name", req.By.RunID)
	}
}

// --thread decides where the claim is written down, never what it covers.
//
// The distinction is the whole reason the field is not called a scope: the
// resource is taken from everybody in every thread. A test that let Thread
// narrow anything would be describing a lock that does not lock.
func TestParseClaimFlagsThreadOnlyRecordsWhereItWasTaken(t *testing.T) {
	claimEnv(t)

	req, _, err := parseClaim(t, "browser", "--as", "ada", "--thread", "webapp")
	if err != nil {
		t.Fatalf("parseClaimFlags: %v", err)
	}
	if req.Thread != "webapp" {
		t.Errorf("thread = %q, want the flag", req.Thread)
	}
	if req.Resource != "browser" {
		t.Errorf("resource = %q: the thread must not qualify the resource", req.Resource)
	}

	// With no flag it follows the same rule as every other thread-taking
	// command, so an agent that exported BERMUDA_THREAD for its run has its
	// claims recorded in the conversation the rest of its work is in.
	t.Setenv("BERMUDA_THREAD", "tiktok-deals")
	if req, _, err := parseClaim(t, "browser", "--as", "ada"); err != nil || req.Thread != "tiktok-deals" {
		t.Errorf("thread = %q, %v, want $BERMUDA_THREAD", req.Thread, err)
	}

	t.Setenv("BERMUDA_THREAD", "")
	if req, _, err := parseClaim(t, "browser", "--as", "ada"); err != nil || req.Thread != store.GlobalThread {
		t.Errorf("thread = %q, %v, want global", req.Thread, err)
	}
}

// A malformed thread id fails the claim rather than being slugged into a new
// conversation nobody else is reading.
func TestParseClaimFlagsRefusesAMalformedThread(t *testing.T) {
	claimEnv(t)

	if _, _, err := parseClaim(t, "browser", "--as", "ada", "--thread", "Better Lingo"); err == nil {
		t.Error("a claim was recorded against a thread id that is not one")
	}
}

// Defaults, which are the values `thread with` then refuses on.
//
// TTL zero means a lease that never lapses. It is the right default for `thread
// claim`, where somebody is present to release it, and the wrong one for a
// wrapper that can be killed — so the zero has to arrive here intact for that
// caller to be able to reject it.
func TestParseClaimFlagsDefaultsToAnUnboundedLease(t *testing.T) {
	claimEnv(t)

	req, wait, err := parseClaim(t, "browser", "--as", "ada")
	if err != nil {
		t.Fatalf("parseClaimFlags: %v", err)
	}
	if req.TTL != 0 {
		t.Errorf("ttl = %v, want zero so the caller can decide whether that is allowed", req.TTL)
	}
	if wait != 0 {
		t.Errorf("wait = %v, want zero: an unasked-for wait must not block", wait)
	}
	if req.Why != "" {
		t.Errorf("why = %q, want empty", req.Why)
	}
	if !req.Now.IsZero() {
		t.Errorf("now = %v, want zero so the store stamps it", req.Now)
	}
}

// A mistyped flag stops the claim instead of being swallowed.
//
// `--tll 20m` silently ignored is the shape that hurts: the caller believes the
// lease lapses in twenty minutes and it never does.
func TestParseClaimFlagsRejectsUnknownFlags(t *testing.T) {
	claimEnv(t)

	if _, _, err := parseClaim(t, "browser", "--as", "ada", "--tll", "20m"); err == nil {
		t.Error("a misspelled flag was accepted, so its value was silently dropped")
	}
	if _, _, err := parseClaim(t, "browser", "--as", "ada", "--ttl", "twenty"); err == nil {
		t.Error("an unparseable duration was accepted")
	}
}
