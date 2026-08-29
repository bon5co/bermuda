package mention

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/herdrcli"
)

// Live is where herdr's view of the machine becomes the list a mention is
// resolved against, and everything downstream trusts it completely: an agent
// missing from it is an agent no mention can reach, and a field dropped on the
// way in is a name that silently stops answering.
//
// None of these tests may touch the real herdr socket. The agents it would
// return are whichever ones are running on this machine right now, and the
// package under test delivers text into them.

// stubHerdr writes a herdr stand-in that answers `agent list` and `pane list`
// from the given JSON. An empty body for either makes that subcommand fail the
// way a wedged or upgraded herdr does — non-zero, nothing on stdout.
func stubHerdr(t *testing.T, agentList, paneList string) *herdrcli.Client {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	agents := write("agents.json", agentList)
	panes := write("panes.json", paneList)

	fail := func(body string) string {
		if body == "" {
			return "exit 1"
		}
		return "exit 0"
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"agent\" ] && [ \"$2\" = \"list\" ]; then cat " + agents + "; " + fail(agentList) + "; fi\n" +
		"if [ \"$1\" = \"pane\" ] && [ \"$2\" = \"list\" ]; then cat " + panes + "; " + fail(paneList) + "; fi\n" +
		"printf '{\"id\":\"1\",\"result\":{}}\\n'\n"
	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub herdr: %v", err)
	}
	return &herdrcli.Client{Bin: bin}
}

func agentsJSON(body string) string { return `{"id":"1","result":{"agents":[` + body + `]}}` }
func panesJSON(body string) string  { return `{"id":"1","result":{"panes":[` + body + `]}}` }

// A pane's label is one of the three names an agent answers to, and it is the
// one a person actually reads on screen. It lives on the pane rather than the
// agent, so it only exists if Live joins the two lists — and if it does not,
// `@reviewing` reaches nobody while looking exactly like an agent that exited.
func TestLiveJoinsPaneLabelsOntoTheirAgents(t *testing.T) {
	c := stubHerdr(t,
		agentsJSON(`{"agent":"claude","pane_id":"w1:p2","cwd":"/home/x/Projects/bermuda","name":"reviewer"}`),
		panesJSON(`{"pane_id":"w1:p2","label":"reviewing"},{"pane_id":"w9:p9","label":"somebody else"}`),
	)
	live, err := FromHerdr(c).Live(context.Background())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("Live = %d agents, want 1: %+v", len(live), live)
	}
	if live[0].Label != "reviewing" {
		t.Errorf("Label = %q, want the pane's label %q", live[0].Label, "reviewing")
	}
	if !live[0].Answers("reviewing") {
		t.Error("the agent does not answer to the label a person sees on its pane")
	}
	// The label belongs to the pane it was listed under and to no other agent.
	if live[0].Answers("somebody else") {
		t.Error("a label from a different pane reached this agent")
	}
}

// The pane list is a second call and is allowed to fail on its own. Losing it
// costs one of three names; treating it as fatal would cost every mention on
// the machine, and the failure that would do it — a herdr that changed its
// output, an upgrade mid-session — is exactly the case where an author most
// needs `@name` to still work.
func TestLiveStillListsAgentsWhenThePaneListFails(t *testing.T) {
	c := stubHerdr(t,
		agentsJSON(`{"agent":"claude","pane_id":"w1:p2","cwd":"/home/x/Projects/bermuda","name":"reviewer"}`),
		"", // pane list exits non-zero
	)
	live, err := FromHerdr(c).Live(context.Background())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("Live = %d agents, want the agent list to stand on its own: %+v", len(live), live)
	}
	if live[0].Label != "" {
		t.Errorf("Label = %q, want it empty when no pane list came back", live[0].Label)
	}
	if !live[0].Answers("reviewer") || !live[0].Answers("bermuda") {
		t.Error("the name and the directory should still answer with no labels")
	}
}

// Losing the agent list is the other case, and it is not the same: nobody can
// be reached, so the caller has to be told rather than handed an empty machine.
// An empty list and a broken herdr look identical to a caller that only counts
// agents, and one of them means the mention silently went nowhere.
func TestLiveReportsAFailingAgentList(t *testing.T) {
	c := stubHerdr(t, "", panesJSON(``))
	live, err := FromHerdr(c).Live(context.Background())
	if err == nil {
		t.Fatalf("Live returned %+v and no error when herdr could not list agents", live)
	}
	if live != nil {
		t.Errorf("Live = %+v alongside an error, want no agents", live)
	}
}

// The target is the pane, so an agent herdr knows about but has no pane for
// cannot be prompted however it is named. Keeping it would put a name in the
// list that resolves and then fails to deliver on every single send.
func TestLiveDropsAnAgentWithNoPane(t *testing.T) {
	c := stubHerdr(t,
		agentsJSON(
			`{"agent":"claude","pane_id":"","cwd":"/home/x/Projects/bermuda","name":"detached"},`+
				`{"agent":"claude","pane_id":"w1:p2","cwd":"/home/x/Projects/bermuda","name":"reviewer"}`),
		panesJSON(``),
	)
	live, err := FromHerdr(c).Live(context.Background())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("Live = %d agents, want only the one with a pane: %+v", len(live), live)
	}
	if live[0].Name != "reviewer" {
		t.Errorf("kept %q, want the agent that has a pane", live[0].Name)
	}
}

// The workspace is what bounds `@all`. If it does not survive the join, every
// agent arrives with an empty workspace, `allScope` matches on that empty
// string, and a broadcast meant for one window reaches every agent on the
// machine — which is the behaviour the bound exists to end.
func TestLiveCarriesTheWorkspaceThatBoundsAll(t *testing.T) {
	c := stubHerdr(t,
		agentsJSON(
			`{"agent":"claude","pane_id":"w1:p2","cwd":"/home/x/a","name":"here","workspace_id":"ws-1"},`+
				`{"agent":"claude","pane_id":"w2:p2","cwd":"/home/x/b","name":"elsewhere","workspace_id":"ws-2"}`),
		panesJSON(``),
	)
	live, err := FromHerdr(c).Live(context.Background())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("Live = %d agents, want 2: %+v", len(live), live)
	}
	got := map[string]string{}
	for _, a := range live {
		got[a.Name] = a.Workspace
	}
	if got["here"] != "ws-1" || got["elsewhere"] != "ws-2" {
		t.Fatalf("workspaces = %v, want each agent in its own", got)
	}

	// And the bound then holds: @all in ws-1 reaches ws-1 and stops there.
	r := Resolve(Message{Thread: "t", Body: "@all", Workspace: "ws-1"}, live, Self{Target: "w9:p9"})
	if len(r.Delivered) != 1 || r.Delivered[0].Agent.Name != "here" {
		t.Fatalf("@all in ws-1 reached %+v, want only the agent in ws-1", r.Delivered)
	}
}

// With no herdr there is nothing to deliver into, and FromHerdr says so by
// returning a nil Herd rather than a wrapper around a nil client. Deliver
// checks for exactly that, so the difference is between a post that quietly
// skips delivery and one that panics after the message is already written.
func TestWithoutAHerdrClientThereIsNoHerd(t *testing.T) {
	if h := FromHerdr(nil); h != nil {
		t.Fatalf("FromHerdr(nil) = %#v, want a nil Herd", h)
	}
	r, err := Deliver(context.Background(), FromHerdr(nil), Message{
		Thread: "t", Author: "me", Body: "@somebody are you there",
	}, Self{})
	if err != nil {
		t.Fatalf("Deliver with no herd: %v", err)
	}
	if !r.Empty() {
		t.Errorf("Deliver with no herd = %+v, want an empty Result", r)
	}
}

// Me is how an agent recognises its own message coming back. The pane id is
// exact and is what herdr injects into everything it starts; the name is the
// fallback for a caller that passed --as from something herdr did not start.
// Losing the pane id turns `@all` into a message the sender delivers to itself,
// reads, and answers.
func TestMeIdentifiesTheSpeakerByItsPane(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p7")
	self := Me("review-agent")
	if self.Target != "w1:p7" {
		t.Errorf("Target = %q, want the pane herdr injected", self.Target)
	}
	if self.Name != "review-agent" {
		t.Errorf("Name = %q, want the name the caller gave", self.Name)
	}
	if !self.isMe(Agent{Target: "w1:p7", Name: "something-else"}) {
		t.Error("the speaker's own pane is not recognised as itself")
	}
}

func TestMeFallsBackToTheNameOutsideAPane(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	self := Me("review-agent")
	if self.Target != "" {
		t.Errorf("Target = %q, want it empty with no pane in the environment", self.Target)
	}
	if !self.isMe(Agent{Target: "w1:p7", Name: "review-agent"}) {
		t.Error("with no pane, the speaker should still be recognised by name")
	}
	// An empty Target must not match every agent that also has none.
	if self.isMe(Agent{Name: "somebody-else"}) {
		t.Error("an empty pane id matched an unrelated agent")
	}
}
