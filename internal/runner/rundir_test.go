package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The run dir sits outside the job's working directory, and acceptEdits only
// covers the working directory. Without the grant the agent parks on an approval
// prompt for the one file bermuda requires it to write — an unattended run that
// asks a question nobody is there to answer.
func TestRunDirAccessIsGrantedToClaude(t *testing.T) {
	args := []string{"--model", "opus", "--permission-mode", "acceptEdits"}
	got := withRunDirAccess("claude", args, "/state/runs/r1")

	want := []string{"--model", "opus", "--permission-mode", "acceptEdits", "--add-dir", "/state/runs/r1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

// A caller that already manages directory access keeps it. Appending a second
// grant on top of a deliberate one widens access past what the caller decided,
// which is the opposite of the point: this widens by exactly one bermuda-owned
// directory or not at all.
func TestRunDirAccessDefersToACallerManagingDirs(t *testing.T) {
	args := []string{"--model", "opus", "--add-dir", "/only/here"}
	got := withRunDirAccess("claude", args, "/state/runs/r1")
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("args = %v, want them untouched: %v", got, args)
	}
}

// Only claude's flag spellings are modelled. Handing another agent kind a
// --add-dir it does not understand turns a run that would have worked into an
// agent that refuses its own arguments.
func TestRunDirAccessLeavesOtherKindsAlone(t *testing.T) {
	for _, kind := range []string{"codex", "gemini", ""} {
		args := []string{"--profile", "x"}
		got := withRunDirAccess(kind, args, "/state/runs/r1")
		if !reflect.DeepEqual(got, args) {
			t.Errorf("kind %q: args = %v, want them untouched: %v", kind, got, args)
		}
	}
}

// ExecuteIn calls this on job.AgentArgs, which come from the job spec. If the
// grant were appended in place it could write into the caller's backing array,
// so a second run of the same job would inherit the first run's directory — the
// wrong run dir, silently, and only for reused specs.
func TestRunDirAccessDoesNotWriteThroughToTheCaller(t *testing.T) {
	args := make([]string, 2, 8) // spare capacity: append would land in place
	copy(args, []string{"--model", "opus"})

	first := withRunDirAccess("claude", args, "/state/runs/r1")
	second := withRunDirAccess("claude", args, "/state/runs/r2")

	if len(args) != 2 {
		t.Fatalf("caller's args grew to %v", args)
	}
	if got := first[len(first)-1]; got != "/state/runs/r1" {
		t.Errorf("first run's grant became %q", got)
	}
	if got := second[len(second)-1]; got != "/state/runs/r2" {
		t.Errorf("second run's grant became %q", got)
	}
}

// result.json is the sole authority on a run's outcome, and classify() tells the
// two failure modes apart with os.IsNotExist: a missing file may still be an
// agent that was lost and is still working (park as agent_lost), while a present
// unreadable one is the agent's own mistake (park as bad_result). Wrapping the
// read error would collapse both into one park reason.
func TestReadResultTellsAMissingFileFromAMalformedOne(t *testing.T) {
	missing := t.TempDir()
	if _, err := ReadResult(missing); !os.IsNotExist(err) {
		t.Fatalf("missing result.json gave err %v, want one os.IsNotExist accepts", err)
	}

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "result.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadResult(bad)
	if err == nil {
		t.Fatal("malformed result.json read without error")
	}
	if os.IsNotExist(err) {
		t.Errorf("malformed result.json reported as missing: %v", err)
	}
}

// Reconciliation reads a run's outcome out of this file long after whatever
// supervised the run has died, so every field a caller acts on has to survive
// the round trip — including the opaque data blob, which is passed through to
// the job's own consumers.
func TestReadResultCarriesTheWholeResult(t *testing.T) {
	dir := t.TempDir()
	body := `{"status":"error","note":"herdr lost the pane","data":{"attempts":3}}`
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ReadResult(dir)
	if err != nil {
		t.Fatalf("ReadResult: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("status = %q, want error", res.Status)
	}
	if res.Note != "herdr lost the pane" {
		t.Errorf("note = %q", res.Note)
	}
	var data struct{ Attempts int }
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("data did not survive as raw JSON: %v", err)
	}
	if data.Attempts != 3 {
		t.Errorf("data.attempts = %d, want 3", data.Attempts)
	}
}

// A result the runner itself wrote has to be readable by the reader
// reconciliation uses. These are the two halves of one contract, and they are in
// different files.
func TestWrittenResultRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := writeResult(dir, Result{Status: "ok", Note: "covered the runner"}); err != nil {
		t.Fatal(err)
	}
	res, err := ReadResult(dir)
	if err != nil {
		t.Fatalf("ReadResult after writeResult: %v", err)
	}
	if res.Status != "ok" || !strings.Contains(res.Note, "covered") {
		t.Errorf("round trip gave %+v", res)
	}
}
