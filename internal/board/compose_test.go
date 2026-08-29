package board

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// sendKey delivers a key without running the command it returns.
//
// The input box answers every keystroke with the textarea's cursor-blink
// command, and running that blocks for the blink interval. Following it would
// make these tests take half a minute and prove nothing about the board.
func (m *Model) sendKey(msg tea.KeyMsg) { m.Update(msg) }

// typeText sends a string one key at a time, the way a person does.
func (m *Model) typeText(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		m.sendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func threadBodies(t *testing.T, m *Model) []string {
	t.Helper()
	msgs, err := m.store.ThreadLog(context.Background(), store.ThreadFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, msg.Body)
	}
	return out
}

func TestComposeOpensAndOwnsTheKeyboard(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "1")
	m.press(t, "i")
	if m.compose == nil {
		t.Fatal("i should open the input box")
	}
	before := m.scroll

	// Every one of these is a board command outside the box. Inside it they are
	// letters, and a `q` that quits mid-sentence is the worst of them.
	m.typeText(t, "quit j k 3")
	if m.compose == nil {
		t.Fatal("typing closed the box")
	}
	if got := m.compose.area.Value(); got != "quit j k 3" {
		t.Errorf("the box holds %q, want the text that was typed", got)
	}
	if m.scroll != before {
		t.Error("typing scrolled the thread")
	}
}

func TestComposePostsWithCtrlSAndClosesTheBox(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "1")
	m.press(t, "i")
	m.typeText(t, "fix the discriminator first")
	m.apply(t, tea.KeyMsg{Type: tea.KeyCtrlS})

	if m.compose != nil {
		t.Error("posting should close the box")
	}
	bodies := threadBodies(t, m)
	last := bodies[len(bodies)-1]
	if last != "fix the discriminator first" {
		t.Fatalf("the thread's newest message is %q", last)
	}
	// It has to be attributable, and to the person rather than to an agent.
	msgs, _ := m.store.ThreadLog(context.Background(), store.ThreadFilter{Limit: 100})
	newest := msgs[len(msgs)-1]
	if newest.By.Name != m.composeAs() {
		t.Errorf("posted as %q, want %q", newest.By.Name, m.composeAs())
	}
	if !m.isHuman(newest.By) {
		t.Error("a message posted from the board must render as a person's")
	}
	if newest.Kind != store.KindNote {
		t.Errorf("posted as kind %q, want a note", newest.Kind)
	}
}

// Enter is a newline, which is the whole reason the box commits with ctrl+s.
func TestComposeTakesMoreThanOneLine(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "1")
	m.press(t, "i")
	m.typeText(t, "first")
	m.sendKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.typeText(t, "second")
	if m.compose == nil {
		t.Fatal("enter closed the box instead of adding a line")
	}
	m.apply(t, tea.KeyMsg{Type: tea.KeyCtrlS})

	bodies := threadBodies(t, m)
	if got := bodies[len(bodies)-1]; got != "first\nsecond" {
		t.Errorf("posted %q, want both lines", got)
	}
}

func TestEscapeAbandonsTheMessage(t *testing.T) {
	m := newTestModel(t)
	before := len(threadBodies(t, m))
	m.press(t, "1")
	m.press(t, "i")
	m.typeText(t, "never mind")
	m.sendKey(tea.KeyMsg{Type: tea.KeyEsc})

	if m.compose != nil {
		t.Error("esc should close the box")
	}
	if got := len(threadBodies(t, m)); got != before {
		t.Errorf("the thread grew to %d messages, want the %d it had", got, before)
	}
}

// Closing on an empty box would look exactly like posting, and the reader would
// go looking in the thread for a message that was never written.
func TestPostingNothingRefusesInsteadOfClosingQuietly(t *testing.T) {
	m := newTestModel(t)
	before := len(threadBodies(t, m))
	m.press(t, "1")
	m.press(t, "i")
	m.apply(t, tea.KeyMsg{Type: tea.KeyCtrlS})

	if m.compose == nil {
		t.Fatal("an empty post should keep the box open")
	}
	if m.compose.err == "" {
		t.Error("an empty post should say why nothing happened")
	}
	if got := len(threadBodies(t, m)); got != before {
		t.Errorf("the thread grew to %d messages", got)
	}
}

// The box you type into has to be on screen, and the thread pins to its live
// end when it opens for exactly that reason.
func TestComposeIsRenderedAtTheLiveEndOfTheThread(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 14
	m.focus = focusThread
	m.press(t, "k") // scroll up into history, giving up follow
	m.press(t, "i")
	if !m.threadFollow {
		t.Error("opening the box should jump back to the live end of the thread")
	}
	out := m.View()
	if !strings.Contains(out, "ctrl+s post") {
		t.Error("the open input box is not visible in the rendered pane")
	}
}

// BERMUDA_THREAD_HUMAN names whoever is actually sitting at this board.
func TestComposeIdentityCanBeNamed(t *testing.T) {
	m := newTestModel(t)
	if got := m.composeAs(); got != composeDefault {
		t.Errorf("board posts as %q by default, want %q", got, composeDefault)
	}
	t.Setenv("BERMUDA_THREAD_HUMAN", "mira")
	if got := m.composeAs(); got != "mira" {
		t.Errorf("board posts as %q, want the configured name", got)
	}
	if !m.isHuman(store.Identity{Name: "mira"}) {
		t.Error("the configured name should render as the person at the board")
	}
}
