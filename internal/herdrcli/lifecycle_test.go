package herdrcli

import (
	"context"
	"testing"
)

// The calls in this file are the ones that take something away or rename it:
// closing a tab or a workspace, relabelling one, naming an agent. Their whole
// content is the argument list, and an id in the wrong position closes a
// different window than the one bermuda meant to close — which nothing
// downstream can detect, because herdr answers with an empty success envelope
// either way.
func TestLifecycleArgumentAssembly(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
		want []string
	}{
		{
			name: "tab close names the tab positionally",
			call: func(c *Client) error { return c.TabClose(context.Background(), "t1") },
			want: []string{"tab", "close", "t1"},
		},
		{
			name: "tab rename puts the id before the new label",
			call: func(c *Client) error { return c.TabRename(context.Background(), "t1", "run 7") },
			want: []string{"tab", "rename", "t1", "run 7"},
		},
		{
			name: "workspace close names the workspace positionally",
			call: func(c *Client) error { return c.WorkspaceClose(context.Background(), "w1") },
			want: []string{"workspace", "close", "w1"},
		},
		{
			name: "workspace rename puts the id before the new label",
			call: func(c *Client) error { return c.WorkspaceRename(context.Background(), "w1", "flow: nightly") },
			want: []string{"workspace", "rename", "w1", "flow: nightly"},
		},
		{
			// Distinct from AgentClearName, which is the same subcommand with
			// --clear: a name argument that went missing would silently clear
			// the name instead of setting it.
			name: "agent rename puts the target before the name",
			call: func(c *Client) error { return c.AgentRename(context.Background(), "@ada", "unit-tests") },
			want: []string{"agent", "rename", "@ada", "unit-tests"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, `{"id":"1","result":{}}`, "", 0)
			if err := tc.call(f.client()); err != nil {
				t.Fatalf("call: %v", err)
			}
			args := f.lastCall()
			if len(args) != len(tc.want) {
				t.Fatalf("args = %v, want %v", args, tc.want)
			}
			for i := range tc.want {
				if args[i] != tc.want[i] {
					t.Fatalf("args = %v, want %v", args, tc.want)
				}
			}
		})
	}
}

// A close or a rename that herdr refused must reach the caller as an error.
// These calls decode nothing, so the error envelope is the only thing standing
// between "the workspace is gone" and "bermuda believes the workspace is gone".
func TestLifecycleCallsPropagateAnErrorEnvelope(t *testing.T) {
	calls := map[string]func(*Client) error{
		"tab close":        func(c *Client) error { return c.TabClose(context.Background(), "t1") },
		"tab rename":       func(c *Client) error { return c.TabRename(context.Background(), "t1", "x") },
		"workspace close":  func(c *Client) error { return c.WorkspaceClose(context.Background(), "w1") },
		"workspace rename": func(c *Client) error { return c.WorkspaceRename(context.Background(), "w1", "x") },
		"agent rename":     func(c *Client) error { return c.AgentRename(context.Background(), "@ada", "x") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, "", `{"id":"1","error":{"code":"not_found","message":"no such thing"}}`, 1)
			err := call(f.client())
			if err == nil {
				t.Fatal("a refused call reported success")
			}
			if !Code(err, "not_found") {
				t.Fatalf("error lost its code: %v", err)
			}
		})
	}
}

func TestWorkspaceListDecodesEveryWorkspace(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"workspaces":[
		{"workspace_id":"w1","label":"bermuda","active_tab_id":"t1"},
		{"workspace_id":"w2","label":"dotfiles","active_tab_id":"t9"}]}}`, "", 0)

	got, err := f.client().WorkspaceList(context.Background())
	if err != nil {
		t.Fatalf("WorkspaceList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d workspaces, want 2", len(got))
	}
	// Field-by-field, because a wrong json tag would still produce two
	// workspaces — with the labels bermuda matches its own space by missing.
	if got[0].WorkspaceID != "w1" || got[0].Label != "bermuda" || got[0].ActiveTabID != "t1" {
		t.Errorf("first workspace decoded wrong: %+v", got[0])
	}
	if got[1].WorkspaceID != "w2" || got[1].Label != "dotfiles" {
		t.Errorf("second workspace decoded wrong: %+v", got[1])
	}
	if args := f.lastCall(); len(args) != 2 || args[0] != "workspace" || args[1] != "list" {
		t.Errorf("args = %v, want [workspace list]", args)
	}
}

// An empty list of workspaces is a real answer, not a failure: bermuda reads
// it as "nothing of mine is open" and makes a new space.
func TestWorkspaceListAcceptsAnEmptyResult(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"workspaces":[]}}`, "", 0)
	got, err := f.client().WorkspaceList(context.Background())
	if err != nil {
		t.Fatalf("WorkspaceList: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d workspaces, want 0", len(got))
	}
}

func TestWorkspaceGetDecodesTheWorkspaceItAskedFor(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"workspace":{"workspace_id":"w1","label":"bermuda","active_tab_id":"t4"}}}`, "", 0)

	ws, err := f.client().WorkspaceGet(context.Background(), "w1")
	if err != nil {
		t.Fatalf("WorkspaceGet: %v", err)
	}
	if ws.WorkspaceID != "w1" || ws.Label != "bermuda" || ws.ActiveTabID != "t4" {
		t.Errorf("workspace decoded wrong: %+v", ws)
	}
	if args := f.lastCall(); len(args) != 3 || args[2] != "w1" {
		t.Errorf("args = %v, want [workspace get w1]", args)
	}
}

// The half of WorkspaceGet bermuda actually depends on: a workspace id
// recorded in an earlier session is how it recognises the space it owns, and
// "no such workspace" is how it learns the space is gone and must be made
// again. Swallowing that error would have bermuda address a window that no
// longer exists.
func TestWorkspaceGetReportsAWorkspaceThatIsGone(t *testing.T) {
	f := newFake(t, "", `{"id":"1","error":{"code":"workspace_not_found","message":"no such workspace"}}`, 1)

	ws, err := f.client().WorkspaceGet(context.Background(), "w-old")
	if err == nil {
		t.Fatal("a missing workspace was reported as found")
	}
	if ws != nil {
		t.Errorf("workspace returned alongside an error: %+v", ws)
	}
	if !Code(err, "workspace_not_found") {
		t.Fatalf("caller cannot branch on the code: %v", err)
	}
}

// A reply that carries no result at all is not an empty workspace. Returning a
// zero Workspace here would hand back an empty id that matches nothing.
func TestWorkspaceGetRefusesAReplyWithNoResult(t *testing.T) {
	f := newFake(t, `{"id":"1"}`, "", 0)
	if _, err := f.client().WorkspaceGet(context.Background(), "w1"); err == nil {
		t.Fatal("a reply with no result was accepted")
	}
}

func TestIsBoardAgentMatchesWhateverSpellingIsOnScreen(t *testing.T) {
	tests := []struct {
		label string
		want  bool
	}{
		{BoardAgent, true},
		{"Bermuda", true},
		{"bermuda", true},
		{"BERMUDA", true},
		{"bermuda-board", false},
		{"claude", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			if got := IsBoardAgent(tc.label); got != tc.want {
				t.Errorf("IsBoardAgent(%q) = %v, want %v", tc.label, got, tc.want)
			}
		})
	}
}

// A list bermuda could not get is not an empty list. Returning the zero slice
// with a nil error would read as "no workspaces of mine are open" and start a
// second one beside the one already on screen.
func TestWorkspaceListReportsAFailureRatherThanNothing(t *testing.T) {
	f := newFake(t, "", `{"id":"1","error":{"code":"server_unreachable","message":"no herdr"}}`, 1)

	got, err := f.client().WorkspaceList(context.Background())
	if err == nil {
		t.Fatal("a failed list was reported as an empty one")
	}
	if got != nil {
		t.Errorf("workspaces returned alongside an error: %+v", got)
	}
}

// TabCreate is how a flow step gets its own tab, and the env entries are how
// BERMUDA_RUN_DIR reaches the agent in it — an agent started without that
// variable writes its result where nothing looks for it.
func TestTabCreateCarriesLabelCwdAndEnv(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"root_pane":{"pane_id":"p2","tab_id":"t2"}}}`, "", 0)

	pane, err := f.client().TabCreate(context.Background(), "w1", "step 2", "/tmp/run",
		map[string]string{"BERMUDA_RUN_DIR": "/tmp/run"})
	if err != nil {
		t.Fatalf("TabCreate: %v", err)
	}
	if pane.PaneID != "p2" || pane.TabID != "t2" {
		t.Errorf("root pane decoded wrong: %+v", pane)
	}
	args := f.lastCall()
	if !hasFlagValue(args, "--workspace", "w1") {
		t.Error("missing --workspace")
	}
	if !hasFlagValue(args, "--label", "step 2") {
		t.Error("missing --label")
	}
	if !hasFlagValue(args, "--cwd", "/tmp/run") {
		t.Error("missing --cwd")
	}
	if !hasFlagValue(args, "--env", "BERMUDA_RUN_DIR=/tmp/run") {
		t.Error("env entry not passed as KEY=VALUE")
	}
	if !hasArg(args, "--no-focus") {
		t.Error("a tab created for a step must not steal focus")
	}
}
