package main

import (
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/runner"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// The auto-compact window, from the flag to the agent's argv.
//
// It is worth covering because every failure here is silent in the way that
// matters: a bad value stored now surfaces as an agent refusing to start in an
// unattended 04:00 run, and a value that never reaches argv looks exactly like
// a value that did — the run simply costs what it always did.

func TestValidateAutoCompactBounds(t *testing.T) {
	for _, ok := range []string{"", "auto", "100000", "200000", "1000000"} {
		if err := validateAutoCompact(ok); err != nil {
			t.Errorf("validateAutoCompact(%q) = %v, want accepted", ok, err)
		}
	}
	// 200k is the spelling a person reaches for first, and claude does not take
	// it; catching it here is the whole point of validating on the way in.
	for _, bad := range []string{"200k", "2000000", "99999", "-1", "lots"} {
		if err := validateAutoCompact(bad); err == nil {
			t.Errorf("validateAutoCompact(%q) = nil, want a refusal", bad)
		}
	}
}

func TestBuildAgentArgsCarriesAutoCompact(t *testing.T) {
	args := runner.BuildAgentArgs(store.Job{Kind: "claude", Model: "opus", AutoCompact: "200000"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--autocompact 200000") {
		t.Errorf("argv = %q, want it to carry --autocompact 200000", joined)
	}

	// Absent by default: a job that never asked for a window must not have one
	// invented for it, or every existing job silently changes behaviour.
	plain := strings.Join(runner.BuildAgentArgs(store.Job{Kind: "claude", Model: "opus"}), " ")
	if strings.Contains(plain, "--autocompact") {
		t.Errorf("argv = %q, want no --autocompact when the job sets none", plain)
	}
}

func TestAutoCompactPassthroughWinsOverTheField(t *testing.T) {
	// claude takes the last occurrence, so a job that spells the flag into
	// --extra-args must override the modelled field rather than fight it.
	args := runner.BuildAgentArgs(store.Job{
		Kind:        "claude",
		Model:       "opus",
		AutoCompact: "200000",
		ExtraArgs:   "--autocompact 150000",
	})
	last := ""
	for i, a := range args {
		if a == "--autocompact" && i+1 < len(args) {
			last = args[i+1]
		}
	}
	if last != "150000" {
		t.Errorf("last --autocompact value = %q, want the passthrough's 150000", last)
	}
}

func TestAutoCompactSurvivesAStoreRoundTrip(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	job := store.Job{ID: "ac", Prompt: "x", Kind: "claude", AutoCompact: "200000"}
	if err := st.PutJob(ctx, job); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	got, err := st.Job(ctx, "ac")
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if got.AutoCompact != "200000" {
		t.Errorf("AutoCompact after round trip = %q, want 200000", got.AutoCompact)
	}
}
