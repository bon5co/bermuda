package board

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/bon5co/bermuda/v2/internal/mention"
)

// fakeHerd stands in for herdr. No board test may reach the real socket: the
// agents it would find are the user's live sessions, and delivering to them
// means typing into whatever somebody is in the middle of.
type fakeHerd struct {
	mu      sync.Mutex
	agents  []mention.Agent
	sent    map[string]string
	err     error
	deliver error
}

func newFakeHerd(agents ...mention.Agent) *fakeHerd {
	return &fakeHerd{agents: agents, sent: map[string]string{}}
}

func (f *fakeHerd) Live(context.Context) ([]mention.Agent, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.agents, nil
}

func (f *fakeHerd) Deliver(_ context.Context, target, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deliver != nil {
		return f.deliver
	}
	f.sent[target] = text
	return nil
}

func (f *fakeHerd) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// A message typed at the board is the one a person writes, and the person is
// usually asking an agent something. If it only landed in the thread, the agent
// would learn about it whenever it next chose to read the log — which, sitting
// idle in a pane, is never.
func TestAMessageTypedAtTheBoardReachesTheAgentItNames(t *testing.T) {
	m := newTestModel(t)
	h := newFakeHerd(mention.Agent{Target: "w1:pA", Dir: "/home/dev/dotfiles"})
	m.mentions = h

	m.press(t, "1")
	m.press(t, "i")
	m.typeText(t, "@dotfiles the browser is free")
	m.apply(t, tea.KeyMsg{Type: tea.KeyCtrlS})

	got := h.sent["w1:pA"]
	if !strings.Contains(got, "the browser is free") {
		t.Fatalf("the agent was told %q, want the message", got)
	}
	// It has to say where the message came from, or the agent has nowhere to
	// reply to.
	if !strings.Contains(got, "[thread "+m.currentThread()+"]") {
		t.Errorf("the agent was told %q, want the thread named", got)
	}
	if !strings.Contains(m.status, "told") {
		t.Errorf("the board says %q, want it to say who was reached", m.status)
	}
}

// Delivery happens inside the command bubbletea runs off its event loop, never
// in Update. A board that talked to herdr on the UI thread would freeze — no
// scrolling, no tick, no redraw — for as long as herdr took to answer, which is
// the whole pane locking up because somebody typed a name.
func TestDeliveryNeverHappensOnTheUIThread(t *testing.T) {
	m := newTestModel(t)
	h := newFakeHerd(mention.Agent{Target: "w1:pA", Name: "ada"})
	m.mentions = h

	m.press(t, "1")
	m.press(t, "i")
	m.typeText(t, "@ada ping")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("ctrl+s returned no command, so the post happened on the UI thread")
	}
	if h.count() != 0 {
		t.Fatal("Update itself talked to herdr: the board is frozen while herdr answers")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("the post command produced nothing")
	}
	if h.count() != 1 {
		t.Error("running the command did not deliver the mention")
	}
}

// Herdr being unreachable must not lose the message. The board is the one place
// a person writes into the thread, and a post that failed because a delivery
// failed would throw away what they typed.
func TestADeliveryFailureStillLeavesTheMessagePosted(t *testing.T) {
	m := newTestModel(t)
	h := newFakeHerd(mention.Agent{Target: "w1:pA", Name: "ada"})
	h.deliver = errors.New("agent_pane_busy")
	m.mentions = h

	m.press(t, "1")
	m.press(t, "i")
	m.typeText(t, "@ada please stop")
	m.apply(t, tea.KeyMsg{Type: tea.KeyCtrlS})

	if m.err != nil {
		t.Fatalf("a failed delivery failed the post: %v", m.err)
	}
	bodies := threadBodies(t, m)
	if got := bodies[len(bodies)-1]; got != "@ada please stop" {
		t.Fatalf("the thread's newest message is %q, want the one that was typed", got)
	}
	if !strings.Contains(m.status, "could not be told") {
		t.Errorf("the board says %q, want it to admit nobody was reached", m.status)
	}
}

// A mention is the only part of a message that did something, and in a scrolled
// wall of bubbles it is otherwise four characters of prose. Colouring it is how
// a reader sees that a message was addressed to somebody without reading it.
func TestAMentionIsColouredInsideTheBubble(t *testing.T) {
	// Tests run without a terminal, so lipgloss renders everything plain unless
	// a profile is set — and a highlight test against a profile that strips
	// colour would pass no matter what this code did.
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	line := "ok @ada, on it"
	got := highlightMentions(line)
	if got == line {
		t.Fatal("the mention was left plain")
	}
	if !strings.Contains(got, mentionStyle.Render("@ada")) {
		t.Errorf("rendered %q, want @ada in the mention colour", got)
	}
	// The width must not change: a bubble is padded to its column before this
	// runs, and a decorated line that measures wider pushes the right-hand
	// border out on that row alone.
	if lipgloss.Width(got) != lipgloss.Width(line) {
		t.Errorf("colouring changed the width from %d to %d",
			lipgloss.Width(line), lipgloss.Width(got))
	}
	// And it must not colour an email address, which would light up every
	// message quoting a login as though it had reached somebody.
	plain := "signed in as someone@example.com"
	if highlightMentions(plain) != plain {
		t.Errorf("an email address was rendered as a mention: %q", highlightMentions(plain))
	}
}

// The bubble is padded to its column before it is coloured, and this is the
// assertion that keeps that order: colouring first would have fit count the
// escape sequences as text and every bubble carrying a mention would come out a
// different width from the ones around it.
func TestABubbleWithAMentionIsTheSameWidthAsOneWithout(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	box := func(text string) int {
		out := bubbleBox{
			header: "ada · note", footer: "12:00", lines: []string{text},
			textW: 40, maxW: 40, decorate: highlightMentions,
		}.render()
		widest := 0
		for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := lipgloss.Width(l); w > widest {
				widest = w
			}
		}
		return widest
	}
	if with, without := box("@ada on it"), box("nobody on it"); with != without {
		t.Errorf("a bubble with a mention is %d wide and one without is %d", with, without)
	}
}
