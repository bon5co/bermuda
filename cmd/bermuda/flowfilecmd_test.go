package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/flow"
)

// The read-only half of the flow file verbs: list, show, edit.
//
// None of them changes anything on its own, which is exactly why a wrong answer
// here goes unnoticed: a flow missing from `flow list` reads as a flow that was
// never made, and a `flow show` that reformats the file is pasted back as a
// different flow. Everything below points BERMUDA_STATE_DIR at a temporary
// directory, so nothing touches the real ~/.bermuda/flows this machine runs on.

// flowFilesIn points the flow verbs at an empty temporary state directory.
func flowFilesIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	// An editor inherited from the surrounding shell would make `flow edit`
	// spawn a real one in the middle of the test run.
	t.Setenv("BERMUDA_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	return dir
}

// An installation with no flows yet has to say so in a way that names the verb
// that makes one. An empty table is indistinguishable from a listing that
// failed.
func TestFlowListOnAnEmptyInstallationSaysHowToMakeOne(t *testing.T) {
	flowFilesIn(t)

	out, err := captureStdout(t, func() error { return flowList(nil) })
	if err != nil {
		t.Fatalf("listing flows before any exist failed: %v", err)
	}
	if !strings.Contains(out, "flow new") {
		t.Errorf("said %q with no flows, want the command that writes one", out)
	}
}

// The listing is what somebody reads to pick a flow to run, so every column has
// to carry the value the caller decides on: how many steps it has, whether it
// needs an input, and what it is for.
func TestFlowListReportsStepCountInputAndAbout(t *testing.T) {
	flowFilesIn(t)
	writeFlow(t, "triage", "about: sort the inbox\ninput: which inbox\nsteps:\n"+
		"  - id: one\n    run: true\n"+
		"  - id: two\n    run: true\n")
	// A flow that takes no input must not print a blank cell, which reads as
	// missing data rather than "supply nothing".
	writeFlow(t, "sweep", "about: clear old runs\nsteps:\n  - id: one\n    run: true\n")

	out, err := captureStdout(t, func() error { return flowList(nil) })
	if err != nil {
		t.Fatalf("flow list failed: %v", err)
	}

	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			rows[fields[0]] = line
		}
	}

	triage, ok := rows["triage"]
	if !ok {
		t.Fatalf("flow list left out triage entirely:\n%s", out)
	}
	if !strings.Contains(triage, "sort the inbox") {
		t.Errorf("the triage row %q does not say what the flow is for", triage)
	}
	if !strings.Contains(triage, "which inbox") {
		t.Errorf("the triage row %q does not say what the caller has to supply", triage)
	}
	if !strings.Contains(triage, "2") {
		t.Errorf("the triage row %q does not carry its step count", triage)
	}

	sweep, ok := rows["sweep"]
	if !ok {
		t.Fatalf("flow list left out sweep entirely:\n%s", out)
	}
	if !strings.Contains(sweep, "-") {
		t.Errorf("the sweep row %q has no placeholder for an input it does not take", sweep)
	}
}

// A flow that will not parse is invisible everywhere else — it does not appear
// on the board, and a job pointing at it fails at its next fire. `flow list` is
// where that has to be said, and saying it must not cost the good flows their
// listing.
func TestFlowListNamesBrokenFlowsWithoutHidingTheGoodOnes(t *testing.T) {
	flowFilesIn(t)
	writeFlow(t, "good", "about: works\nsteps:\n  - id: one\n    run: true\n")
	writeFlow(t, "broken", "steps:\n  - id: one\n    agnet: typo\n")

	var out string
	stderr := captureStderr(t, func() {
		var err error
		out, err = captureStdout(t, func() error { return flowList(nil) })
		if err != nil {
			t.Errorf("flow list failed with a broken flow present: %v", err)
		}
	})

	if !strings.Contains(stderr, "broken") {
		t.Errorf("said %q on stderr, want the unparseable flow named", stderr)
	}
	if !strings.Contains(out, "good") {
		t.Errorf("listed %q, want the flows that do parse still listed", out)
	}
	if strings.Contains(out, "broken") {
		t.Errorf("listed %q, want the broken flow kept out of the table it cannot fill", out)
	}
}

// `flow show` exists so a flow can be read and pasted back. Anything other than
// the bytes on disk — a re-rendering, a normalised key order, a dropped comment
// — makes that paste a different flow.
func TestFlowShowPrintsTheFileVerbatim(t *testing.T) {
	flowFilesIn(t)
	body := "# why this exists\nabout: sort the inbox\n\nsteps:\n  - id: one\n    run: true\n"
	writeFlow(t, "triage", body)

	out, err := captureStdout(t, func() error { return flowShow([]string{"triage"}) })
	if err != nil {
		t.Fatalf("flow show failed: %v", err)
	}
	if out != body {
		t.Errorf("flow show printed\n%q\nwant the file byte for byte:\n%q", out, body)
	}
}

func TestFlowShowRefusesWhatItCannotShow(t *testing.T) {
	flowFilesIn(t)

	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"no id", nil, "usage"},
		{"unknown flow", []string{"missing"}, "no flow missing"},
		{"not an id", []string{"Not An Id"}, "not a flow id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error { return flowShow(tc.argv) })
			if err == nil {
				t.Fatalf("flow show %v succeeded and printed %q", tc.argv, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("flow show %v said %q, want it to mention %q", tc.argv, err, tc.want)
			}
		})
	}
}

// An agent has no $EDITOR, so `flow edit` with nothing to open is not a failure:
// the path is the useful half of the command, and printing it is what lets an
// agent edit the file with its own tools.
func TestFlowEditWithoutAnEditorPrintsThePath(t *testing.T) {
	dir := flowFilesIn(t)
	writeFlow(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")

	out, err := captureStdout(t, func() error { return flowEdit([]string{"triage"}) })
	if err != nil {
		t.Fatalf("flow edit with no editor failed: %v", err)
	}
	want := filepath.Join(flow.Dir(dir), "triage"+flow.Ext)
	if strings.TrimSpace(out) != want {
		t.Errorf("flow edit printed %q, want the path %q", strings.TrimSpace(out), want)
	}
}

// The flow somebody most needs to open is the one that no longer parses, so a
// broken file has to be editable. Refusing it is the state where the only way
// out is to know where bermuda keeps its flows.
func TestFlowEditOpensAFlowThatDoesNotParse(t *testing.T) {
	dir := flowFilesIn(t)
	writeFlow(t, "broken", "steps:\n  - id: one\n    agnet: typo\n")

	out, err := captureStdout(t, func() error { return flowEdit([]string{"broken"}) })
	if err != nil {
		t.Fatalf("flow edit refused the broken flow it exists to fix: %v", err)
	}
	want := filepath.Join(flow.Dir(dir), "broken"+flow.Ext)
	if strings.TrimSpace(out) != want {
		t.Errorf("flow edit printed %q, want the path %q to the broken file", strings.TrimSpace(out), want)
	}
}

// A flow that is not there is a different answer from one that is there and
// broken, and `flow edit` must not turn the first into an empty file or a
// success.
func TestFlowEditRefusesWhatIsNotThere(t *testing.T) {
	dir := flowFilesIn(t)

	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"no id", nil, "usage"},
		{"unknown flow", []string{"missing"}, "no flow missing"},
		{"not an id", []string{"Not An Id"}, "not a flow id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error { return flowEdit(tc.argv) })
			if err == nil {
				t.Fatalf("flow edit %v succeeded and printed %q", tc.argv, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("flow edit %v said %q, want it to mention %q", tc.argv, err, tc.want)
			}
		})
	}

	if entries, err := os.ReadDir(flow.Dir(dir)); err == nil && len(entries) > 0 {
		t.Errorf("flow edit created %d file(s) for flows that do not exist", len(entries))
	}
}

// The re-read on the way out is the whole reason `flow edit` is a command and
// not a printed path: a syntax error saved at 17:00 is otherwise first reported
// by the scheduler at 04:00, to nobody.
func TestFlowEditReportsASyntaxErrorSavedByTheEditor(t *testing.T) {
	flowFilesIn(t)
	writeFlow(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")
	t.Setenv("BERMUDA_EDITOR", editorWriting(t, "steps:\n  - id: one\n    agnet: typo\n"))

	if err := flowEdit([]string{"triage"}); err == nil {
		t.Fatal("flow edit reported success after the editor saved a flow that does not parse")
	}
}

// The ordinary case still has to succeed, or the check above would just be a
// command that always fails.
func TestFlowEditAcceptsAnEditThatStillParses(t *testing.T) {
	dir := flowFilesIn(t)
	writeFlow(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")
	edited := "about: sort the inbox\nsteps:\n  - id: one\n    run: true\n  - id: two\n    run: true\n"
	t.Setenv("BERMUDA_EDITOR", editorWriting(t, edited))

	if err := flowEdit([]string{"triage"}); err != nil {
		t.Fatalf("flow edit rejected an edit that parses: %v", err)
	}

	f, err := flow.Load(flow.Dir(dir), "triage")
	if err != nil {
		t.Fatalf("the edited flow no longer loads: %v", err)
	}
	if len(f.Steps) != 2 || f.About != "sort the inbox" {
		t.Errorf("the file on disk is %+v, want what the editor saved", f)
	}
}

// An editor that exits non-zero means the edit was abandoned, and reporting
// success would tell the caller their unsaved change is on disk.
func TestFlowEditFailsWhenTheEditorDoes(t *testing.T) {
	flowFilesIn(t)
	writeFlow(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")
	t.Setenv("BERMUDA_EDITOR", "false")

	if err := flowEdit([]string{"triage"}); err == nil {
		t.Fatal("flow edit reported success after the editor exited non-zero")
	}
}

// editorWriting returns an editor command that replaces the file it is given
// with body. It stands in for a person saving a change.
func editorWriting(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	content := "#!/bin/sh\ncat > \"$1\" <<'BERMUDA_EOF'\n" + body + "BERMUDA_EOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return shellQuote(script)
}
