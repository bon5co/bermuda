package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v2/internal/statefs"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// Every caller that reads or writes a flow derives the directory from the state
// directory through Dir, so the two must agree on one location; a second
// spelling anywhere would make `flow new` write where `flow list` never looks.
func TestFlowsLiveInOnePlaceUnderTheStateDirectory(t *testing.T) {
	state := t.TempDir()
	got := Dir(state)
	if want := filepath.Join(state, "flows"); got != want {
		t.Errorf("Dir(%q) = %q, want %q", state, got, want)
	}
}

// A saved flow has to come back through Load unchanged, since that is the whole
// round trip `flow new` then `flow run` depends on.
func TestASavedFlowIsReadBackAsItWasWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flows")
	f := Flow{
		ID:    "release-check",
		About: "check a release",
		Input: "a version",
		Steps: []store.Step{
			{ID: "assess", Agent: "Look at {{input}}."},
			{ID: "verify", Run: "go test ./..."},
		},
	}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, "release-check")
	if err != nil {
		t.Fatal(err)
	}
	if got.About != f.About || got.Input != f.Input {
		t.Errorf("about/input did not survive the round trip: %+v", got)
	}
	if len(got.Steps) != 2 || got.Steps[0].Agent != f.Steps[0].Agent || got.Steps[1].Run != f.Steps[1].Run {
		t.Errorf("steps did not survive the round trip: %+v", got.Steps)
	}
	if got.Path == "" {
		t.Error("a loaded flow should know the file it came from")
	}
}

// Save creates the directory it needs, so a first `flow new` on a fresh machine
// works rather than reporting a missing path the user never made.
func TestSaveCreatesTheFlowDirectoryOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flows")
	f := Flow{ID: "triage", Steps: []store.Step{{ID: "one", Run: "true"}}}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != statefs.Dir {
		t.Errorf("flow directory mode = %o, want %o", got, statefs.Dir)
	}
	fi, err := os.Stat(filepath.Join(dir, "triage"+Ext))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != statefs.File {
		t.Errorf("flow file mode = %o, want %o", got, statefs.File)
	}
}

// A flow id is a filename, so an id the loader would refuse must never reach
// the disk: writing one would produce a file that can be listed and never read.
func TestSaveRefusesAnIdThatIsNotOne(t *testing.T) {
	for _, id := range []string{"", "Triage", "two words", "trailing-", "a--b", "../escape"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			if err := Save(dir, Flow{ID: id, Steps: []store.Step{{ID: "one", Run: "true"}}}); err == nil {
				t.Fatalf("saving a flow named %q was allowed", id)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("a refused save still wrote %d file(s)", len(entries))
			}
		})
	}
}

// Removing a flow takes the file with it, so the id is free again and the flow
// stops appearing in a listing.
func TestRemoveDeletesTheFlowFile(t *testing.T) {
	dir := t.TempDir()
	f := Flow{ID: "triage", Steps: []store.Step{{ID: "one", Run: "true"}}}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "triage"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "triage"+Ext)); !os.IsNotExist(err) {
		t.Errorf("flow file still present after Remove: %v", err)
	}
	if _, err := Load(dir, "triage"); err == nil {
		t.Error("a removed flow can still be loaded")
	}
	// The id is free again.
	if err := Save(dir, f); err != nil {
		t.Errorf("saving after a remove was refused: %v", err)
	}
}

// Removing something that is not there is reported, not swallowed: a silent
// success tells the caller a flow was deleted when the one they meant, probably
// misspelled, is still on disk and still scheduled.
func TestRemovingAFlowThatIsNotThereSaysSo(t *testing.T) {
	dir := t.TempDir()
	err := Remove(dir, "ghost")
	if err == nil {
		t.Fatal("removing a missing flow reported success")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the flow: %v", err)
	}
}

// Remove validates the id before touching the filesystem, for the same reason
// Save does: an id that is not one cannot name a flow file.
func TestRemoveRefusesAnIdThatIsNotOne(t *testing.T) {
	if err := Remove(t.TempDir(), "../etc"); err == nil {
		t.Fatal("removing by a bogus id was allowed")
	}
}

// NeedsInput drives whether a run is asked for input, so it must report on what
// the prompts actually reference rather than on what the file declares.
func TestNeedsInputReportsWhatThePromptsReference(t *testing.T) {
	cases := []struct {
		name string
		f    Flow
		want bool
	}{
		{
			name: "an agent step that uses the input",
			f:    Flow{ID: "a", Input: "a report", Steps: []store.Step{{ID: "one", Agent: "Assess {{input}}."}}},
			want: true,
		},
		{
			name: "a later step that uses the input",
			f: Flow{ID: "b", Input: "a report", Steps: []store.Step{
				{ID: "one", Agent: "Start."},
				{ID: "two", Agent: "Now do {{input}}."},
			}},
			want: true,
		},
		{
			name: "declared but never referenced",
			f:    Flow{ID: "c", Input: "a report", Steps: []store.Step{{ID: "one", Agent: "Assess whatever."}}},
			want: false,
		},
		{
			name: "only the previous result is referenced",
			f: Flow{ID: "d", Steps: []store.Step{
				{ID: "one", Agent: "Start."},
				{ID: "two", Agent: "{{previous}} — continue."},
			}},
			want: false,
		},
		{
			name: "a run step cannot be seen into",
			f:    Flow{ID: "e", Input: "a report", Steps: []store.Step{{ID: "one", Run: "echo $BERMUDA_INPUT"}}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsInput(tc.f); got != tc.want {
				t.Errorf("NeedsInput() = %v, want %v", got, tc.want)
			}
		})
	}
}
