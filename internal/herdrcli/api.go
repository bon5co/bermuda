package herdrcli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AgentStatus mirrors herdr's agent lifecycle states.
type AgentStatus string

const (
	StatusIdle    AgentStatus = "idle"
	StatusWorking AgentStatus = "working"
	StatusBlocked AgentStatus = "blocked"
	StatusDone    AgentStatus = "done"
	StatusUnknown AgentStatus = "unknown"
)

// Pane is a herdr pane, as reported inside workspace/agent payloads.
type Pane struct {
	PaneID      string      `json:"pane_id"`
	TabID       string      `json:"tab_id"`
	WorkspaceID string      `json:"workspace_id"`
	AgentStatus AgentStatus `json:"agent_status"`
	CWD         string      `json:"cwd"`
	// Label is what a human named this pane, absent on most of them. It is the
	// name an agent is called on screen, which is not the name herdr filed it
	// under.
	Label string `json:"label"`
}

// Workspace is a herdr workspace.
type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	ActiveTabID string `json:"active_tab_id"`
}

// Agent is an agent attached to a pane.
type Agent struct {
	Name             string      `json:"name"`
	Agent            string      `json:"agent"`
	AgentStatus      AgentStatus `json:"agent_status"`
	PaneID           string      `json:"pane_id"`
	TabID            string      `json:"tab_id"`
	WorkspaceID      string      `json:"workspace_id"`
	InteractiveReady bool        `json:"interactive_ready"`
	TerminalTitle    string      `json:"terminal_title_stripped"`
	// CWD is where the agent is working. An agent started outside bermuda
	// usually has no name, so this is often the only thing that identifies it
	// as "the one on dotfiles".
	CWD string `json:"cwd"`
}

// AgentList lists every agent herdr currently knows about.
func (c *Client) AgentList(ctx context.Context) ([]Agent, error) {
	var out struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.run(ctx, &out, "agent", "list"); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

// AgentNotify submits a prompt and does not wait for the agent to answer.
//
// It is AgentPrompt without --wait, and the difference is the whole point.
// AgentPrompt is how bermuda runs a job: it hands over the work and blocks
// until the turn settles. A mention is a nudge — the sender is a `bermuda
// thread post` that has already written its message and has nothing to do with
// the reply — so waiting here would hold one agent's command open for the
// length of another agent's turn, and on the board it would hold the tick.
func (c *Client) AgentNotify(ctx context.Context, target, text string) error {
	return c.run(ctx, nil, "agent", "prompt", target, text)
}

// WorkspaceCreate creates a workspace. env entries are injected into the
// launched shell, which is how a run's BERMUDA_RUN_DIR reaches the agent.
func (c *Client) WorkspaceCreate(ctx context.Context, label, cwd string, env map[string]string) (*Workspace, *Pane, error) {
	args := []string{"workspace", "create", "--label", label, "--no-focus"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}
	var out struct {
		Workspace Workspace `json:"workspace"`
		RootPane  Pane      `json:"root_pane"`
	}
	if err := c.run(ctx, &out, args...); err != nil {
		return nil, nil, err
	}
	return &out.Workspace, &out.RootPane, nil
}

// TabCreate creates a tab in a workspace and returns its root pane.
func (c *Client) TabCreate(ctx context.Context, workspaceID, label, cwd string, env map[string]string) (*Pane, error) {
	args := []string{"tab", "create", "--workspace", workspaceID, "--no-focus"}
	if label != "" {
		args = append(args, "--label", label)
	}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}
	var out struct {
		RootPane Pane `json:"root_pane"`
	}
	if err := c.run(ctx, &out, args...); err != nil {
		return nil, err
	}
	return &out.RootPane, nil
}

// TabClose closes a tab.
func (c *Client) TabClose(ctx context.Context, tabID string) error {
	return c.run(ctx, nil, "tab", "close", tabID)
}

// WorkspaceList lists workspaces.
func (c *Client) WorkspaceList(ctx context.Context) ([]Workspace, error) {
	var out struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := c.run(ctx, &out, "workspace", "list"); err != nil {
		return nil, err
	}
	return out.Workspaces, nil
}

// ProcessInfo describes what is running in a pane.
type ProcessInfo struct {
	PaneID                  string `json:"pane_id"`
	ShellPID                int    `json:"shell_pid"`
	ForegroundProcessGroups int    `json:"foreground_process_group_id"`
}

// PaneProcessInfo returns process information for a pane.
func (c *Client) PaneProcessInfo(ctx context.Context, paneID string) (*ProcessInfo, error) {
	var out struct {
		ProcessInfo ProcessInfo `json:"process_info"`
	}
	if err := c.run(ctx, &out, "pane", "process-info", "--pane", paneID); err != nil {
		return nil, err
	}
	return &out.ProcessInfo, nil
}

// WaitShellPrompt blocks until a freshly created pane is sitting at its
// interactive shell prompt, which is what "agent start" requires.
//
// A newly created tab reports a pane before its shell has finished starting,
// and starting an agent too early fails with agent_pane_busy. The pane is idle
// when the foreground process group is the shell itself.
func (c *Client) WaitShellPrompt(ctx context.Context, paneID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		info, err := c.PaneProcessInfo(ctx, paneID)
		if err == nil && info.ShellPID != 0 && info.ForegroundProcessGroups == info.ShellPID {
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("pane %s not at shell prompt: %w", paneID, last)
			}
			return fmt.Errorf("pane %s not at shell prompt after %s", paneID, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// AgentStart starts an interactive agent in a pane that is at its shell prompt.
// agentArgs are passed through to the agent binary after "--".
func (c *Client) AgentStart(ctx context.Context, name, kind, paneID string, timeout time.Duration, agentArgs ...string) (*Agent, error) {
	args := []string{"agent", "start", name, "--kind", kind, "--pane", paneID}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	if len(agentArgs) > 0 {
		args = append(args, "--")
		args = append(args, agentArgs...)
	}
	var out struct {
		Agent Agent `json:"agent"`
	}
	if err := c.run(ctx, &out, args...); err != nil {
		return nil, err
	}
	return &out.Agent, nil
}

// AgentGet returns the current state of one agent.
func (c *Client) AgentGet(ctx context.Context, target string) (*Agent, error) {
	var out struct {
		Agent Agent `json:"agent"`
	}
	if err := c.run(ctx, &out, "agent", "get", target); err != nil {
		return nil, err
	}
	return &out.Agent, nil
}

// AgentPrompt submits a prompt and waits for the agent to settle.
//
// It deliberately waits on herdr's default settled set (idle, done, blocked)
// rather than a single state: a completed claude turn may land on "done"
// (session exited) or "idle" (session still up), and "blocked" means the agent
// is waiting on a human. Callers branch on the returned status.
func (c *Client) AgentPrompt(ctx context.Context, target, text string, timeout time.Duration) (AgentStatus, error) {
	args := []string{"agent", "prompt", target, text, "--wait"}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	if err := c.run(ctx, nil, args...); err != nil {
		// A timeout is not a transport failure: the run is parked, and the
		// caller still needs the agent's last observed status.
		if Code(err, "timeout") {
			ag, getErr := c.AgentGet(ctx, target)
			if getErr != nil {
				return StatusUnknown, err
			}
			return ag.AgentStatus, err
		}
		return StatusUnknown, err
	}
	ag, err := c.AgentGet(ctx, target)
	if err != nil {
		return StatusUnknown, err
	}
	return ag.AgentStatus, nil
}

// AgentWait blocks until the agent reaches one of the given states.
func (c *Client) AgentWait(ctx context.Context, target string, timeout time.Duration, until ...AgentStatus) error {
	args := []string{"agent", "wait", target}
	for _, s := range until {
		args = append(args, "--until", string(s))
	}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	return c.run(ctx, nil, args...)
}

// AgentRead returns the agent's terminal output.
//
// This is a terminal scrape containing redraws and escape sequences. It is
// archived as a human-readable transcript and must never be parsed to decide a
// run's outcome; result.json is the only authority for that.
func (c *Client) AgentRead(ctx context.Context, target string) (string, error) {
	cmd := c.Bin
	out, err := runRaw(ctx, cmd, "agent", "read", target)
	if err != nil {
		return "", err
	}
	return out, nil
}

// AgentFocus brings an agent's pane into view.
func (c *Client) AgentFocus(ctx context.Context, target string) error {
	return c.run(ctx, nil, "agent", "focus", target)
}

// AgentRename gives an agent a name in herdr's own agent list.
//
// This is what makes @mentions land on one agent instead of a directory full of
// them. Herdr detects agents but does not name them: `agent list` reports the
// kind (`claude`), the pane and the working directory, and nothing else. With
// three sessions open in ~/dotfiles, every one of them answers to @dotfiles and
// all three get told. A name is the only field that can distinguish them, and
// nothing sets it unless somebody says so.
func (c *Client) AgentRename(ctx context.Context, target, name string) error {
	return c.run(ctx, nil, "agent", "rename", target, name)
}

// AgentClearName removes an agent's name, leaving it identified by its
// directory again — which is what an agent should do on the way out, so a
// mention does not keep resolving to a pane whose session has ended.
func (c *Client) AgentClearName(ctx context.Context, target string) error {
	return c.run(ctx, nil, "agent", "rename", target, "--clear")
}

// PaneMetadata is display-only information about a pane.
type PaneMetadata struct {
	DisplayAgent string
	Title        string
	StateLabels  map[string]string
	Tokens       map[string]string
}

// ReportPaneMetadata labels one pane in Herdr's agents list.
//
// Scoped to the panes bermuda creates and to bermuda's own source, so it
// changes how bermuda's runs are displayed and nothing else. Herdr's default
// behaviour, and every pane bermuda did not create, are left alone.
func (c *Client) ReportPaneMetadata(ctx context.Context, paneID, source string, opts PaneMetadata) error {
	args := []string{"pane", "report-metadata", paneID, "--source", source}
	if opts.DisplayAgent != "" {
		args = append(args, "--display-agent", opts.DisplayAgent)
	}
	if opts.Title != "" {
		args = append(args, "--title", opts.Title)
	}
	for status, text := range opts.StateLabels {
		args = append(args, "--state-label", status+"="+text)
	}
	for name, value := range opts.Tokens {
		args = append(args, "--token", name+"="+value)
	}
	return c.run(ctx, nil, args...)
}

// BoardAgent is the label the board claims its own pane under.
//
// It is here rather than beside the board because being in Herdr's agent list
// has a consequence the board does not get to opt out of: anything in that list
// looks promptable, and the board is a TUI where a delivered message would be
// typed in as keystrokes — where a single letter is an action, and one of them
// runs the selected job. Mention delivery therefore has to recognise the board
// and skip it, which means both sides need this name.
//
// Capitalised because Herdr prints it verbatim, next to agents called Claude
// and Codex. It is a proper noun in that column, not a command being typed.
const BoardAgent = "Bermuda"

// IsBoardAgent reports whether a herdr agent label is the board's.
//
// Case-insensitive, and that is the point rather than tidiness: the label is
// baked into a binary, so during an upgrade a board still running the old one
// holds the pane under the old spelling. Matching loosely means the skip that
// keeps mentions out of a TUI cannot be defeated by which build happens to be
// on screen.
func IsBoardAgent(label string) bool {
	return strings.EqualFold(label, BoardAgent)
}

// PaneAgent is a pane claimed as an agent under a label of bermuda's choosing.
//
// Herdr detects the agents it knows how to detect, and it also lets a source
// declare one: `pane report-agent` takes the label as a free-form string, so a
// pane running something Herdr has never heard of can still be an entry in the
// agents list. That list is the only part of the sidebar a plugin can reach,
// which is what the board uses it for.
//
// Seq orders reports for the same pane. Herdr keeps the highest it has seen, so
// a slow report cannot overwrite a newer state with a stale one.
type PaneAgent struct {
	Source  string
	Agent   string
	State   AgentStatus // idle, working, blocked, unknown
	Message string
	Seq     uint64
}

// ReportPaneAgent declares a pane to be an agent, and reports its state.
//
// Herdr accepts idle, working, blocked and unknown here — done is a state it
// derives, not one it is told — so anything else is sent as unknown rather than
// rejected by herdr with an error the caller cannot act on.
func (c *Client) ReportPaneAgent(ctx context.Context, paneID string, a PaneAgent) error {
	state := a.State
	switch state {
	case StatusIdle, StatusWorking, StatusBlocked:
	default:
		state = StatusUnknown
	}
	args := []string{
		"pane", "report-agent", paneID,
		"--source", a.Source,
		"--agent", a.Agent,
		"--state", string(state),
	}
	if a.Message != "" {
		args = append(args, "--message", a.Message)
	}
	if a.Seq > 0 {
		args = append(args, "--seq", strconv.FormatUint(a.Seq, 10))
	}
	return c.run(ctx, nil, args...)
}

// ReleasePaneAgent gives the pane's agent identity back to herdr.
//
// The report is a claim on how a pane is displayed and it outlives the process
// that made it, so a board that exits without releasing leaves a row in the
// sidebar pointing at a pane that is no longer a board.
func (c *Client) ReleasePaneAgent(ctx context.Context, paneID, source, agent string) error {
	return c.run(ctx, nil, "pane", "release-agent", paneID, "--source", source, "--agent", agent)
}

// PluginPane says where one of this plugin's panes should open.
//
// Placement and direction are runtime choices rather than manifest ones: the
// manifest can name a placement but not a split direction, so a full-width
// horizontal board has to be requested when it is opened.
type PluginPane struct {
	Plugin     string
	Entrypoint string
	Placement  string // overlay, split, tab, zoomed
	Direction  string // right, down — split only
	Workspace  string
	TargetPane string // the pane a split divides
	// Background opens the pane without focusing it. A board opened because a
	// person asked for it should be in front of them; one opened by a startup
	// hook is furniture, and stealing focus from whatever they were doing to
	// announce it would be the opposite of convenient.
	Background bool
}

// OpenPluginPane asks Herdr to open one of this plugin's panes.
//
// A split divides an existing pane, so it is addressed by target pane and not
// by workspace: herdr answers "split and zoomed plugin panes target an existing
// pane" when it is given only a workspace, and ignores the target when it is
// given both. The two are therefore exclusive here rather than at every call
// site.
func (c *Client) OpenPluginPane(ctx context.Context, p PluginPane) error {
	args := []string{"plugin", "pane", "open", "--plugin", p.Plugin, "--entrypoint", p.Entrypoint}
	if p.Placement != "" {
		args = append(args, "--placement", p.Placement)
	}
	if p.Direction != "" {
		args = append(args, "--direction", p.Direction)
	}
	switch {
	case p.TargetPane != "":
		args = append(args, "--target-pane", p.TargetPane)
	case p.Workspace != "":
		args = append(args, "--workspace", p.Workspace)
	}
	if p.Background {
		args = append(args, "--no-focus")
	}
	return c.run(ctx, nil, args...)
}

// TabRename sets a tab's label.
func (c *Client) TabRename(ctx context.Context, tabID, label string) error {
	return c.run(ctx, nil, "tab", "rename", tabID, label)
}

// PaneList lists panes, optionally restricted to one workspace.
func (c *Client) PaneList(ctx context.Context, workspaceID string) ([]Pane, error) {
	args := []string{"pane", "list"}
	if workspaceID != "" {
		args = append(args, "--workspace", workspaceID)
	}
	var out struct {
		Panes []Pane `json:"panes"`
	}
	if err := c.run(ctx, &out, args...); err != nil {
		return nil, err
	}
	return out.Panes, nil
}
