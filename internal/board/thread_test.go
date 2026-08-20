package board

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// seedThread puts one of everything in the thread: a live claim, a lapsed one,
// and the two message kinds an agent posts. The lapsed claim is the case that
// matters — the board must not show a lease whose holder is gone.
func seedThread(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	// Seeded in the order they happened, since the log's order is the order it
	// was appended in.

	// A lease taken an hour ago for ten minutes: long dead, and nothing ran to
	// notice.
	if _, err := s.ThreadClaim(ctx, store.ClaimRequest{Resource: "gpu",
		By: store.Identity{Name: "dead-agent"}, Why: "render",
		TTL: 10 * time.Minute, Now: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadPost(ctx, store.ThreadMessage{Kind: store.KindEvent,
		By: store.Identity{Name: "setup-agent"}, Body: "removed camoufox",
		CreatedAt: now.Add(-30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadPost(ctx, store.ThreadMessage{Kind: store.KindNote,
		By: store.Identity{Name: "setup-agent"}, Body: "gog configured for gmail only, no send",
		CreatedAt: now.Add(-29 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadClaim(ctx, store.ClaimRequest{Resource: "browser",
		By: store.Identity{Name: "tiktok-signup"}, Why: "tiktok signup",
		TTL: 20 * time.Minute, Now: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
}

func TestThreadTabIsReachableAndLoadsTheThread(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "1")
	if m.focus != focusThread {
		t.Fatal("3 should open the thread")
	}
	if len(m.thread) != 4 {
		t.Fatalf("the thread holds %d messages, want the 4 that were posted", len(m.thread))
	}
	// A thread reads downwards, so the newest message is last.
	if m.thread[0].Resource != "gpu" {
		t.Errorf("the thread starts with %q, want the oldest message", m.thread[0].Resource)
	}
	if m.thread[len(m.thread)-1].Resource != "browser" {
		t.Errorf("the thread ends with %q, want the newest message",
			m.thread[len(m.thread)-1].Resource)
	}
}

// The pinned claims are the thread's whole point: a lease nobody holds any
// more must not be shown as held.
func TestOnlyLiveClaimsArePinned(t *testing.T) {
	m := newTestModel(t)
	if len(m.claims) != 1 {
		t.Fatalf("board shows %d live claims, want 1: the lapsed lease on gpu is not held", len(m.claims))
	}
	if m.claims[0].Resource != "browser" {
		t.Errorf("pinned claim is on %s, want browser", m.claims[0].Resource)
	}
	// The claim was taken a minute ago, not now: an extension must not be needed
	// for "held since" to be right, and neither must a fresh read.
	if held := time.Since(m.claims[0].Since); held < 30*time.Second {
		t.Errorf("browser reads as held for %s, want about a minute", held)
	}
}

func TestThreadRendersHoldsAndEveryMessage(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusThread
	p := m.listPane()

	// The holds are chrome and the messages are the body, and the reader needs
	// both at once: what is held right now, and what was said about it.
	for _, want := range []string{"HOLDS", "browser", "tiktok-signup"} {
		if !strings.Contains(p.top, want) {
			t.Errorf("the pinned block does not mention %q", want)
		}
	}
	for _, want := range []string{"removed camoufox", "gog configured",
		"event", "note", "claim"} {
		if !strings.Contains(p.body, want) {
			t.Errorf("the thread does not mention %q", want)
		}
	}
	// The dead lease is in the log as a claim, but must not be pinned.
	if strings.Contains(p.top, "dead-agent") {
		t.Error("an expired lease is pinned as a live hold")
	}
}

// The HOLDS block answers "who do I go and ask". Two interactive agents can
// share a name — that is the bug the pid was added for — so a holder shown
// without one sends the reader to whichever ada they find first.
func TestHoldsNameWhichAgentHoldsIt(t *testing.T) {
	m := newTestModel(t)
	now := time.Now()
	m.claims = []store.Claim{{Resource: "browser",
		Holder: store.Identity{Name: "ada", PID: "5095"},
		Why:    "jlpt scrape", Since: now.Add(-time.Minute)}}

	if !strings.Contains(m.renderHolds(now), "ada#5095") {
		t.Errorf("the holds block renders as %q, and should name the holder's pid",
			m.renderHolds(now))
	}
}

func TestThreadSaysNothingIsClaimedWhenNothingIs(t *testing.T) {
	m := newTestModel(t)
	m.claims = nil
	m.focus = focusThread
	if !strings.Contains(m.renderHolds(time.Now()), "nothing is claimed") {
		t.Error("an empty holds block must say so, not render as blank space")
	}
}

// A machine can hold more leases than the pane has rows. The holds block is
// pinned, so an uncapped one would take the whole pane and leave no
// conversation under it at all.
func TestABigHoldsBlockIsCappedAndSaysWhatItHid(t *testing.T) {
	m := newTestModel(t)
	m.height = 24
	now := time.Now()
	m.claims = nil
	for i := 0; i < 30; i++ {
		m.claims = append(m.claims, store.Claim{Resource: "res" + itoa(i),
			Holder: store.Identity{Name: "agent"}, Since: now})
	}
	holds := m.renderHolds(now)
	if rows := blockRows(holds); rows > m.height/3+3 {
		t.Errorf("the holds block takes %d rows of a %d-row pane", rows, m.height)
	}
	if !strings.Contains(holds, "more held") {
		t.Errorf("the block hid holds without saying how many:\n%s", holds)
	}
}

// The thread has no selection, so the keys that act on a selected job must not
// reach it. `R` in the thread would otherwise run whichever job was first.
func TestThreadKeysCannotActOnAJob(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "1")
	for _, key := range []string{"R", "p", "f", "D", "e", "n"} {
		m.press(t, key)
	}
	if m.testRuns["alpha"] != 0 {
		t.Errorf("a key pressed in the thread ran alpha %d times", m.testRuns["alpha"])
	}
	if m.editor != nil {
		t.Error("a key pressed in the thread opened the job editor")
	}
	if j, _ := m.store.Job(context.Background(), "alpha"); !j.Enabled || !j.Favorite {
		t.Error("a key pressed in the thread changed a job behind it")
	}
}

func TestThreadSearchFiltersIt(t *testing.T) {
	m := newTestModel(t)
	m.press(t, "1")
	m.press(t, "/")
	for _, r := range "camoufox" {
		m.press(t, string(r))
	}
	if got := len(m.visibleThread()); got != 1 {
		t.Errorf("search matched %d messages, want 1", got)
	}
	// Searching by kind is the other thing a reader types.
	m.query = "claim"
	if got := len(m.visibleThread()); got != 2 {
		t.Errorf("searching for a kind matched %d messages, want the 2 claims", got)
	}
}

// A thread is followed at its newest end. Opening it on the oldest line
// would show a board full of history that is already over.
func TestTheThreadFollowsItsNewestMessage(t *testing.T) {
	m := newTestModel(t)
	// A pane too short for the thread, so there is something to scroll.
	m.height = 12
	m.focus = focusThread
	m.View()
	if !m.threadFollow {
		t.Fatal("the thread should open following the newest message")
	}
	atBottom := m.scroll

	m.press(t, "k")
	m.View()
	if m.threadFollow {
		t.Error("scrolling up should hold position instead of snapping back to live")
	}
	if m.scroll >= atBottom {
		t.Errorf("scroll is %d after moving up from %d", m.scroll, atBottom)
	}

	// 1 is the way back to live.
	m.press(t, "1")
	m.View()
	if !m.threadFollow {
		t.Error("3 should jump back to the newest message")
	}
}

func TestThreadWhenShowsTheDateOnlyWhenItIsNotToday(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.Local)
	if got := threadWhen(now.Add(-2*time.Hour), now); got != "12:00" {
		t.Errorf("today reads as %q, want the clock alone", got)
	}
	if got := threadWhen(now.AddDate(0, 0, -1), now); got != "Jul 25 14:00" {
		t.Errorf("yesterday reads as %q: 14:00 alone does not say which afternoon", got)
	}
}

// A claim's line has to answer "for how long" without opening anything else.
func TestThreadSaysFoldsTheLeaseIntoAClaimLine(t *testing.T) {
	at := time.Now()
	expires := at.Add(20 * time.Minute)
	claim := store.ThreadMessage{Kind: store.KindClaim, Body: "tiktok signup",
		CreatedAt: at, ExpiresAt: &expires}
	if got := threadSays(claim); got != "tiktok signup (ttl 20m)" {
		t.Errorf("claim line reads %q", got)
	}
	unbounded := store.ThreadMessage{Kind: store.KindClaim, CreatedAt: at}
	if got := threadSays(unbounded); got != "no expiry" {
		t.Errorf("an unbounded claim reads %q, want it flagged as never lapsing", got)
	}
	note := store.ThreadMessage{Kind: store.KindNote, Body: "plain"}
	if got := threadSays(note); got != "plain" {
		t.Errorf("a note reads %q", got)
	}
}

// The tab rule is drawn to the width of what sits under it. The thread has no
// table, so the rule must follow the bubbles instead of some other view's
// columns — and must never run past the pane.
func TestThreadRuleFollowsTheBubblesAndNotThePane(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusThread

	m.width = 400
	if got, cap := m.tableWidth(), threadBubbleMax+4; got != cap {
		t.Errorf("on a wide pane the rule runs %d columns, want the bubble cap %d", got, cap)
	}
	m.width = 50
	if got := m.tableWidth(); got != m.contentWidth() {
		t.Errorf("on a narrow pane the rule runs %d columns, want the content width %d",
			got, m.contentWidth())
	}
}

func TestThreadTabIsLabelled(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusThread
	// Plural: there is more than one conversation, and the tab is the way into
	// all of them.
	if !strings.Contains(m.renderTabs(""), "THREADS") {
		t.Error("the thread needs a tab, or there is no obvious way to reach it")
	}
}

// Threads is the leftmost tab, and the number keys count the tabs as drawn. A
// reader presses what they can see; if the two orders disagree, 1 opens
// whichever tab the focus constants happen to declare first, which is not a
// thing anybody can see.
func TestThreadsIsTheLeftmostTabAndTheKeysAgree(t *testing.T) {
	m := newTestModel(t)
	tabs := m.renderTabs("")
	iThreads, iJobs := strings.Index(tabs, "THREADS"), strings.Index(tabs, "JOBS")
	iRuns, iFlows := strings.Index(tabs, "RUNS"), strings.Index(tabs, "FLOWS")
	iForum, iMemory := strings.Index(tabs, "FORUM"), strings.Index(tabs, "MEMORY")
	if !(iThreads < iJobs && iJobs < iRuns && iRuns < iFlows && iFlows < iForum && iForum < iMemory) {
		t.Fatalf("tabs read %q, want THREADS then JOBS then RUNS then FLOWS then FORUM then MEMORY", tabs)
	}

	for _, c := range []struct {
		key  string
		want focus
	}{{"1", focusThread}, {"2", focusJobs}, {"3", focusRuns}, {"4", focusFlows}, {"5", focusForum},
		{"6", focusMemory}} {
		m.press(t, c.key)
		if m.focus != c.want {
			t.Errorf("%s opened focus %d, want %d — the keys must count the tabs left to right",
				c.key, m.focus, c.want)
		}
	}

	// Tab walks the same way round, and comes back to where it started.
	m.focus = focusThread
	for _, want := range []focus{focusJobs, focusRuns, focusFlows, focusForum, focusMemory, focusThread} {
		m.pressSpecial(t, tea.KeyTab)
		if m.focus != want {
			t.Fatalf("tab moved to focus %d, want %d", m.focus, want)
		}
	}
}

// The regression this whole view exists to fix: the body used to live in a
// ~20-column cell and *marquee* — scroll sideways and wrap around onto itself —
// so no message could be read in full at any moment.
func TestALongMessageIsWrappedWholeAndNeverScrolledSideways(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 60
	m.focus = focusThread
	body := "correction: feat/room worktree was NOT removed, it is still at " +
		"bermuda_worktrees/feat_room — my earlier event was wrong"
	if _, err := m.store.ThreadPost(context.Background(), store.ThreadMessage{
		Kind: store.KindEvent, By: store.Identity{Name: "agent-main"}, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())

	out := m.renderThreadBody()
	if strings.Contains(out, marqueeGap) {
		t.Error("the thread is still marqueeing: the body scrolls instead of wrapping")
	}
	// Every word survived somewhere in the render.
	for _, word := range strings.Fields(body) {
		if !strings.Contains(out, word) {
			t.Errorf("the rendered thread has lost %q", word)
		}
	}
	if strings.Contains(out, "…") {
		t.Error("the body was truncated; a message must wrap, not be elided")
	}
}

func TestAMessageWrittenAsTwoLinesRendersAsTwoLines(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 60
	m.focus = focusThread
	if _, err := m.store.ThreadPost(context.Background(), store.ThreadMessage{
		Kind: store.KindNote, By: store.Identity{Name: "story-gen"},
		Body: "5 stories rendered\n1 rejected by the dead-air gate",
	}); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())

	out := m.renderThreadBody()
	if !strings.Contains(out, "5 stories rendered") || !strings.Contains(out, "1 rejected") {
		t.Fatal("both authored lines should be present")
	}
	// They must be on separate rows, not joined into one.
	for _, row := range strings.Split(out, "\n") {
		if strings.Contains(row, "5 stories rendered") && strings.Contains(row, "1 rejected") {
			t.Error("the two authored lines were flattened into one")
		}
	}
}

// Which side a bubble sits on is what tells a person from an agent before any
// name has been read.
func TestAHumansBubbleSitsFurtherRightThanAnAgents(t *testing.T) {
	m := newTestModel(t)
	m.width = 90
	now := time.Now()
	agent := m.threadBubble(store.ThreadMessage{Kind: store.KindNote,
		By: store.Identity{Name: "some-agent"}, Body: "x", CreatedAt: now}, now)
	human := m.threadBubble(store.ThreadMessage{Kind: store.KindNote,
		By: store.Identity{Name: "handler"}, Body: "x", CreatedAt: now}, now)

	if lead(agent) >= lead(human) {
		t.Errorf("agent bubble starts at column %d and the human's at %d: "+
			"a person must be visibly on the other side", lead(agent), lead(human))
	}
	if lead(agent) != threadMargin {
		t.Errorf("agent bubble starts at column %d, want the board's margin %d",
			lead(agent), threadMargin)
	}
}

// lead is how far the first row of a rendered bubble is indented.
func lead(bubble string) int {
	first := strings.Split(bubble, "\n")[0]
	return len(first) - len(strings.TrimLeft(first, " "))
}

// An agent can call itself anything, including "handler". What the board can
// honestly claim is that a message came from the name it posts under with no
// job or run behind it — anything with a run is a scheduled agent.
func TestAnAgentUsingThePersonsNameIsNotTreatedAsAPerson(t *testing.T) {
	m := newTestModel(t)
	if !m.isHuman(store.Identity{Name: "handler"}) {
		t.Error("a bare handler identity should render as the person at the board")
	}
	if m.isHuman(store.Identity{Name: "handler", JobID: "nightly", RunID: "r7"}) {
		t.Error("a scheduled run calling itself handler must not render as a person")
	}
	if m.isHuman(store.Identity{Name: "ada"}) {
		t.Error("an ordinary agent is not the person at the board")
	}
}

// Every bubble takes the width it is allowed and none of them shrink to their
// text. Boxes sized to their own content leave a ragged right edge down the
// thread, so a two-word note and a wrapped paragraph read as two different
// views rather than as one conversation.
func TestEveryBubbleIsTheSameWidthWhateverItHolds(t *testing.T) {
	m := newTestModel(t)
	// Under the cap, so what is being measured is the pane's width rather than
	// threadBubbleMax flattening everything by accident.
	m.width = 70
	now := time.Now()
	msgs := []store.ThreadMessage{
		{Kind: store.KindNote, By: store.Identity{Name: "a"}, Body: "ok", CreatedAt: now},
		{Kind: store.KindEvent, By: store.Identity{Name: "setup-agent"},
			Body: "removed camoufox", CreatedAt: now},
		{Kind: store.KindClaim, By: store.Identity{Name: "a-rather-long-agent-name"},
			Resource: "a-long-resource-name", Body: strings.Repeat("word ", 40),
			CreatedAt: now},
	}

	want := m.bubbleTextMax(threadMargin) + 4 + threadMargin
	for _, msg := range msgs {
		for i, row := range boxLines(m.threadBubble(msg, now)) {
			if got := lipgloss.Width(row); got != want {
				t.Errorf("row %d of the %q bubble is %d columns, want %d — the "+
					"thread's right edge is ragged", i, msg.Kind, got, want)
			}
		}
	}
	if want >= m.width {
		t.Errorf("a bubble is %d columns on a %d-column pane", want, m.width)
	}

	// The cap still wins on a wide pane: a bubble that spanned a 400-column
	// terminal would take several eye movements to read one line of prose.
	m.width = 400
	for _, row := range boxLines(m.threadBubble(msgs[0], now)) {
		if got := lipgloss.Width(row); got != threadBubbleMax+4+threadMargin {
			t.Errorf("on a wide pane a row is %d columns, want the cap %d",
				got, threadBubbleMax+4+threadMargin)
		}
	}
}

// A human's bubble is full width too, measured from its indent — which is what
// makes the indent visible at all. Both sides end at the same column, so the
// person's box is narrower by exactly what it was pushed right.
func TestAHumansBubbleIsFullWidthFromItsIndent(t *testing.T) {
	m := newTestModel(t)
	m.width = 70
	now := time.Now()
	box := func(name, body string) int {
		rows := boxLines(m.threadBubble(store.ThreadMessage{Kind: store.KindNote,
			By: store.Identity{Name: name}, Body: body, CreatedAt: now}, now))
		return lipgloss.Width(strings.TrimLeft(rows[0], " "))
	}
	agent := box("some-agent", strings.Repeat("word ", 40))
	human := box("handler", "x")

	if want := agent - (m.humanIndent() - threadMargin); human != want {
		t.Errorf("the human bubble's box is %d columns, want %d — it should give up "+
			"exactly its indent and nothing more", human, want)
	}
}

// A bubble that runs past the pane wraps in the terminal and destroys the
// layout, so the geometry has to hold at every width the pane can take.
func TestNoBubbleEverRunsPastThePane(t *testing.T) {
	m := newTestModel(t)
	now := time.Now()
	msgs := []store.ThreadMessage{
		{Kind: store.KindNote, By: store.Identity{Name: "a"}, Body: "short", CreatedAt: now},
		{Kind: store.KindClaim, By: store.Identity{Name: "a-rather-long-agent-name"},
			Resource: "a-long-resource-name", Body: strings.Repeat("word ", 40), CreatedAt: now},
		{Kind: store.KindNote, By: store.Identity{Name: "handler"},
			Body:      "/home/dev/Projects/bermuda_worktrees/feat_room-bubbles/internal/board",
			CreatedAt: now},
	}
	for width := 20; width <= 200; width++ {
		m.width = width
		for _, msg := range msgs {
			for _, row := range strings.Split(strings.TrimRight(m.threadBubble(msg, now), "\n"), "\n") {
				if got := lipgloss.Width(row); got > width {
					t.Fatalf("at pane width %d a row runs %d columns: %q", width, got, row)
				}
			}
		}
	}
}

var _ tea.Model = (*Model)(nil)

// The read happens off the event loop, so which thread it is about has to be
// decided on the event loop and travel with the result.
//
// If the command asked m.threadID for itself, it would be reading a field that
// `t` writes from another goroutine — a data race whose visible form is a read
// tagged with whichever thread the switch happened to have got to.
func TestAReadIsAboutTheThreadItWasStartedFor(t *testing.T) {
	m := newTestModel(t)
	if _, err := m.store.NewThread(context.Background(), "quiet", "empty"); err != nil {
		t.Fatal(err)
	}
	cmd := m.load()
	// What Update does the instant the reader picks another conversation.
	m.threadID = "quiet"

	msg, ok := cmd().(dataMsg)
	if !ok {
		t.Fatal("the load command did not produce a dataMsg")
	}
	if msg.threadID != store.GlobalThread {
		t.Errorf("the read came back tagged %q, want the thread it was started for", msg.threadID)
	}
	if len(msg.thread) == 0 {
		t.Error("the read came back empty, so it read the thread that was switched to")
	}
}

// A read that was in flight when the reader switched belongs to the
// conversation they left. Applying it would put one thread's messages under
// another's name — and the compose box posts into the *named* thread, so a
// reply typed to what was on screen would land in the conversation the reader
// had already walked away from.
func TestAReadThatLandsAfterASwitchIsDropped(t *testing.T) {
	m := newTestModel(t)
	if _, err := m.store.NewThread(context.Background(), "quiet", "empty"); err != nil {
		t.Fatal(err)
	}
	// The read that is about to be overtaken.
	stale := m.load()()
	m.Update(m.load()()) // so the picker knows quiet exists

	m.press(t, "1")
	m.press(t, "t")
	m.press(t, "j")
	m.apply(t, tea.KeyMsg{Type: tea.KeyEnter})
	if m.currentThread() != "quiet" {
		t.Fatalf("the picker is reading %s, want quiet", m.currentThread())
	}
	if len(m.thread) != 0 {
		t.Fatalf("quiet shows %d messages before the stale read even lands", len(m.thread))
	}

	m.Update(stale)
	if len(m.thread) != 0 {
		t.Errorf("quiet is showing %d messages from another conversation", len(m.thread))
	}
	// The parts of the read that are the same in every thread are still taken,
	// or a dropped read would also drop the holds block and the thread list.
	if len(m.threads) == 0 {
		t.Error("dropping the stale thread also dropped the thread list")
	}
}

// The thread on screen can be deleted from a shell while the board is open. The
// board moves to global and says so — and must not leave the dead
// conversation's messages sitting under global's name, which is the wrong-thread
// render with the wrong-thread reply behind it.
func TestADeletedThreadDoesNotLeaveItsMessagesOnScreen(t *testing.T) {
	m := newTestModel(t)
	ctx := context.Background()
	if _, err := m.store.NewThread(ctx, "quiet", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.ThreadPost(ctx, store.ThreadMessage{Thread: "quiet",
		Kind: store.KindNote, By: store.Identity{Name: "someone"},
		Body: "only said here"}); err != nil {
		t.Fatal(err)
	}
	m.Update(m.load()())
	m.press(t, "1")
	m.press(t, "t")
	m.press(t, "j")
	m.apply(t, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.thread) != 1 {
		t.Fatalf("quiet shows %d messages, want the one posted into it", len(m.thread))
	}

	if _, err := m.store.RemoveThread(ctx, "quiet", true); err != nil {
		t.Fatal(err)
	}
	// The next tick's read, which is still about the thread that has gone.
	m.apply(t, m.load()())

	if m.currentThread() != store.GlobalThread {
		t.Fatalf("the board is reading %s, want global", m.currentThread())
	}
	for _, msg := range m.thread {
		if msg.Body == "only said here" {
			t.Fatal("the deleted thread's messages are being shown as global's")
		}
	}
	// And global is not left blank until the next tick: the fallback re-reads.
	if len(m.thread) == 0 {
		t.Error("global shows nothing after the fallback, so it did not re-read")
	}
}
