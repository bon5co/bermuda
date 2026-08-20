package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/flow"
	"github.com/bon5co/bermuda/v2/internal/memory"
)

// The state directory is the one thing every part of bermuda has to agree on.
// The daemon writes the store, the board pane reads it, and the sentinel takes
// its lock beside it; if any of them resolves a different directory the failure
// is silent — a board that shows no jobs, a sentinel that guards nothing, a
// second database nobody looks at. So the resolution rules are asserted rather
// than left to the doc comment.
func TestStateDirResolution(t *testing.T) {
	tests := []struct {
		name    string
		bermuda string
		plugin  string
		home    string
		want    string
	}{
		{
			name:    "override wins",
			bermuda: "/tmp/explicit",
			home:    "/home/someone",
			want:    "/tmp/explicit",
		},
		{
			name:   "home fallback",
			home:   "/home/someone",
			want:   "/home/someone/.bermuda",
			plugin: "",
		},
		{
			// HERDR_PLUGIN_STATE_DIR is set for anything herdr launches, so
			// honouring it would give the board opened as a plugin pane a
			// different database from the one the daemon writes.
			name:   "herdr plugin state dir is ignored",
			plugin: "/tmp/herdr-plugin",
			home:   "/home/someone",
			want:   "/home/someone/.bermuda",
		},
		{
			// Both set: the override still decides, so a plugin pane started
			// with an explicit state directory lands on that one and not on
			// herdr's.
			name:    "override wins over herdr plugin state dir",
			bermuda: "/tmp/explicit",
			plugin:  "/tmp/herdr-plugin",
			home:    "/home/someone",
			want:    "/tmp/explicit",
		},
		{
			// An empty override is not an override. Exported-but-empty is what
			// a shell leaves behind after `export BERMUDA_STATE_DIR=`, and
			// treating it as a state directory would put the store at the
			// filesystem root.
			name:    "empty override falls back",
			bermuda: "",
			home:    "/home/someone",
			want:    "/home/someone/.bermuda",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BERMUDA_STATE_DIR", tc.bermuda)
			t.Setenv("HERDR_PLUGIN_STATE_DIR", tc.plugin)
			t.Setenv("HOME", tc.home)

			if got := stateDir(); got != tc.want {
				t.Errorf("stateDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Everything bermuda keeps lives inside the state directory and nowhere else.
// That is the whole promise of "one visible directory rather than the XDG
// split": a person can find the lot by looking in one place, and a test machine
// pointed at a temp directory leaves nothing behind in the real one. A helper
// that quietly resolved against $HOME instead would still work on the developer
// machine and scatter files on every other.
func TestStateDirContainsEverythingBermudaWrites(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(t.TempDir(), "herdr"))
	// Set so an accidental fallback lands somewhere identifiable rather than on
	// the developer's real home directory.
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	// The memory directory has an override of its own, which must not be in
	// force while the default layout is under test.
	t.Setenv("BERMUDA_MEMORY_DIR", "")

	paths := map[string]string{
		"stop file":     stopFile(),
		"flow dir":      flowDir(),
		"memory dir":    memory.Dir(stateDir()),
		"run dir":       runDirFor("20260101T000000Z-somejob"),
		"sentinel lock": lockPath(roleSentinel),
	}

	for what, p := range paths {
		if !filepath.IsAbs(p) {
			t.Errorf("%s = %q, want an absolute path", what, p)
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s = %q, which is outside the state directory %q", what, p, root)
		}
	}
}

// The memory directory is the one part of the layout that is meant to be
// movable: a vault usually already has a home, so BERMUDA_MEMORY_DIR points at
// it. Nothing else follows that override.
func TestMemoryDirOverrideMovesOnlyMemory(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(t.TempDir(), "vault")
	t.Setenv("BERMUDA_STATE_DIR", root)
	t.Setenv("BERMUDA_MEMORY_DIR", vault)

	if got := memory.Dir(stateDir()); got != vault {
		t.Errorf("memory.Dir = %q, want the override %q", got, vault)
	}
	if got := flowDir(); got != flow.Dir(root) {
		t.Errorf("flowDir = %q, want %q — the memory override must not move it", got, flow.Dir(root))
	}
	if got := stopFile(); got != filepath.Join(root, "stopped") {
		t.Errorf("stopFile = %q, want it still under %q", got, root)
	}
}
