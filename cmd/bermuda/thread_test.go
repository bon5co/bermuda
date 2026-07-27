package main

import (
	"testing"

	"github.com/bon5co/bermuda/internal/store"
)

// Which thread a command writes into decides who ever reads it, so the
// selection rule is worth pinning: an agent that exported $BERMUDA_THREAD for
// its whole run must not have one stray command land in global, and a command
// that names a thread must not be overridden by the environment it inherited.
func TestResolveThreadPrefersTheFlagThenTheEnvironment(t *testing.T) {
	t.Setenv("BERMUDA_THREAD", "tiktok-deals")
	if got, err := resolveThread("webapp"); err != nil || got != "webapp" {
		t.Errorf("--thread resolved to %q, %v: the flag has to beat the environment", got, err)
	}
	if got, err := resolveThread(""); err != nil || got != "tiktok-deals" {
		t.Errorf("with no flag the thread resolved to %q, %v, want $BERMUDA_THREAD", got, err)
	}

	t.Setenv("BERMUDA_THREAD", "")
	if got, err := resolveThread(""); err != nil || got != store.GlobalThread {
		t.Errorf("with nothing set the thread resolved to %q, %v, want global", got, err)
	}
}

// A malformed id is refused rather than slugged, and that has to hold for the
// environment too: a shell exporting BERMUDA_THREAD="Better Lingo" should be
// told, not quietly given a second conversation nobody else is in.
func TestResolveThreadRefusesAMalformedID(t *testing.T) {
	if _, err := resolveThread("Better Lingo"); err == nil {
		t.Error("--thread accepted an id that is not one")
	}
	t.Setenv("BERMUDA_THREAD", "Better Lingo")
	if _, err := resolveThread(""); err == nil {
		t.Error("$BERMUDA_THREAD accepted an id that is not one")
	}
}
