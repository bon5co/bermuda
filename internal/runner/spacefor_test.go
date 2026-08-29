package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/herdrcli"
)

// fakeSpaceServer is a herdr stand-in that answers `workspace get` per id: the
// ids in known are found, every other id is a not-found error envelope on
// stderr, and `workspace create` mints ownID and makes it findable afterwards.
//
// Per-id answers are the point: spaceFor has to tell "the space this job named
// is still there" from "it is gone", and a fake with one answer for every id
// cannot express the difference.
func fakeSpaceServer(t *testing.T, ownID string, known ...string) (*herdrcli.Client, func() string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	found := filepath.Join(dir, "found")

	if err := os.WriteFile(found, []byte(strings.Join(known, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write found ids: %v", err)
	}

	space := func(id string) string {
		return `{"id":"1","result":{"workspace":{"workspace_id":"` + id + `","label":"` + WorkspaceLabel + `"}}}`
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + calls + "\n" +
		"if [ \"$2\" = \"get\" ]; then\n" +
		"  if grep -qx \"$3\" " + found + "; then\n" +
		"    printf '%s\\n' '{\"id\":\"1\",\"result\":{\"workspace\":{\"workspace_id\":\"'\"$3\"'\",\"label\":\"" + WorkspaceLabel + "\"}}}'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"  printf '%s\\n' '{\"id\":\"1\",\"error\":{\"code\":\"not_found\",\"message\":\"no such workspace\"}}' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"if [ \"$2\" = \"create\" ]; then\n" +
		"  printf '%s\\n' \"" + ownID + "\" >> " + found + "\n" +
		"  printf '%s\\n' '" + space(ownID) + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf '%s\\n' '{\"id\":\"1\",\"result\":{}}'\n"

	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake herdr: %v", err)
	}
	return &herdrcli.Client{Bin: bin}, func() string {
		b, err := os.ReadFile(calls)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// A job that names a space gets that space, and bermuda's own is never touched.
//
// This is what keeps a flow's steps together: every step names the flow's
// space, and a run that quietly landed somewhere else would put the steps in
// different threads without anything reporting a problem.
func TestSpaceForHonoursTheSpaceTheJobNamed(t *testing.T) {
	h, calls := fakeSpaceServer(t, "ws-own", "ws-flow")
	r := &Runner{Herdr: h, StateDir: t.TempDir()}

	got, err := r.spaceFor(context.Background(), Job{WorkspaceID: "ws-flow"})
	if err != nil {
		t.Fatalf("spaceFor: %v", err)
	}
	if got != "ws-flow" {
		t.Errorf("run went to space %q, want the one the job named, ws-flow", got)
	}
	if strings.Contains(calls(), "create") {
		t.Error("a space was created even though the named one was there")
	}
}

// A named space herdr no longer has falls back rather than failing.
//
// The space is where a run is legible, not what makes it possible. A closed
// window must not be able to stop the work.
func TestSpaceForFallsBackWhenTheNamedSpaceIsGone(t *testing.T) {
	h, calls := fakeSpaceServer(t, "ws-own")
	r := &Runner{Herdr: h, StateDir: t.TempDir()}

	got, err := r.spaceFor(context.Background(), Job{WorkspaceID: "ws-closed"})
	if err != nil {
		t.Fatalf("a closed space refused the run: %v", err)
	}
	if got != "ws-own" {
		t.Errorf("fell back to space %q, want bermuda's own, ws-own", got)
	}
	if !strings.Contains(calls(), "create") {
		t.Error("nothing was created, so the run had no space to fall back to")
	}
}

// A job that names no space lands in bermuda's own.
func TestSpaceForUsesBermudasOwnWhenNoneIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"unset", ""},
		{"blank", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := fakeSpaceServer(t, "ws-own")
			r := &Runner{Herdr: h, StateDir: t.TempDir()}

			got, err := r.spaceFor(context.Background(), Job{WorkspaceID: tc.id})
			if err != nil {
				t.Fatalf("spaceFor: %v", err)
			}
			if got != "ws-own" {
				t.Errorf("run went to space %q, want bermuda's own, ws-own", got)
			}
		})
	}
}

// Two runs in a row share one space: the second reuses the record the first
// wrote instead of making a second space nobody is watching.
func TestSpaceForReusesBermudasSpaceAcrossRuns(t *testing.T) {
	h, calls := fakeSpaceServer(t, "ws-own")
	r := &Runner{Herdr: h, StateDir: t.TempDir()}

	first, err := r.spaceFor(context.Background(), Job{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := r.spaceFor(context.Background(), Job{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first != second {
		t.Errorf("two runs landed in different spaces, %q and %q", first, second)
	}
	if n := strings.Count(calls(), "create"); n != 1 {
		t.Errorf("workspace create called %d times, want 1", n)
	}
}
