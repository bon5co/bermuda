package mention

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Agent is one live agent a mention can reach.
//
// Three names are carried because the same agent is called three different
// things depending on who is talking about it: herdr knows it by the name it
// was started under, the pane it sits in carries a label a human typed, and the
// agent itself will describe its work by the directory it is in. An agent
// working in ~/dotfiles answers to `@dotfiles` because that is what every other
// agent calls it, whatever herdr has it filed under.
type Agent struct {
	// Target is what `herdr agent prompt` is given. It is the pane id rather
	// than the name: an agent started outside bermuda often has no name at all,
	// and every agent has a pane.
	Target string
	Name   string
	Label  string
	// Dir is the agent's working directory; its basename is the third name.
	Dir string
	// Workspace is the herdr workspace the agent's pane sits in, and is what
	// makes `@all` affordable. Membership needs no registration and no prior
	// message: herdr already knows which space every pane is in, so an agent
	// that has never written a word is still reachable in its own space and
	// still invisible from everybody else's.
	Workspace string
}

// Answers reports whether this agent goes by the mentioned text.
//
// Case-insensitively, because a mention is typed by a person or by another
// agent quoting from memory, and `@dotfiles` failing to reach the agent in
// ~/dotfiles would be indistinguishable from that agent having exited.
func (a Agent) Answers(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, known := range a.knownAs() {
		if strings.EqualFold(known, name) {
			return true
		}
	}
	return false
}

// AnsweredByName reports whether the agent carries this exact registered name.
//
// A name is deliberate: something ran `thread register` to claim it. A working
// directory is an accident of where a session was opened, and three sessions in
// one repo share it. So a name, where one exists, wins — see pickTargets.
func (a Agent) AnsweredByName(name string) bool {
	return strings.TrimSpace(a.Name) != "" &&
		strings.EqualFold(strings.TrimSpace(a.Name), strings.TrimSpace(name))
}

// pickTargets narrows the agents a single mention reaches.
//
// Loose matching is the right default when nobody has registered anything: with
// no names, an agent's directory is all there is, and reaching everyone in it
// beats reaching nobody. It is the wrong answer the moment a name exists —
// `@bermuda-review` should go to the agent that took that name, not to every
// session that happens to sit in ~/Projects/bermuda alongside it.
//
// So: if any candidate answers by registered name, only those are targets.
// Otherwise the loose matches stand.
func pickTargets(name string, live []Agent) []Agent {
	var byName, loose []Agent
	for _, a := range live {
		switch {
		case a.AnsweredByName(name):
			byName = append(byName, a)
		case a.Answers(name):
			loose = append(loose, a)
		}
	}
	if len(byName) > 0 {
		return byName
	}
	return loose
}

// knownAs is every name this agent responds to, empties dropped.
func (a Agent) knownAs() []string {
	out := make([]string, 0, 3)
	for _, known := range []string{a.Name, a.Label} {
		if strings.TrimSpace(known) != "" {
			out = append(out, known)
		}
	}
	if dir := strings.TrimSpace(a.Dir); dir != "" {
		out = append(out, filepath.Base(dir))
	}
	return out
}

// Describe names an agent for a person reading the delivery report. The target
// is always shown because it is the only name guaranteed to exist.
func (a Agent) Describe() string {
	if known := a.knownAs(); len(known) > 0 {
		return known[0] + " (" + a.Target + ")"
	}
	return a.Target
}

// Self is whoever is speaking, so the delivery can skip them.
//
// Both fields are used and neither is sufficient alone. Target is exact and is
// what a bermuda run or a herdr-launched shell knows about itself; Name is what
// a caller passing --as says it is, and is the only handle when the message is
// posted from something herdr did not start.
type Self struct {
	Target string
	Name   string
}

// isMe reports whether an agent is the speaker.
func (s Self) isMe(a Agent) bool {
	if s.Target != "" && s.Target == a.Target {
		return true
	}
	return s.Name != "" && a.Answers(s.Name)
}

// Herd is the part of herdr this package needs: who is alive, and how to say
// something to one of them.
//
// It is an interface so tests can fake it. A test that reached the real socket
// would prompt whichever agents happen to be running on this machine, which is
// a test that types into somebody's live session.
type Herd interface {
	// Live lists the agents that exist right now.
	Live(ctx context.Context) ([]Agent, error)
	// Deliver submits text to one agent and returns without waiting for it to
	// answer. Waiting would hold a `bermuda thread post` open for the length of
	// another agent's turn.
	Deliver(ctx context.Context, target, text string) error
}

// Message is what is being delivered.
type Message struct {
	// Thread and Author are carried into the delivered text: an agent that
	// receives a mention has to know where to reply, and a reply into the wrong
	// thread is a reply nobody reads.
	Thread string
	Author string
	Body   string
	// Workspace is the herdr workspace this thread belongs to, and is the bound
	// on `@all`. Empty means the thread has no space behind it — global, or one
	// somebody made by hand — and `@all` in one of those is refused rather than
	// widened to the machine. See allScope.
	Workspace string
}

// Text is what the mentioned agent is shown.
func (m Message) Text() string {
	return fmt.Sprintf("[thread %s] %s: %s", m.Thread, m.Author, m.Body)
}

// Delivery is one attempt to reach one agent.
type Delivery struct {
	Agent Agent
	Err   error
}

// Result is what happened to every mention in a message.
//
// Missed is not a failure. It is the ordinary outcome of mentioning an agent
// that has finished — which is most agents, most of the time — and reporting it
// as an error would train everyone to ignore the report.
type Result struct {
	Delivered []Delivery
	Failed    []Delivery
	// Missed are mentions that matched no live agent.
	Missed []string
	// Mine are mentions that matched only the speaker. Skipping them is what
	// stops `@all` from becoming a message an agent sends to itself, reads, and
	// answers.
	Mine []string
	// Refused are mentions that were not resolved at all because the thread they
	// were written in has no space to bound them. It is only ever `@all`, and it
	// is reported rather than silently dropped: an agent that believes it has
	// broadcast and has not is worse off than one that is told to go and say it
	// somewhere specific.
	Refused []string
}

// Reached reports whether anybody was actually told.
func (r Result) Reached() bool { return len(r.Delivered) > 0 }

// Empty reports whether there was nothing to do — no mentions at all, which is
// the common case and deserves no output.
func (r Result) Empty() bool {
	return len(r.Delivered) == 0 && len(r.Failed) == 0 &&
		len(r.Missed) == 0 && len(r.Mine) == 0 && len(r.Refused) == 0
}

// allScope decides who `@all` may reach, and returns false when the answer is
// nobody.
//
// `@all` used to mean every live agent on the machine. That is a broadcast into
// the context of every agent running, most of which are working on something
// else and none of which can act on it — paid for in tokens by all of them, on
// every send. Bounded to a workspace it means what people assume it means: the
// agents working on this, in this window.
//
// A thread with no workspace has no such bound, so `@all` in one is refused
// outright rather than falling back to the machine. Global is the case that
// matters: it is the default for anything that never named a thread, so a
// fallback there would leave the old behaviour in place for exactly the agents
// least likely to have thought about it.
func allScope(msg Message, live []Agent) ([]Agent, bool) {
	ws := strings.TrimSpace(msg.Workspace)
	if ws == "" {
		return nil, false
	}
	var out []Agent
	for _, a := range live {
		if strings.TrimSpace(a.Workspace) == ws {
			out = append(out, a)
		}
	}
	return out, true
}

// Resolve works out who a message is addressed to.
//
// Resolution is deliberately a superset: one mention may reach several agents,
// because two agents working in the same directory are both plausibly the one
// being talked to and silently picking one of them is how a question goes to
// the agent that was not asked.
func Resolve(msg Message, live []Agent, self Self) Result {
	var r Result
	sent := map[string]bool{}
	for _, name := range Names(msg.Body) {
		var matched, mine bool
		var candidates []Agent
		if strings.EqualFold(name, All) {
			scoped, ok := allScope(msg, live)
			if !ok {
				r.Refused = append(r.Refused, name)
				continue
			}
			candidates = scoped
		} else {
			// A name is deliberate and singular, so it is still resolved across
			// the machine. The cost `@all` was refused for is the breadth, not
			// the crossing: `@dotfiles` reaches one agent whoever asks, and
			// scoping it would only mean a question going unanswered with nothing
			// said about why.
			candidates = pickTargets(name, live)
		}
		for _, a := range candidates {
			if self.isMe(a) {
				mine = true
				continue
			}
			matched = true
			if sent[a.Target] {
				// Two mentions of the same agent are one delivery. An agent told
				// twice about one message answers twice.
				continue
			}
			sent[a.Target] = true
			r.Delivered = append(r.Delivered, Delivery{Agent: a})
		}
		switch {
		case matched:
		case mine:
			// @all with nobody else in the space lands here too, which is honest:
			// the only agent that matched was the one that wrote the message.
			r.Mine = append(r.Mine, name)
		default:
			r.Missed = append(r.Missed, name)
		}
	}
	return r
}

// Deliver resolves the mentions in a message and pushes it to each of them.
//
// The error is only ever "herdr could not be asked who is alive". Everything
// else — an agent that has exited, a prompt that was refused — is in the
// Result, because none of it means the message was not posted.
func Deliver(ctx context.Context, h Herd, msg Message, self Self) (Result, error) {
	if h == nil || len(Names(msg.Body)) == 0 {
		return Result{}, nil
	}
	live, err := h.Live(ctx)
	if err != nil {
		return Result{}, err
	}
	r := Resolve(msg, live, self)
	text := msg.Text()
	var delivered []Delivery
	for _, d := range r.Delivered {
		if err := h.Deliver(ctx, d.Agent.Target, text); err != nil {
			d.Err = err
			r.Failed = append(r.Failed, d)
			continue
		}
		delivered = append(delivered, d)
	}
	r.Delivered = delivered
	return r, nil
}

// Report writes what happened, one line per outcome.
//
// It goes to stderr at every call site, so `thread log --json` still pipes into
// a parser and a script that only cares whether the post landed can ignore it
// entirely.
func Report(w io.Writer, r Result) {
	for _, d := range r.Delivered {
		fmt.Fprintln(w, "bermuda: told "+d.Agent.Describe())
	}
	for _, d := range r.Failed {
		fmt.Fprintf(w, "bermuda: could not tell %s: %v (the message is still in the thread)\n",
			d.Agent.Describe(), d.Err)
	}
	if len(r.Missed) > 0 {
		fmt.Fprintln(w, "bermuda: nobody live answers to @"+strings.Join(r.Missed, ", @")+
			" — it may have finished; the message is in the thread either way")
	}
	for _, name := range r.Mine {
		fmt.Fprintln(w, "bermuda: @"+name+" is you, so nothing was delivered")
	}
	for _, name := range r.Refused {
		// A warning, not an error, and the distinction is the whole message: the
		// post succeeded and nothing was lost. What the author has to understand
		// is the change in *timing* — nobody was interrupted, so this will be read
		// when other agents next look at the thread rather than now. An author who
		// thinks it was pushed will sit waiting for an answer to a message nobody
		// has been handed.
		fmt.Fprintln(w, "bermuda: warning: @"+name+" delivered to nobody — this thread has "+
			"no workspace to bound it to, and global reaches everyone or no one. The "+
			"message is posted, so other agents will read it at leisure, whenever they "+
			"next run `bermuda thread log`. If it needs attention now, post it in your "+
			"workspace's thread or name the agents you want.")
	}
}

// Status is the same outcome in one line, for a board that has one line to
// spend on it.
func Status(r Result) string {
	if r.Empty() {
		return ""
	}
	var parts []string
	if n := len(r.Delivered); n > 0 {
		who := make([]string, 0, n)
		for _, d := range r.Delivered {
			who = append(who, d.Agent.Describe())
		}
		parts = append(parts, "told "+strings.Join(who, ", "))
	}
	if n := len(r.Failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d could not be told", n))
	}
	if len(r.Missed) > 0 {
		parts = append(parts, "nobody answers to @"+strings.Join(r.Missed, ", @"))
	}
	if len(r.Refused) > 0 {
		parts = append(parts, "@"+strings.Join(r.Refused, ", @")+
			" delivered to nobody, will be read at leisure")
	}
	return strings.Join(parts, "; ")
}
