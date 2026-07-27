package mention

import (
	"context"
	"os"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
)

// herd is Herd backed by the real herdr CLI.
type herd struct {
	client *herdrcli.Client
	// timeout bounds each herdr call. Delivery happens on the path of a command
	// that has already done its real work — the message is posted — so it must
	// not be able to hang there because herdr is wedged.
	timeout time.Duration
}

// FromHerdr wraps a herdr client as the herd a message is delivered into.
func FromHerdr(c *herdrcli.Client) Herd {
	if c == nil {
		return nil
	}
	return herd{client: c, timeout: callTimeout}
}

// callTimeout is how long any one herdr call gets. `agent prompt` without
// --wait returns as soon as the text is submitted, so this is generous for the
// normal case and short enough that a stuck herdr costs a post nothing it
// cannot afford.
const callTimeout = 5 * time.Second

// Live lists the agents that exist, joined to their panes for the labels.
//
// The pane list is a second call and is allowed to fail on its own: a label is
// one of three names an agent answers to, and losing it leaves `@name` and
// `@directory` still working. Losing the agent list is different — then nobody
// can be reached and the caller is told.
func (h herd) Live(ctx context.Context) ([]Agent, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	agents, err := h.client.AgentList(ctx)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	if panes, err := h.client.PaneList(ctx, ""); err == nil {
		for _, p := range panes {
			if p.Label != "" {
				labels[p.PaneID] = p.Label
			}
		}
	}
	out := make([]Agent, 0, len(agents))
	for _, a := range agents {
		if a.PaneID == "" {
			// Nothing to prompt: the target is the pane, so an agent without one
			// cannot be reached however it was named.
			continue
		}
		if herdrcli.IsBoardAgent(a.Agent) {
			// The board reports its own pane as an agent so that bermuda has a
			// row in Herdr's sidebar. It is not something to talk to: a mention
			// delivered here is typed into a TUI as keystrokes, where a single
			// letter is an action and one of them runs the selected job.
			//
			// It would be reached by accident rather than on purpose, too. The
			// board sits in ~/…/bermuda, and an unnamed agent answers to the
			// basename of its directory — so `@bermuda` would find it, and
			// `@all` would find it every time.
			continue
		}
		out = append(out, Agent{
			Target: a.PaneID,
			Name:   a.Name,
			Label:  labels[a.PaneID],
			Dir:    a.CWD,
		})
	}
	return out, nil
}

// Deliver pushes the text into one agent's pane.
func (h herd) Deliver(ctx context.Context, target, text string) error {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	return h.client.AgentNotify(ctx, target, text)
}

// Me is what this process knows about which agent it is.
//
// HERDR_PANE_ID is injected into every pane herdr starts, which covers an
// interactive agent, a bermuda run, and a shell a person opened — so the
// speaker is identified exactly rather than by name matching, and `@all` cannot
// hand an agent its own message back.
func Me(name string) Self {
	return Self{Target: os.Getenv("HERDR_PANE_ID"), Name: name}
}
