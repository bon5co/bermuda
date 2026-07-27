package herdrcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeHerdr is a stand-in binary, not the real herdr: it records the arguments
// it was called with and replays a canned envelope. That is enough to test the
// two things this package actually owns — how a call is assembled, and how a
// reply is decoded — without a live server.
type fakeHerdr struct {
	t        *testing.T
	dir      string
	argsFile string
}

func newFake(t *testing.T, stdout, stderr string, exit int) *fakeHerdr {
	t.Helper()
	dir := t.TempDir()
	f := &fakeHerdr{t: t, dir: dir, argsFile: filepath.Join(dir, "args")}

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	outPath := write("stdout", stdout)
	errPath := write("stderr", stderr)

	script := "#!/bin/sh\n" +
		"printf '###\\n' >> " + f.argsFile + "\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + f.argsFile + "; done\n" +
		"cat " + outPath + "\n" +
		"cat " + errPath + " >&2\n" +
		"exit " + strconv.Itoa(exit) + "\n"
	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake herdr: %v", err)
	}
	return f
}

func (f *fakeHerdr) client() *Client { return &Client{Bin: filepath.Join(f.dir, "herdr")} }

// calls returns the argument list of every invocation, in order.
func (f *fakeHerdr) calls() [][]string {
	f.t.Helper()
	b, err := os.ReadFile(f.argsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		f.t.Fatalf("read args: %v", err)
	}
	var out [][]string
	var cur []string
	started := false
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "###" {
			if started {
				out = append(out, cur)
			}
			started, cur = true, nil
			continue
		}
		cur = append(cur, line)
	}
	if started {
		out = append(out, cur)
	}
	return out
}

func (f *fakeHerdr) lastCall() []string {
	f.t.Helper()
	calls := f.calls()
	if len(calls) == 0 {
		f.t.Fatal("herdr was never invoked")
	}
	return calls[len(calls)-1]
}

func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestNewPrefersHerdrBinPath(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/opt/herdr/bin/herdr")
	if got := New().Bin; got != "/opt/herdr/bin/herdr" {
		t.Fatalf("New().Bin = %q, want the HERDR_BIN_PATH value", got)
	}
	t.Setenv("HERDR_BIN_PATH", "")
	if got := New().Bin; got != "herdr" {
		t.Fatalf("New().Bin = %q, want %q when HERDR_BIN_PATH is unset", got, "herdr")
	}
}

func TestDecodeResultEnvelope(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"agents":[{"name":"ada","agent":"claude","agent_status":"working","pane_id":"p1","cwd":"/home/dev"}]}}`, "", 0)
	agents, err := f.client().AgentList(context.Background())
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	got := agents[0]
	if got.Name != "ada" || got.AgentStatus != StatusWorking || got.PaneID != "p1" || got.CWD != "/home/dev" {
		t.Fatalf("decoded agent %+v does not match the envelope", got)
	}
}

// herdr writes its error envelope to stderr with a nonzero exit status. A
// decoding slip here turns a structured, branchable error into an opaque one.
func TestErrorEnvelopeOnStderr(t *testing.T) {
	f := newFake(t, "", `{"id":"1","error":{"code":"timeout","message":"agent did not settle"}}`, 1)
	_, err := f.client().AgentList(context.Background())
	if err == nil {
		t.Fatal("AgentList succeeded despite an error envelope")
	}
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("error is %T (%v), want *Error", err, err)
	}
	if he.Code != "timeout" || he.Message != "agent did not settle" {
		t.Fatalf("decoded error %+v does not match the envelope", he)
	}
	if !Code(err, "timeout") {
		t.Error("Code(err, \"timeout\") = false, want true")
	}
	if Code(err, "agent_pane_busy") {
		t.Error("Code matched a code the envelope did not carry")
	}
}

func TestErrorEnvelopeOnStdoutWins(t *testing.T) {
	f := newFake(t, `{"id":"1","error":{"code":"not_found","message":"no such agent"}}`, "warning: ignore me\n", 1)
	err := f.client().AgentFocus(context.Background(), "ghost")
	if !Code(err, "not_found") {
		t.Fatalf("err = %v, want a not_found herdr error", err)
	}
}

// A crash with no envelope anywhere must surface as a plain error, not as a
// silent success.
func TestNonEnvelopeFailureIsAnError(t *testing.T) {
	f := newFake(t, "", "herdr: segmentation fault\n", 2)
	_, err := f.client().AgentList(context.Background())
	if err == nil {
		t.Fatal("AgentList succeeded on a non-envelope failure")
	}
	var he *Error
	if errors.As(err, &he) {
		t.Fatalf("garbage output decoded as a structured herdr error: %+v", he)
	}
	if !strings.Contains(err.Error(), "segmentation fault") {
		t.Errorf("error %q drops herdr's stderr, which is the only diagnostic left", err)
	}
}

func TestNonEnvelopeSuccessIsAnError(t *testing.T) {
	f := newFake(t, "not json at all\n", "", 0)
	_, err := f.client().AgentList(context.Background())
	if err == nil {
		t.Fatal("AgentList succeeded on undecodable stdout with exit 0")
	}
}

func TestCodeAndAsHerdrErrorOnOtherErrors(t *testing.T) {
	if Code(nil, "timeout") {
		t.Error("Code(nil, ...) = true")
	}
	if Code(errors.New("boom"), "timeout") {
		t.Error("Code matched a non-herdr error")
	}
	var he *Error
	if AsHerdrError(nil, &he) {
		t.Error("AsHerdrError(nil) = true")
	}
	if AsHerdrError(errors.New("boom"), &he) {
		t.Error("AsHerdrError matched a non-herdr error")
	}
}

func TestArgumentAssembly(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		call   func(*Client) error
		want   []string // args that must be present, in order
		checks func(*testing.T, []string)
	}{
		{
			name:   "workspace create carries label, cwd and env",
			stdout: `{"id":"1","result":{"workspace":{"workspace_id":"w1"},"root_pane":{"pane_id":"p1"}}}`,
			call: func(c *Client) error {
				_, _, err := c.WorkspaceCreate(context.Background(), "run-7", "/tmp/run", map[string]string{"BERMUDA_RUN_DIR": "/tmp/run"})
				return err
			},
			want: []string{"workspace", "create"},
			checks: func(t *testing.T, args []string) {
				if !hasFlagValue(args, "--label", "run-7") {
					t.Error("missing --label run-7")
				}
				if !hasFlagValue(args, "--cwd", "/tmp/run") {
					t.Error("missing --cwd")
				}
				if !hasFlagValue(args, "--env", "BERMUDA_RUN_DIR=/tmp/run") {
					t.Error("env entry not passed as KEY=VALUE")
				}
				if !hasArg(args, "--no-focus") {
					t.Error("workspace create must not steal focus")
				}
			},
		},
		{
			name:   "workspace create omits an empty cwd",
			stdout: `{"id":"1","result":{"workspace":{"workspace_id":"w1"},"root_pane":{"pane_id":"p1"}}}`,
			call: func(c *Client) error {
				_, _, err := c.WorkspaceCreate(context.Background(), "run-7", "", nil)
				return err
			},
			want: []string{"workspace", "create"},
			checks: func(t *testing.T, args []string) {
				if hasArg(args, "--cwd") {
					t.Error("--cwd passed with no value")
				}
				if hasArg(args, "--env") {
					t.Error("--env passed with no entries")
				}
			},
		},
		{
			name:   "tab create omits an empty label",
			stdout: `{"id":"1","result":{"root_pane":{"pane_id":"p2"}}}`,
			call: func(c *Client) error {
				_, err := c.TabCreate(context.Background(), "w1", "", "", nil)
				return err
			},
			want: []string{"tab", "create"},
			checks: func(t *testing.T, args []string) {
				if !hasFlagValue(args, "--workspace", "w1") {
					t.Error("missing --workspace")
				}
				if hasArg(args, "--label") {
					t.Error("--label passed with no value")
				}
			},
		},
		{
			name:   "agent start sends the timeout in milliseconds and agent args after --",
			stdout: `{"id":"1","result":{"agent":{"name":"job"}}}`,
			call: func(c *Client) error {
				_, err := c.AgentStart(context.Background(), "job", "claude", "p1", 90*time.Second, "--model", "opus")
				return err
			},
			want: []string{"agent", "start", "job"},
			checks: func(t *testing.T, args []string) {
				if !hasFlagValue(args, "--kind", "claude") {
					t.Error("missing --kind")
				}
				if !hasFlagValue(args, "--pane", "p1") {
					t.Error("missing --pane")
				}
				if !hasFlagValue(args, "--timeout", "90000") {
					t.Errorf("timeout not sent in milliseconds: %v", args)
				}
				sep := -1
				for i, a := range args {
					if a == "--" {
						sep = i
					}
				}
				if sep < 0 || sep != len(args)-3 {
					t.Fatalf("agent args not passed after a trailing --: %v", args)
				}
				if args[sep+1] != "--model" || args[sep+2] != "opus" {
					t.Errorf("agent args mangled: %v", args[sep+1:])
				}
			},
		},
		{
			name:   "agent start omits a zero timeout and an empty arg list",
			stdout: `{"id":"1","result":{"agent":{"name":"job"}}}`,
			call: func(c *Client) error {
				_, err := c.AgentStart(context.Background(), "job", "claude", "p1", 0)
				return err
			},
			want: []string{"agent", "start", "job"},
			checks: func(t *testing.T, args []string) {
				if hasArg(args, "--timeout") {
					t.Error("--timeout passed for a zero timeout")
				}
				if hasArg(args, "--") {
					t.Error("trailing -- passed with no agent args")
				}
			},
		},
		{
			name:   "agent notify does not wait",
			stdout: `{"id":"1","result":{}}`,
			call: func(c *Client) error {
				return c.AgentNotify(context.Background(), "@ada", "ping")
			},
			want: []string{"agent", "prompt", "@ada", "ping"},
			checks: func(t *testing.T, args []string) {
				if hasArg(args, "--wait") {
					t.Error("a notify must not block on the agent's turn")
				}
			},
		},
		{
			name:   "agent wait passes every until state",
			stdout: `{"id":"1","result":{}}`,
			call: func(c *Client) error {
				return c.AgentWait(context.Background(), "job", time.Minute, StatusIdle, StatusDone)
			},
			want: []string{"agent", "wait", "job"},
			checks: func(t *testing.T, args []string) {
				if !hasFlagValue(args, "--until", "idle") || !hasFlagValue(args, "--until", "done") {
					t.Errorf("both until states must be sent: %v", args)
				}
				if !hasFlagValue(args, "--timeout", "60000") {
					t.Error("timeout not sent in milliseconds")
				}
			},
		},
		{
			name:   "clear name is a rename with --clear",
			stdout: `{"id":"1","result":{}}`,
			call: func(c *Client) error {
				return c.AgentClearName(context.Background(), "job")
			},
			want: []string{"agent", "rename", "job", "--clear"},
		},
		{
			name:   "pane list without a workspace is unfiltered",
			stdout: `{"id":"1","result":{"panes":[]}}`,
			call: func(c *Client) error {
				_, err := c.PaneList(context.Background(), "")
				return err
			},
			want: []string{"pane", "list"},
			checks: func(t *testing.T, args []string) {
				if hasArg(args, "--workspace") {
					t.Error("--workspace passed with no value")
				}
			},
		},
		{
			name:   "report metadata is scoped to a source",
			stdout: `{"id":"1","result":{}}`,
			call: func(c *Client) error {
				return c.ReportPaneMetadata(context.Background(), "p1", "bermuda", PaneMetadata{
					DisplayAgent: "bermuda",
					Title:        "run 7",
					StateLabels:  map[string]string{"working": "running"},
					Tokens:       map[string]string{"job": "unit-tests"},
				})
			},
			want: []string{"pane", "report-metadata", "p1"},
			checks: func(t *testing.T, args []string) {
				if !hasFlagValue(args, "--source", "bermuda") {
					t.Error("metadata must be scoped to bermuda's source")
				}
				if !hasFlagValue(args, "--state-label", "working=running") {
					t.Error("state label not sent as status=text")
				}
				if !hasFlagValue(args, "--token", "job=unit-tests") {
					t.Error("token not sent as name=value")
				}
			},
		},
		{
			name:   "report metadata omits empty display fields",
			stdout: `{"id":"1","result":{}}`,
			call: func(c *Client) error {
				return c.ReportPaneMetadata(context.Background(), "p1", "bermuda", PaneMetadata{})
			},
			want: []string{"pane", "report-metadata", "p1"},
			checks: func(t *testing.T, args []string) {
				for _, flag := range []string{"--display-agent", "--title", "--state-label", "--token"} {
					if hasArg(args, flag) {
						t.Errorf("%s passed with no value", flag)
					}
				}
			},
		},
		{
			name:   "plugin pane open carries placement and direction",
			stdout: `{"id":"1","result":{}}`,
			call: func(c *Client) error {
				return c.OpenPluginPane(context.Background(), PluginPane{
					Plugin: "bermuda", Entrypoint: "board",
					Placement: "bottom", Direction: "horizontal", Workspace: "w1",
				})
			},
			want: []string{"plugin", "pane", "open"},
			checks: func(t *testing.T, args []string) {
				if !hasFlagValue(args, "--plugin", "bermuda") || !hasFlagValue(args, "--entrypoint", "board") {
					t.Error("plugin or entrypoint missing")
				}
				if !hasFlagValue(args, "--placement", "bottom") || !hasFlagValue(args, "--direction", "horizontal") {
					t.Error("placement or direction missing")
				}
				if !hasFlagValue(args, "--workspace", "w1") {
					t.Error("workspace missing")
				}
			},
		},
		{
			// Herdr resolves a split's workspace from the pane it divides, and
			// ignores the target pane when it is handed both. Sending only the
			// target is what makes the wide board open at all.
			name:   "a targeted plugin pane drops the workspace",
			stdout: `{"id":"1","result":{}}`,
			call: func(c *Client) error {
				return c.OpenPluginPane(context.Background(), PluginPane{
					Plugin: "bermuda", Entrypoint: "board",
					Placement: "split", Direction: "down",
					Workspace: "w1", TargetPane: "w1:p3",
				})
			},
			want: []string{"plugin", "pane", "open"},
			checks: func(t *testing.T, args []string) {
				if !hasFlagValue(args, "--target-pane", "w1:p3") {
					t.Error("target pane missing")
				}
				if hasArg(args, "--workspace") {
					t.Error("workspace passed alongside a target pane")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, tt.stdout, "", 0)
			if err := tt.call(f.client()); err != nil {
				t.Fatalf("call: %v", err)
			}
			args := f.lastCall()
			if len(args) < len(tt.want) {
				t.Fatalf("args %v are shorter than the expected prefix %v", args, tt.want)
			}
			for i, w := range tt.want {
				if args[i] != w {
					t.Fatalf("args %v, want prefix %v", args, tt.want)
				}
			}
			if tt.checks != nil {
				tt.checks(t, args)
			}
		})
	}
}

// A timeout parks a run rather than failing it, so the caller still needs the
// agent's last observed status alongside the error.
func TestAgentPromptTimeoutStillReportsStatus(t *testing.T) {
	f := newFake(t, "", `{"id":"1","error":{"code":"timeout","message":"no settle"}}`, 1)

	status, err := f.client().AgentPrompt(context.Background(), "job", "go", time.Second)
	if !Code(err, "timeout") {
		t.Fatalf("err = %v, want a timeout herdr error", err)
	}
	// AgentGet also fails against this stub, so the status falls back to unknown
	// — the error must still be herdr's timeout, not the lookup failure.
	if status != StatusUnknown {
		t.Fatalf("status = %q, want %q when the follow-up lookup fails", status, StatusUnknown)
	}
	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want a prompt followed by a status lookup: %v", len(calls), calls)
	}
	if !hasArg(calls[0], "--wait") {
		t.Error("prompt did not wait")
	}
	if !hasFlagValue(calls[0], "--timeout", "1000") {
		t.Error("prompt timeout not sent in milliseconds")
	}
	if calls[1][0] != "agent" || calls[1][1] != "get" {
		t.Errorf("follow-up call was %v, want agent get", calls[1])
	}
}

func TestAgentPromptReturnsSettledStatus(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"agent":{"name":"job","agent_status":"done"}}}`, "", 0)
	status, err := f.client().AgentPrompt(context.Background(), "job", "go", 0)
	if err != nil {
		t.Fatalf("AgentPrompt: %v", err)
	}
	if status != StatusDone {
		t.Fatalf("status = %q, want %q", status, StatusDone)
	}
	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want prompt then get", len(calls))
	}
	if hasArg(calls[0], "--timeout") {
		t.Error("--timeout passed for a zero timeout")
	}
}

// agent read is a terminal scrape, not an envelope: it must come back verbatim.
func TestAgentReadReturnsRawOutput(t *testing.T) {
	raw := "\x1b[1mhello\x1b[0m\nworld\n"
	f := newFake(t, raw, "", 0)
	got, err := f.client().AgentRead(context.Background(), "job")
	if err != nil {
		t.Fatalf("AgentRead: %v", err)
	}
	if got != raw {
		t.Fatalf("AgentRead returned %q, want the bytes verbatim", got)
	}
}

func TestAgentReadFailurePropagates(t *testing.T) {
	f := newFake(t, "", "no such agent\n", 1)
	if _, err := f.client().AgentRead(context.Background(), "ghost"); err == nil {
		t.Fatal("AgentRead succeeded on a failing command")
	}
}

func TestWaitShellPromptSucceedsWhenPaneIsIdle(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"process_info":{"pane_id":"p1","shell_pid":42,"foreground_process_group_id":42}}}`, "", 0)
	if err := f.client().WaitShellPrompt(context.Background(), "p1", time.Second); err != nil {
		t.Fatalf("WaitShellPrompt: %v", err)
	}
}

// Starting an agent before the shell is up fails with agent_pane_busy, so a
// pane whose foreground group is not the shell must never be reported ready.
func TestWaitShellPromptTimesOutWhilePaneIsBusy(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"process_info":{"pane_id":"p1","shell_pid":42,"foreground_process_group_id":99}}}`, "", 0)
	err := f.client().WaitShellPrompt(context.Background(), "p1", 10*time.Millisecond)
	if err == nil {
		t.Fatal("WaitShellPrompt reported ready while another process held the foreground")
	}
	if !strings.Contains(err.Error(), "p1") {
		t.Errorf("error %q does not name the pane", err)
	}
}

func TestWaitShellPromptStopsOnCancelledContext(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{"process_info":{"pane_id":"p1","shell_pid":42,"foreground_process_group_id":99}}}`, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := f.client().WaitShellPrompt(ctx, "p1", time.Hour)
	if err == nil {
		t.Fatal("WaitShellPrompt returned nil for a cancelled context")
	}
}
