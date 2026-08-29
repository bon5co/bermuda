package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/herdrcli"
)

// fakeSpaces writes a herdr stand-in with a fixed answer for `workspace get`
// and `workspace create`, and a log of every call it was given.
func fakeSpaces(t *testing.T, getJSON, getExit, createJSON string) (*herdrcli.Client, func() string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	get := filepath.Join(dir, "get.json")
	create := filepath.Join(dir, "create.json")
	for path, body := range map[string]string{get: getJSON, create: createJSON} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	exitFile := filepath.Join(dir, "get.exit")
	if err := os.WriteFile(exitFile, []byte(getExit+"\n"), 0o644); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	// A created space is one herdr can be asked about afterwards, so create
	// becomes the answer to the next get. Without that the fake would let a
	// second caller conclude the space it just made does not exist.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + calls + "\n" +
		"if [ \"$2\" = \"get\" ]; then cat " + get + "; exit $(cat " + exitFile + "); fi\n" +
		"if [ \"$2\" = \"create\" ]; then cp " + create + " " + get + "; echo 0 > " + exitFile + "; cat " + create + "; exit 0; fi\n" +
		"printf '{\"id\":\"1\",\"result\":{}}\\n'\n"
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

func recordWorkspace(t *testing.T, stateDir, id, label string) {
	t.Helper()
	b, err := json.Marshal(workspaceRecord{ID: id, Label: label})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "workspace.json"), b, 0o644); err != nil {
		t.Fatalf("write record: %v", err)
	}
}

func spaceInfo(id, label string) string {
	return `{"id":"1","result":{"workspace":{"workspace_id":"` + id + `","label":"` + label + `"}}}`
}

// With nothing recorded, bermuda makes its own space rather than moving into
// one that already carries the name.
func TestEnsureWorkspaceCreatesAndRecords(t *testing.T) {
	state := t.TempDir()
	h, calls := fakeSpaces(t, "", "0", spaceInfo("w9", WorkspaceLabel))

	ws, err := EnsureWorkspace(context.Background(), h, state, "/home/x")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if ws.WorkspaceID != "w9" {
		t.Errorf("got %q, want the created space w9", ws.WorkspaceID)
	}
	if !strings.Contains(calls(), "workspace create") {
		t.Errorf("no create was attempted; calls were:\n%s", calls())
	}

	rec, err := readWorkspaceRecord(state)
	if err != nil || rec.ID != "w9" {
		t.Errorf("record = %+v, err = %v; want w9 written down", rec, err)
	}
}

// The recorded space is reused, and nothing new is made.
func TestEnsureWorkspaceReusesTheRecordedSpace(t *testing.T) {
	state := t.TempDir()
	recordWorkspace(t, state, "w3", WorkspaceLabel)
	h, calls := fakeSpaces(t, spaceInfo("w3", WorkspaceLabel), "0", spaceInfo("w9", WorkspaceLabel))

	ws, err := EnsureWorkspace(context.Background(), h, state, "/home/x")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if ws.WorkspaceID != "w3" {
		t.Errorf("got %q, want the recorded space w3", ws.WorkspaceID)
	}
	if strings.Contains(calls(), "workspace create") {
		t.Errorf("created a second space when it already had one:\n%s", calls())
	}
}

// This is the whole point. Herdr hands ids out again after a restart, so the id
// bermuda wrote down last week can be somebody else's space today — and opening
// runs into it is exactly the thing owning a space is meant to prevent.
func TestEnsureWorkspaceWillNotAdoptSomebodyElsesSpace(t *testing.T) {
	state := t.TempDir()
	recordWorkspace(t, state, "w3", WorkspaceLabel)
	h, _ := fakeSpaces(t, spaceInfo("w3", "Handler's notes"), "0", spaceInfo("w9", WorkspaceLabel))

	ws, err := EnsureWorkspace(context.Background(), h, state, "/home/x")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if ws.WorkspaceID == "w3" {
		t.Fatal("moved into a space that is no longer bermuda's")
	}
	rec, _ := readWorkspaceRecord(state)
	if rec.ID != "w9" {
		t.Errorf("record = %q, want the newly created w9", rec.ID)
	}
}

// A space that has been closed is gone, not an error to report: bermuda makes
// another and carries on.
func TestEnsureWorkspaceReplacesAClosedSpace(t *testing.T) {
	state := t.TempDir()
	recordWorkspace(t, state, "w3", WorkspaceLabel)
	gone := `{"id":"1","error":{"code":"workspace_not_found","message":"no such workspace"}}`
	h, _ := fakeSpaces(t, gone, "1", spaceInfo("w9", WorkspaceLabel))

	ws, err := EnsureWorkspace(context.Background(), h, state, "/home/x")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if ws.WorkspaceID != "w9" {
		t.Errorf("got %q, want a fresh space", ws.WorkspaceID)
	}
}

// Two processes wanting the space at once — the daemon and the board, on a
// machine where neither has run before — must end up in one space, not two.
func TestEnsureWorkspaceIsOneSpaceUnderConcurrency(t *testing.T) {
	state := t.TempDir()
	h, calls := fakeSpaces(t, "", "0", spaceInfo("w9", WorkspaceLabel))

	done := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ws, err := EnsureWorkspace(context.Background(), h, state, "/home/x")
			if err != nil || ws == nil {
				done <- ""
				return
			}
			done <- ws.WorkspaceID
		}()
	}
	for i := 0; i < 2; i++ {
		if id := <-done; id != "w9" {
			t.Errorf("caller %d got %q, want w9", i, id)
		}
	}
	if n := strings.Count(calls(), "workspace create"); n != 1 {
		t.Errorf("created %d spaces, want exactly 1:\n%s", n, calls())
	}
}
