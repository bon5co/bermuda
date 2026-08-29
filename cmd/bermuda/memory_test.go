package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The memory command's guards, driven the way they are typed.
//
// The dangerous path is init: it touches a directory that may already hold a
// person's notes, and every branch that refuses instead of replacing is a
// promise these tests hold it to. The resolution order matters too — an agent
// that reads `memory path` and writes there must land in the same place a
// later `init` would.

func TestMemoryCmdRefusesAnUnknownSubcommand(t *testing.T) {
	if err := memoryCmd(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("memoryCmd with no arguments returned %v, want the usage line", err)
	}
	err := memoryCmd([]string{"pth"})
	if err == nil || !strings.Contains(err.Error(), "pth") {
		t.Errorf("memoryCmd(pth) returned %v, want an error naming the typo", err)
	}
}

func TestMemoryPathPrefersTheOverrideThenTheStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", state)
	t.Setenv("BERMUDA_MEMORY_DIR", "")
	if got, want := memoryDir(), filepath.Join(state, "memory"); got != want {
		t.Errorf("memoryDir() = %q, want %q", got, want)
	}
	override := t.TempDir()
	t.Setenv("BERMUDA_MEMORY_DIR", override)
	if got := memoryDir(); got != override {
		t.Errorf("memoryDir() with override = %q, want %q", got, override)
	}
}

func TestMemoryInitSeedsTheIndexOnceAndOnlyOnce(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("BERMUDA_MEMORY_DIR", "")
	if _, err := captureStdout(t, func() error { return memoryInit(nil) }); err != nil {
		t.Fatalf("memory init: %v", err)
	}
	index := filepath.Join(memoryDir(), "MEMORY.md")
	seeded, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("index after init: %v", err)
	}

	// A second init must not eat notes: the index a person has grown is not
	// re-seeded, and the run still succeeds so init stays safe to script.
	if err := os.WriteFile(index, []byte("- [a fact](fact.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return memoryInit(nil) }); err != nil {
		t.Fatalf("second memory init: %v", err)
	}
	kept, _ := os.ReadFile(index)
	if string(kept) != "- [a fact](fact.md)\n" {
		t.Errorf("second init rewrote the index to %q", kept)
	}
	if len(seeded) == 0 {
		t.Error("first init seeded an empty index")
	}
}

func TestMemoryInitWiresAVaultAndRefusesToRewire(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("BERMUDA_MEMORY_DIR", "")
	vault := filepath.Join(t.TempDir(), "vault", "memory")
	if _, err := captureStdout(t, func() error { return memoryInit([]string{"--vault", vault}) }); err != nil {
		t.Fatalf("memory init --vault: %v", err)
	}
	if target, err := os.Readlink(memoryDir()); err != nil || target != vault {
		t.Errorf("memory is %q -> %q (%v), want a link to %q", memoryDir(), target, err, vault)
	}
	if _, err := os.Stat(filepath.Join(vault, "MEMORY.md")); err != nil {
		t.Errorf("index in the vault: %v", err)
	}

	// Same vault again is idempotent; a different vault is refused, because
	// silently repointing the link strands every note in the old one.
	if _, err := captureStdout(t, func() error { return memoryInit([]string{"--vault", vault}) }); err != nil {
		t.Errorf("re-init with the same vault: %v", err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere")
	err := memoryInit([]string{"--vault", other})
	if err == nil || !strings.Contains(err.Error(), "already links") {
		t.Errorf("re-init with another vault returned %v, want a refusal naming the current link", err)
	}
}

func TestMemoryInitRefusesToReplaceARealDirectory(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("BERMUDA_MEMORY_DIR", "")
	if err := os.MkdirAll(memoryDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	err := memoryInit([]string{"--vault", filepath.Join(t.TempDir(), "vault")})
	if err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("init --vault over a real directory returned %v, want a refusal that says how to migrate", err)
	}
}
