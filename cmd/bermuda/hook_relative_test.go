//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstalledHookWorksWithRelativeStateOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := "state"
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_, e := hookEvent(t, dir)
	writeHook(t, dir, "cat > received.json\nprintf '%s\\n' \"$PWD\" \"$BERMUDA_STATE_DIR\" > paths.txt\nprintf ok > \"$BERMUDA_STATE_DIR/via-env.txt\"\n")
	skipped, err := runSettledHook(context.Background(), stateDir(), e, time.Second)
	if err != nil || skipped {
		t.Fatalf("installed hook must execute with relative BERMUDA_STATE_DIR: skipped=%v err=%v", skipped, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "received.json")); err != nil {
		t.Fatal(err)
	}

	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := os.ReadFile(filepath.Join(dir, "paths.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(paths) != strings.Repeat(want+"\n", 2) {
		t.Fatalf("hook cwd/state paths=%q, want %q", paths, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "via-env.txt")); err != nil {
		t.Fatal(err)
	}
}
