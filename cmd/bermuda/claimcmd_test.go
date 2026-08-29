package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// The claim commands are the whole of bermuda's mutual exclusion as a caller
// meets it: `thread claim` takes the browser, `thread release` gives it back,
// `thread status` says who holds it. The flag parsing already has tests; what
// is untested is the exchange itself, and every failure mode here is silent —
// a second claim that succeeds means two agents drive one browser, and a
// release accepted from a non-holder means the resource is handed to whoever
// asked last rather than to nobody.

// claimStore opens the store the commands just wrote to. The commands resolve
// it from BERMUDA_STATE_DIR, which claimEnv points at a temp directory, so the
// assertions read the same database the command used.
func claimStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// heldNow is the live claim on a resource, or nil when it is free.
func heldNow(t *testing.T, resource string) *store.Claim {
	t.Helper()
	c, err := claimStore(t).ThreadClaimOf(context.Background(), resource, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// captureStdout collects what a command printed. The status command writes its
// JSON straight to os.Stdout, and that output is the machine-readable contract
// another agent parses.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = previous
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

// The first claim wins and the second is refused. Without this the resource is
// not a resource: two agents would both be told they hold the browser, and the
// only symptom would be two sessions typing into one window.
func TestThreadClaimRefusesAResourceSomebodyElseHolds(t *testing.T) {
	claimEnv(t)
	if _, err := captureStdout(t, func() error {
		return threadClaim([]string{"browser", "--as", "first", "--why", "scraping"})
	}); err != nil {
		t.Fatalf("the first claim on a free resource failed: %v", err)
	}

	_, err := captureStdout(t, func() error {
		return threadClaim([]string{"browser", "--as", "second"})
	})
	var held *store.HeldError
	if !errors.As(err, &held) {
		t.Fatalf("the second claim returned %v, want a HeldError naming the holder", err)
	}

	// The holder must still be the first agent: a refused claim that quietly
	// overwrote the row would be worse than one that succeeded.
	c := heldNow(t, "browser")
	if c == nil || c.Holder.Name != "first" {
		t.Fatalf("browser is held by %v, want the first claimant", c)
	}
}

// A release from somebody who does not hold the lease must fail, and must
// leave the lease where it was. This is the theft case: an agent tidying up
// after a run it did not own would free a browser another agent is mid-way
// through using.
func TestThreadReleaseRefusesANonHolderAndKeepsTheClaim(t *testing.T) {
	claimEnv(t)
	if _, err := captureStdout(t, func() error {
		return threadClaim([]string{"browser", "--as", "holder"})
	}); err != nil {
		t.Fatal(err)
	}

	_, err := captureStdout(t, func() error {
		return threadRelease([]string{"browser", "--as", "bystander"})
	})
	var notHeld *store.NotHeldError
	if !errors.As(err, &notHeld) {
		t.Fatalf("a stranger's release returned %v, want a NotHeldError", err)
	}
	if c := heldNow(t, "browser"); c == nil || c.Holder.Name != "holder" {
		t.Fatalf("browser is held by %v after a refused release, want the original holder", c)
	}
}

// The pair a caller depends on: release frees the resource, and the next agent
// can take it. A release that did not actually clear the row would park the
// browser forever with nobody holding it.
func TestThreadReleaseFreesTheResourceForTheNextClaimant(t *testing.T) {
	claimEnv(t)
	for _, step := range []func() error{
		func() error { return threadClaim([]string{"browser", "--as", "first"}) },
		func() error { return threadRelease([]string{"browser", "--as", "first"}) },
		func() error { return threadClaim([]string{"browser", "--as", "second"}) },
	} {
		if _, err := captureStdout(t, step); err != nil {
			t.Fatalf("the claim/release/claim sequence failed: %v", err)
		}
	}
	if c := heldNow(t, "browser"); c == nil || c.Holder.Name != "second" {
		t.Fatalf("browser is held by %v, want the second claimant", c)
	}
}

// Releasing twice is an error on purpose: the second release reports success
// only if the command has lost track of who holds what. The store documents
// this as deliberate non-idempotence, and the command must not soften it.
func TestThreadReleaseIsNotIdempotent(t *testing.T) {
	claimEnv(t)
	for _, step := range []func() error{
		func() error { return threadClaim([]string{"browser", "--as", "first"}) },
		func() error { return threadRelease([]string{"browser", "--as", "first"}) },
	} {
		if _, err := captureStdout(t, step); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := captureStdout(t, func() error {
		return threadRelease([]string{"browser", "--as", "first"})
	}); err == nil {
		t.Fatal("releasing an already-released resource succeeded, want an error")
	}
}

// A lapsed lease is not a hold. An agent whose ttl ran out has no claim to give
// back, and the resource is free for the next one — otherwise a crashed agent
// would take the browser out of circulation permanently.
func TestAnExpiredClaimIsNotAHold(t *testing.T) {
	claimEnv(t)
	if _, err := captureStdout(t, func() error {
		return threadClaim([]string{"browser", "--as", "first", "--ttl", "1ms"})
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	if c := heldNow(t, "browser"); c != nil {
		t.Fatalf("a lapsed lease still reads as held by %v", c.Holder)
	}
	if _, err := captureStdout(t, func() error {
		return threadClaim([]string{"browser", "--as", "second"})
	}); err != nil {
		t.Fatalf("claiming a resource whose lease lapsed failed: %v", err)
	}
}

// Waiting has to end. A --wait that outlived its deadline would hang an agent
// on a resource nobody is going to give back, which is indistinguishable from
// a hung command.
func TestAClaimThatWaitsGivesUpAtItsDeadline(t *testing.T) {
	claimEnv(t)
	if _, err := captureStdout(t, func() error {
		return threadClaim([]string{"browser", "--as", "first"})
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := captureStdout(t, func() error {
		return threadClaim([]string{"browser", "--as", "second", "--wait", "300ms"})
	})
	var held *store.HeldError
	if !errors.As(err, &held) {
		t.Fatalf("the waiting claim returned %v, want a HeldError once the wait ran out", err)
	}
	if waited := time.Since(start); waited < 300*time.Millisecond {
		t.Errorf("the claim gave up after %s, before its 300ms deadline", waited)
	}
}

// `thread status --json` is what another agent parses, so it has to be JSON
// even when nothing is claimed: an empty list, not null and not a prose line.
func TestThreadStatusJSONIsAListEvenWhenNothingIsClaimed(t *testing.T) {
	claimEnv(t)
	out, err := captureStdout(t, func() error { return threadStatus([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var claims []store.Claim
	if err := json.Unmarshal([]byte(out), &claims); err != nil {
		t.Fatalf("status --json emitted %q, which does not decode: %v", out, err)
	}
	if len(claims) != 0 {
		t.Fatalf("status listed %d claims on an empty store", len(claims))
	}
}

// Status reports every live hold, whichever thread it was taken from, and
// carries the reason across. An agent that finds the browser busy reads this to
// decide whether to wait, so the holder and the why are the payload.
func TestThreadStatusReportsEveryLiveHold(t *testing.T) {
	claimEnv(t)
	for _, step := range []func() error{
		func() error {
			return threadClaim([]string{"browser", "--as", "first", "--why", "scraping"})
		},
		func() error { return threadClaim([]string{"printer", "--as", "second"}) },
	} {
		if _, err := captureStdout(t, step); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error { return threadStatus([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var claims []store.Claim
	if err := json.Unmarshal([]byte(out), &claims); err != nil {
		t.Fatalf("status --json emitted %q, which does not decode: %v", out, err)
	}
	byResource := map[string]store.Claim{}
	for _, c := range claims {
		byResource[c.Resource] = c
	}
	if len(byResource) != 2 {
		t.Fatalf("status listed %v, want both holds", claims)
	}
	if got := byResource["browser"]; got.Holder.Name != "first" || got.Why != "scraping" {
		t.Errorf("the browser hold reads as %+v, want first holding it for scraping", got)
	}
	if got := byResource["printer"]; got.Holder.Name != "second" {
		t.Errorf("the printer hold reads as %+v, want second holding it", got)
	}

	// A released hold drops out of the report rather than lingering as history.
	if _, err := captureStdout(t, func() error {
		return threadRelease([]string{"printer", "--as", "second"})
	}); err != nil {
		t.Fatal(err)
	}
	out, err = captureStdout(t, func() error { return threadStatus([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "printer") {
		t.Errorf("status still reports the released printer: %s", out)
	}
}

// Both commands need a resource before they need anything else. Without the
// guard, `bermuda thread release --as me` would read the flag as the resource
// and fail somewhere further in with an error about the wrong thing.
func TestTheClaimCommandsNeedAResourceFirst(t *testing.T) {
	claimEnv(t)
	cases := []struct {
		name string
		run  func() error
	}{
		{"claim with no arguments", func() error { return threadClaim(nil) }},
		{"claim starting with a flag", func() error { return threadClaim([]string{"--as", "me"}) }},
		{"release with no arguments", func() error { return threadRelease(nil) }},
		{"release starting with a flag", func() error { return threadRelease([]string{"--as", "me"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := captureStdout(t, tc.run)
			if err == nil || !strings.Contains(err.Error(), "usage:") {
				t.Fatalf("got %v, want a usage error", err)
			}
		})
	}
}
