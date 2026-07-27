package mention

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

// fakeHerd is a herd that exists only in this test.
//
// Nothing in this package's tests may touch the real herdr socket: the agents
// it would find are whichever ones are running on this machine, and delivering
// to them means typing into somebody's live session in the middle of their
// work.
type fakeHerd struct {
	agents  []Agent
	listErr error
	// failFor makes delivery to one target fail, which is how an agent that
	// exited between the list and the prompt behaves.
	failFor string
	sent    map[string]string
}

func newFakeHerd(agents ...Agent) *fakeHerd {
	return &fakeHerd{agents: agents, sent: map[string]string{}}
}

func (f *fakeHerd) Live(context.Context) ([]Agent, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.agents, nil
}

func (f *fakeHerd) Deliver(_ context.Context, target, text string) error {
	if target == f.failFor {
		return errors.New("agent_not_found")
	}
	f.sent[target] = text
	return nil
}

func (f *fakeHerd) targets() []string {
	out := make([]string, 0, len(f.sent))
	for t := range f.sent {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// The whole point of resolving three ways is that one agent has three names and
// nobody agrees on which. An agent working in ~/dotfiles is "the dotfiles one"
// to every other agent, whatever herdr filed it under — and a mention that has
// to match herdr's spelling exactly is a mention that usually reaches nobody.
func TestAnAgentAnswersToItsNameItsLabelAndItsDirectory(t *testing.T) {
	agents := []Agent{
		{Target: "w1:pA", Name: "agent-main", Dir: "/home/dev/dotfiles"},
		{Target: "w1:pB", Label: "tiktok", Dir: "/home/dev/Projects/bermuda"},
	}
	for _, tc := range []struct{ body, want string }{
		{"@agent-main ping", "w1:pA"}, // the herdr name
		{"@dotfiles ping", "w1:pA"},   // the working directory
		{"@dotfiles ping", "w1:pA"},   // and case does not matter
		{"@tiktok ping", "w1:pB"},     // the pane label
		{"@bermuda ping", "w1:pB"},    // its directory again
	} {
		r := Resolve(tc.body, agents, Self{})
		if len(r.Delivered) != 1 || r.Delivered[0].Agent.Target != tc.want {
			t.Errorf("%q resolved to %+v, want %s", tc.body, r.Delivered, tc.want)
		}
	}
}

// @all is how "camoufox is gone, stop trying to use it" reaches everyone at
// once. It must not include the sender: an agent that is handed its own
// broadcast reads it, treats it as a new instruction, and broadcasts again.
func TestAllIsEverybodyExceptTheSender(t *testing.T) {
	h := newFakeHerd(
		Agent{Target: "w1:pA", Name: "ada"},
		Agent{Target: "w1:pB", Name: "tiktok"},
		Agent{Target: "w1:pC", Name: "scraper"},
	)
	r, err := Deliver(context.Background(), h, Message{
		Thread: "global", Author: "ada", Body: "@all camoufox is gone"},
		Self{Target: "w1:pA"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.targets(), ","); got != "w1:pB,w1:pC" {
		t.Fatalf("delivered to %s, want everybody but the sender", got)
	}
	if len(r.Delivered) != 2 {
		t.Errorf("reported %d deliveries, want 2", len(r.Delivered))
	}
}

// The sender is excluded by name as well as by pane, because a message posted
// with --as from a shell herdr did not start has no pane to compare. Without
// the name check, an agent that signs off "@ada done" — its own name, the
// way people write to themselves — prompts itself and starts another turn.
func TestTheSenderIsSkippedByNameToo(t *testing.T) {
	h := newFakeHerd(Agent{Target: "w1:pA", Dir: "/home/dev/dotfiles"})
	r, err := Deliver(context.Background(), h, Message{
		Thread: "global", Author: "dotfiles", Body: "@dotfiles noted"},
		Self{Name: "dotfiles"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("delivered %v to the sender", h.sent)
	}
	if len(r.Mine) != 1 || len(r.Missed) != 0 {
		t.Errorf("reported %+v, want the mention recorded as the sender's own", r)
	}
}

// Mentioning an agent that has finished is the ordinary case: the thread is
// full of names of agents that exited hours ago, and an agent writing a reply
// has no way to know whether the one it is answering is still alive. Failing
// the post here would mean the record was lost because the courtesy could not
// be paid.
func TestAMentionThatReachesNobodyIsNotAFailure(t *testing.T) {
	h := newFakeHerd(Agent{Target: "w1:pA", Name: "ada"})
	r, err := Deliver(context.Background(), h, Message{
		Thread: "global", Author: "handler", Body: "@long-gone are you there"},
		Self{})
	if err != nil {
		t.Fatalf("an absent agent must not be an error: %v", err)
	}
	if len(r.Missed) != 1 || r.Missed[0] != "long-gone" {
		t.Errorf("reported %+v, want long-gone missed", r)
	}
	var out strings.Builder
	Report(&out, r)
	if !strings.Contains(out.String(), "long-gone") {
		t.Errorf("the report %q does not say who was not reached", out.String())
	}
}

// A prompt can fail after the agent was listed — it exited in between, or its
// pane is busy. That is reported and nothing else: the message is already in
// the thread, and an error here would tell the caller the post did not happen.
func TestADeliveryFailureIsReportedNotRaised(t *testing.T) {
	h := newFakeHerd(
		Agent{Target: "w1:pA", Name: "ada"},
		Agent{Target: "w1:pB", Name: "tiktok"},
	)
	h.failFor = "w1:pA"
	r, err := Deliver(context.Background(), h, Message{
		Thread: "global", Author: "handler", Body: "@ada @tiktok look"},
		Self{})
	if err != nil {
		t.Fatalf("a refused prompt must not be an error: %v", err)
	}
	if len(r.Failed) != 1 || r.Failed[0].Agent.Target != "w1:pA" {
		t.Errorf("reported %+v, want the failure against w1:pA", r)
	}
	// The one that could be reached still was: one dead agent must not stop the
	// message reaching the others.
	if len(r.Delivered) != 1 || r.Delivered[0].Agent.Target != "w1:pB" {
		t.Errorf("reported %+v, want w1:pB delivered", r)
	}
	var out strings.Builder
	Report(&out, r)
	if !strings.Contains(out.String(), "could not tell") {
		t.Errorf("the report %q does not admit the failure", out.String())
	}
}

// Herdr not answering at all — no server, a bermuda command from cron — is the
// one thing Deliver reports as an error, and even then the caller only prints
// it. It must not be confused with "nobody was mentioned".
func TestHerdrBeingUnreachableIsAnErrorAndNothingElse(t *testing.T) {
	h := newFakeHerd(Agent{Target: "w1:pA", Name: "ada"})
	h.listErr = errors.New("no herdr server")
	if _, err := Deliver(context.Background(), h, Message{
		Thread: "global", Author: "handler", Body: "@ada hello"}, Self{}); err == nil {
		t.Error("a herd that cannot be listed should be reported")
	}
	// And a message with no mentions never asks herdr anything at all: most
	// messages have none, and a `thread post` from cron should not pay for a
	// herdr call to find that out.
	h.listErr = errors.New("must not be called")
	if _, err := Deliver(context.Background(), h, Message{
		Thread: "global", Author: "handler", Body: "no names here"}, Self{}); err != nil {
		t.Errorf("a message with no mentions asked herdr anyway: %v", err)
	}
}

// The delivered text has to say which thread it came from. An agent that reads
// "the browser is free" with no thread has to guess where to reply, and a reply
// in the wrong conversation is a reply the asker never sees.
func TestTheDeliveredTextSaysWhereToReply(t *testing.T) {
	h := newFakeHerd(Agent{Target: "w1:pA", Name: "ada"})
	if _, err := Deliver(context.Background(), h, Message{
		Thread: "tiktok-deals", Author: "handler", Body: "@ada the browser is free"},
		Self{}); err != nil {
		t.Fatal(err)
	}
	got := h.sent["w1:pA"]
	if !strings.Contains(got, "[thread tiktok-deals]") || !strings.Contains(got, "handler:") {
		t.Errorf("delivered %q, want the thread and the author in it", got)
	}
	if !strings.Contains(got, "the browser is free") {
		t.Errorf("delivered %q, want the message itself", got)
	}
}

// Two agents in the same directory are both plausibly the one being talked to,
// so both are told: picking one silently is how a question goes to the agent
// that was not asked. The same agent named twice is still told once.
func TestOneMentionMayReachTwoAgentsAndOneAgentIsToldOnce(t *testing.T) {
	h := newFakeHerd(
		Agent{Target: "w1:pA", Dir: "/home/dev/dotfiles"},
		Agent{Target: "w1:pB", Dir: "/home/dev/dotfiles"},
	)
	r, err := Deliver(context.Background(), h, Message{
		Thread: "global", Author: "handler", Body: "@dotfiles ping, @dotfiles again"},
		Self{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.targets(), ","); got != "w1:pA,w1:pB" {
		t.Fatalf("delivered to %s, want both agents in that directory", got)
	}
	if len(r.Delivered) != 2 {
		t.Errorf("reported %d deliveries, want one per agent however often it was named",
			len(r.Delivered))
	}
}

// Registered names beat directories, which is the whole reason bermuda tells
// herdr who it is.
//
// Herdr detects agents but does not name them: `agent list` reports the kind,
// the pane and the working directory. With three sessions open in one repo, all
// three answered to @<repo> and all three were told — which is broadcasting,
// not addressing. Once an agent has claimed a name, a mention of that name must
// reach it alone.
func TestARegisteredNameBeatsAWorkingDirectory(t *testing.T) {
	// The case that actually bites: one agent has claimed the name `bermuda`,
	// and two others merely happen to be working in ~/Projects/bermuda. Without
	// precedence, @bermuda reaches all three and the one that claimed the name
	// is indistinguishable from the bystanders.
	named := Agent{Target: "w1:pF", Name: "bermuda", Dir: "/home/dev/dotfiles"}
	sibling := Agent{Target: "w1:pE", Dir: "/home/dev/Projects/bermuda"}
	third := Agent{Target: "w2:pA", Dir: "/home/dev/Projects/bermuda"}

	r := Resolve("@bermuda look at this", []Agent{named, sibling, third}, Self{})
	if len(r.Delivered) != 1 || r.Delivered[0].Agent.Target != "w1:pF" {
		t.Fatalf("a registered name reached %d agents (%v), want only the one that claimed it",
			len(r.Delivered), deliveredTargets(r))
	}

	// The directory still works when nobody has registered anything: with no
	// names, reaching everyone in the directory beats reaching nobody.
	r = Resolve("@bermuda look at this", []Agent{sibling, third}, Self{})
	if len(r.Delivered) != 2 {
		t.Errorf("an unregistered directory mention reached %d agents, want both", len(r.Delivered))
	}

	// And a directory mention still reaches a named agent sitting in it — the
	// name is an addition, not a way to hide.
	r = Resolve("@dotfiles look at this", []Agent{named, Agent{Target: "w9:pZ", Dir: "/home/dev/dotfiles"}}, Self{})
	if len(r.Delivered) != 2 {
		t.Errorf("a directory mention reached %d agents, want both including the named one",
			len(r.Delivered))
	}
}

// A name is matched exactly, not as a prefix or a substring: @review must not
// reach `review-bot` as well, or narrowing by name buys nothing.
func TestARegisteredNameIsMatchedWholeAndCaseInsensitively(t *testing.T) {
	a := Agent{Target: "p1", Name: "review"}
	b := Agent{Target: "p2", Name: "review-bot"}

	r := Resolve("@review ping", []Agent{a, b}, Self{})
	if len(r.Delivered) != 1 || r.Delivered[0].Agent.Target != "p1" {
		t.Errorf("@review reached %v, want only the agent named review", deliveredTargets(r))
	}
	if r = Resolve("@REVIEW ping", []Agent{a, b}, Self{}); len(r.Delivered) != 1 {
		t.Errorf("@REVIEW reached %d agents, want the one named review", len(r.Delivered))
	}
}

// deliveredTargets is what the result actually reached, for failure messages.
func deliveredTargets(r Result) []string {
	out := make([]string, 0, len(r.Delivered))
	for _, d := range r.Delivered {
		out = append(out, d.Agent.Target)
	}
	return out
}
