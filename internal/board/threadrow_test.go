package board

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/internal/store"
)

// seedThreads starts a conversation per name, each with one message at the
// given age, so activity order has something to sort.
func seedThreads(t *testing.T, m *Model, ages map[string]time.Duration) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	for id, age := range ages {
		if _, err := m.store.NewThread(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
		if age < 0 {
			// A thread nobody has posted in yet.
			continue
		}
		if _, err := m.store.ThreadPost(ctx, store.ThreadMessage{Thread: id,
			Kind: store.KindNote, By: store.Identity{Name: "agent"},
			Body: "in " + id, CreatedAt: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	m.apply(t, m.load()())
}

func threadIDs(threads []store.Thread) []string {
	out := make([]string, 0, len(threads))
	for _, t := range threads {
		out = append(out, t.ID)
	}
	return out
}

// Global first because it is where every unqualified command writes, and the
// rest by activity because that is what makes a row of bare names useful: the
// conversation somebody is having right now is at the front of it.
func TestTheThreadRowPutsGlobalFirstAndTheRestByActivity(t *testing.T) {
	m := newTestModel(t)
	seedThreads(t, m, map[string]time.Duration{
		"stale":  48 * time.Hour,
		"recent": time.Minute,
		"middle": time.Hour,
		"silent": -1,
	})

	want := []string{store.GlobalThread, "recent", "middle", "stale", "silent"}
	if got := threadIDs(m.threadOrder()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the row lists %v, want %v", got, want)
	}

	// A message moves its thread up the row, and never moves global.
	if _, err := m.store.ThreadPost(context.Background(), store.ThreadMessage{
		Thread: "stale", Kind: store.KindNote, By: store.Identity{Name: "agent"},
		Body: "back from the dead"}); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	got := threadIDs(m.threadOrder())
	if got[0] != store.GlobalThread {
		t.Errorf("global moved to position of %v — it must never move", got)
	}
	if got[1] != "stale" {
		t.Errorf("the row lists %v, want the thread that was just posted in second", got)
	}
}

// The row and the picker are two ways into the same list, so a thread that is
// third in one has to be third in the other.
func TestThePickerUsesTheSameOrderAsTheRow(t *testing.T) {
	m := newTestModel(t)
	seedThreads(t, m, map[string]time.Duration{
		"stale":  48 * time.Hour,
		"recent": time.Minute,
	})
	m.press(t, "1")
	m.press(t, "t")
	order := m.threadOrder()
	rendered := m.renderPicker()
	at := func(id string) int { return strings.Index(rendered, id) }
	for i := 1; i < len(order); i++ {
		if at(order[i-1].ID) > at(order[i].ID) {
			t.Errorf("the picker lists %s before %s, the row lists them the other way",
				order[i].ID, order[i-1].ID)
		}
	}
}

// The row is a navigation aid, not a report. Counts, unread badges and
// timestamps were all considered for it and all rejected: they are the fields
// the eye has to skip past to find the name it came for. `bermuda thread list`
// keeps them.
func TestTheThreadRowShowsNamesAndNothingElse(t *testing.T) {
	m := newTestModel(t)
	seedThreads(t, m, map[string]time.Duration{"recent": time.Minute})
	m.focus = focusThread

	row := m.renderThreadRow()
	for _, unwanted := range []string{"msg", "ago", ":"} {
		if strings.Contains(row, unwanted) {
			t.Errorf("the row carries %q beside the names: %q", unwanted, row)
		}
	}
	for _, want := range []string{store.GlobalThread, "recent", "‹", "›"} {
		if !strings.Contains(row, want) {
			t.Errorf("the row is missing %q: %q", want, row)
		}
	}
}

// A row that wraps costs a second line and every piece of height arithmetic
// under it is then wrong by one. It has to degrade instead: drop names and let
// the ‹ › markers say there are more.
func TestTheThreadRowTruncatesRatherThanWraps(t *testing.T) {
	m := newTestModel(t)
	seedThreads(t, m, map[string]time.Duration{
		"webapp": time.Minute, "game-server": 2 * time.Minute,
		"bermuda": 3 * time.Minute, "railwaytemplate2": 4 * time.Minute,
		"learn-japanese": 5 * time.Minute,
	})
	m.focus = focusThread
	m.threadID = "bermuda"

	for width := 12; width <= 160; width++ {
		m.width = width
		row := m.renderThreadRow()
		if strings.Contains(row, "\n") {
			t.Fatalf("at pane width %d the row is more than one line: %q", width, row)
		}
		if got := lipgloss.Width(row); got > width {
			t.Fatalf("at pane width %d the row runs %d columns: %q", width, got, row)
		}
		// Whatever else is dropped, the thread being read is the last name
		// standing: a row that cut it would leave the reader unable to see where
		// they are. On a pane too narrow for even one name it is elided rather
		// than dropped, which still answers the question.
		want := "bermuda"
		if width < 24 {
			want = "berm"
		}
		if !strings.Contains(row, want) {
			t.Fatalf("at pane width %d the row does not name the thread on screen: %q",
				width, row)
		}
	}
}

// `<` and `>` step along the row. They are not paging keys — `[` and `]` page —
// and they must not act like them.
func TestAngleKeysStepThroughThreadsAndDoNotPage(t *testing.T) {
	m := newTestModel(t)
	seedThreads(t, m, map[string]time.Duration{"recent": time.Minute})
	m.press(t, "1")
	if got := threadIDs(m.threadOrder()); got[0] != store.GlobalThread || got[1] != "recent" {
		t.Fatalf("the row lists %v, want global then recent", got)
	}

	m.press(t, ">")
	if m.currentThread() != "recent" {
		t.Fatalf("> left the board on %q, want the next thread in the row", m.currentThread())
	}
	// Switching re-reads, so the messages on screen are the new thread's.
	if len(m.thread) != 1 || m.thread[0].Body != "in recent" {
		t.Errorf("after > the view holds %+v, want recent's one message", m.thread)
	}
	m.press(t, "<")
	if m.currentThread() != store.GlobalThread {
		t.Errorf("< left the board on %q, want back at global", m.currentThread())
	}

	// At the end of the row they do nothing at all — and in particular they do
	// not scroll the conversation, which is what a paging key would have done.
	m.height = 12
	seedLongThread(t, m, 40)
	m.View()
	before := m.scroll
	m.press(t, "<")
	if m.currentThread() != store.GlobalThread {
		t.Errorf("< past the front of the row moved to %q", m.currentThread())
	}
	if m.scroll != before {
		t.Errorf("< scrolled the thread from %d to %d: it is not a paging key",
			before, m.scroll)
	}
}
