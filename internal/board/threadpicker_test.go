package board

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bon5co/bermuda/internal/store"
)

// seedSecondThread starts a conversation beside global with one message in it,
// which is the shape the picker exists for: a board that has somewhere else to
// go.
func seedSecondThread(t *testing.T, m *Model) {
	t.Helper()
	ctx := context.Background()
	if _, err := m.store.NewThread(ctx, "webapp", "the django saas"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.ThreadPost(ctx, store.ThreadMessage{Thread: "webapp",
		Kind: store.KindNote, By: store.Identity{Name: "bl-agent"},
		Body: "stripe keys rotated"}); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
}

// The board opens on global, because that is where every unqualified command
// writes and therefore where anything not yet sorted into a project is.
func TestTheBoardOpensOnGlobal(t *testing.T) {
	m := newTestModel(t)
	if m.currentThread() != store.GlobalThread {
		t.Errorf("the board opened on %q, want global", m.currentThread())
	}
}

// Which conversation is on screen changes what every other line means. A board
// showing an empty thread and one showing the wrong thread are otherwise
// identical, and the second is how a reply gets written where nobody reads it.
func TestTheThreadOnScreenIsNamed(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.focus = focusThread

	out := m.renderThreadRow()
	if !strings.Contains(out, store.GlobalThread) {
		t.Errorf("the thread view does not say which thread it is showing:\n%s", out)
	}
	m.threadID = "webapp"
	out = m.renderThreadRow()
	if !strings.Contains(out, "webapp") {
		t.Error("the view still does not name the thread after switching")
	}
	// The description is what tells two similarly named threads apart.
	if !strings.Contains(out, "the django saas") {
		t.Error("the thread's description is not shown beside its name")
	}
}

func TestThePickerListsEveryThreadAndSwitchesTheView(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.press(t, "1")
	m.press(t, "t")
	if m.picker == nil {
		t.Fatal("t should open the thread picker")
	}
	// It opens on the thread being read, so the first thing under the cursor is
	// where the reader already is.
	if order := m.threadOrder(); order[m.picker.cursor].ID != store.GlobalThread {
		t.Errorf("the picker opened on %q, want the thread being read",
			order[m.picker.cursor].ID)
	}
	out := m.renderPicker()
	for _, want := range []string{store.GlobalThread, "webapp", "1 msg"} {
		if !strings.Contains(out, want) {
			t.Errorf("the picker does not show %q — choosing by name alone gives no "+
				"clue which conversation is live:\n%s", want, out)
		}
	}

	m.press(t, "j")
	m.pressSpecial(t, tea.KeyEnter)
	if m.picker != nil {
		t.Error("choosing a thread should close the picker")
	}
	if m.threadID != "webapp" {
		t.Fatalf("the board is reading %q, want webapp", m.threadID)
	}
	// Switching re-reads: the messages are loaded per thread, so the view would
	// otherwise still be showing global's.
	if len(m.thread) != 1 || m.thread[0].Body != "stripe keys rotated" {
		t.Errorf("after switching the view holds %+v, want webapp's one message", m.thread)
	}
}

// The picker covers the thread, so it has to own the keyboard while it is open:
// `i` here would open a compose box behind a list hiding it.
func TestThePickerOwnsTheKeyboardWhileOpen(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.press(t, "1")
	m.press(t, "t")
	m.press(t, "i")
	if m.compose != nil {
		t.Error("i opened the compose box from behind the picker")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if m.picker != nil {
		t.Error("esc should close the picker")
	}
	if m.threadID != store.GlobalThread {
		t.Errorf("cancelling the picker switched the thread to %q", m.threadID)
	}
}

// A message typed under a thread has to go into that thread. A box that posted
// into global instead would put the answer to a question in a conversation the
// asker is not reading.
func TestComposePostsIntoTheThreadBeingViewed(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.press(t, "1")
	m.threadID = "webapp"

	m.press(t, "i")
	m.typeText(t, "use the test keys")
	m.apply(t, m.postComposed()())

	ctx := context.Background()
	log, err := m.store.ThreadLog(ctx, store.ThreadFilter{Thread: "webapp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[1].Body != "use the test keys" {
		t.Fatalf("webapp holds %+v, want the message that was typed under it", log)
	}
	global, err := m.store.ThreadLog(ctx, store.ThreadFilter{Thread: store.GlobalThread})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range global {
		if msg.Body == "use the test keys" {
			t.Error("the message was posted into global rather than the thread on screen")
		}
	}
}

// A search belongs to the conversation it was typed in. Carried across, a
// filter matching nothing in the new thread makes a full thread look empty,
// and the reader has no reason to suspect a filter they set a moment ago
// somewhere else.
func TestSwitchingThreadsClearsTheSearch(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.press(t, "1")
	m.press(t, "/")
	m.typeText(t, "camoufox")
	m.pressSpecial(t, tea.KeyEnter)
	if m.query == "" {
		t.Fatal("the search did not take")
	}

	m.press(t, "t")
	m.press(t, "j")
	m.pressSpecial(t, tea.KeyEnter)
	if m.query != "" {
		t.Errorf("the filter %q followed the reader into another thread", m.query)
	}
	if !m.threadFollow {
		t.Error("a switched-to thread should open at its newest message")
	}
}

// Search still narrows within the thread being read, and cannot reach into
// another one — the messages loaded are that thread's alone.
func TestSearchStaysInsideTheCurrentThread(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.press(t, "1")
	m.query = "stripe"
	if got := len(m.visibleThread()); got != 0 {
		t.Errorf("searching global matched %d messages from another thread", got)
	}
	m.threadID = "webapp"
	m.apply(t, m.load()())
	if got := len(m.visibleThread()); got != 1 {
		t.Errorf("searching webapp matched %d messages, want the 1 that says stripe", got)
	}
}

// One browser is one browser whichever conversation is discussing it, so the
// holds block is the same above every thread. A per-thread holds block would
// let two agents in two threads each read that the browser was free.
func TestHoldsAreTheSameAboveEveryThread(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.focus = focusThread

	// The seeded claim on browser was taken in global.
	global := m.renderHolds(m.claims[0].Since)
	m.threadID = "webapp"
	m.apply(t, m.load()())
	if len(m.claims) != 1 || m.claims[0].Resource != "browser" {
		t.Fatalf("a thread with no claims of its own shows %+v, want the browser hold", m.claims)
	}
	if other := m.renderHolds(m.claims[0].Since); other != global {
		t.Errorf("the holds block differs between threads:\n%s\nversus\n%s", global, other)
	}
}

// Two ways the thread on screen stops existing: $BERMUDA_THREAD naming one
// nobody created, and `bermuda thread rm` deleting it while the board is open.
// Both look exactly like an empty conversation, so the board has to move and
// say why rather than sit on nothing.
func TestTheBoardFallsBackWhenItsThreadIsGone(t *testing.T) {
	m := newTestModel(t)
	seedSecondThread(t, m)
	m.threadID = "webapp"
	m.apply(t, m.load()())

	if _, err := m.store.RemoveThread(context.Background(), "webapp", true); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())

	if m.currentThread() != store.GlobalThread {
		t.Fatalf("the board is still reading %q, which no longer exists", m.currentThread())
	}
	if !strings.Contains(m.status, "webapp") {
		t.Errorf("the board moved silently; status reads %q", m.status)
	}
}
