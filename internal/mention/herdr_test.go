package mention

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bon5co/bermuda/internal/herdrcli"
)

// fakeHerdr writes a herdr stand-in that answers `agent list` with the given
// JSON and every other call with an empty result.
func fakeHerdr(t *testing.T, agentList string) *herdrcli.Client {
	t.Helper()
	dir := t.TempDir()
	body := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(body, []byte(agentList), 0o644); err != nil {
		t.Fatalf("write agent list: %v", err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"list\" ]; then cat " + body + "; exit 0; fi\n" +
		"printf '{\"id\":\"1\",\"result\":{}}\\n'\n"
	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake herdr: %v", err)
	}
	return &herdrcli.Client{Bin: bin}
}

// The board reports its own pane as an agent so bermuda has a row in Herdr's
// sidebar. That row must not be something a message can be delivered into: the
// board is a TUI, a delivered mention arrives as keystrokes, and one of the
// board's single-key actions runs the selected job.
//
// It would be hit by accident, not by intent. The board sits in a directory
// called bermuda and an unnamed agent answers to its directory's basename, so
// `@bermuda` finds it and `@all` finds it every time.
func TestLiveSkipsTheBoardsOwnRow(t *testing.T) {
	list := `{"id":"1","result":{"agents":[
		{"agent":"` + herdrcli.BoardAgent + `","pane_id":"wE:p4","cwd":"/home/x/Projects/bermuda"},
		{"agent":"claude","pane_id":"w1:p2","cwd":"/home/x/Projects/bermuda","name":"reviewer"}
	]}}`

	live, err := FromHerdr(fakeHerdr(t, list)).Live(context.Background())
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("live = %d agents, want only the real one: %+v", len(live), live)
	}
	if live[0].Target != "w1:p2" {
		t.Errorf("kept %q, want the claude agent w1:p2", live[0].Target)
	}
}

// A board still running a previous build holds its pane under whatever the
// label was spelled then. The skip has to survive the upgrade, because a board
// on screen is exactly the board a mention would be typed into.
func TestLiveSkipsTheBoardWhateverTheSpelling(t *testing.T) {
	list := `{"id":"1","result":{"agents":[
		{"agent":"bermuda","pane_id":"wE:p4","cwd":"/home/x/Projects/bermuda"},
		{"agent":"Bermuda","pane_id":"wE:p9","cwd":"/home/x/Projects/bermuda"}
	]}}`

	live, err := FromHerdr(fakeHerdr(t, list)).Live(context.Background())
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("live = %+v, want no board reachable under either spelling", live)
	}
}

// Belt and braces on the two mentions that would have reached it.
func TestBoardIsUnreachableByNameAndByAll(t *testing.T) {
	// The board is given a workspace so that @all genuinely resolves against one
	// here: an @all with no space to scope to is refused before it ever looks at
	// the agent list, which would pass this test without testing anything.
	list := `{"id":"1","result":{"agents":[
		{"agent":"` + herdrcli.BoardAgent + `","pane_id":"wE:p4","workspace_id":"wE","cwd":"/home/x/Projects/bermuda"}
	]}}`

	live, err := FromHerdr(fakeHerdr(t, list)).Live(context.Background())
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	for _, body := range []string{"@bermuda ping", "@all ping"} {
		r := Resolve(Message{Body: body, Workspace: "wE"}, live, Self{Target: "w9:p9"})
		if len(r.Delivered) != 0 {
			t.Errorf("%q reached %d agents, want none", body, len(r.Delivered))
		}
	}
}
