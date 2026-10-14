package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bon5co/bermuda/v3/internal/alog"
	"github.com/bon5co/bermuda/v3/internal/checklist"
	"github.com/bon5co/bermuda/v3/internal/improve"
)

// A board is left open in a split for hours, and what it costs while nobody
// touches it is paid for all of them. These tests pin the three things that
// made an idle board cost most of a core: re-reading every file on every
// refresh, drawing every card on every frame, and a frame three times a second
// whether anything moved or not.

// idleStore is a board over a file store with something in every directory the
// refresh reads: A.LOG, IMPROVE, checklists, and memory notes.
func idleStore(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	root := t.TempDir()
	m.deps.ALogDir = filepath.Join(root, "alog")
	m.deps.ImproveDir = filepath.Join(root, "improve")
	m.deps.MemoryDir = filepath.Join(root, "memory")
	m.deps.CheckDir = checklist.Dir(m.deps.MemoryDir)
	for i := 0; i < 5; i++ {
		if _, err := alog.Write(m.deps.ALogDir, alog.Entry{Repo: "acme/widget", Branch: "feat/idle",
			Topic: fmt.Sprintf("Card %d", i), Body: "Started work."}); err != nil {
			t.Fatal(err)
		}
		if _, err := improve.Write(m.deps.ImproveDir, improve.Entry{Kind: improve.Discovery, Repo: "acme/widget",
			Branch: "feat/idle", Topic: fmt.Sprintf("Lesson %d", i), Body: "Observed and verified."}); err != nil {
			t.Fatal(err)
		}
		if _, err := checklist.New(m.deps.CheckDir, fmt.Sprintf("List %d", i), "", time.Now().Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	notes := filepath.Join(m.deps.MemoryDir, "notes")
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(notes, fmt.Sprintf("note-%d.md", i)), []byte("see [[other]]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.selectTab(focusALog)
	return m
}

// parses is every file the refresh has opened to parse, across all of its
// readers.
func (m *Model) parses() int64 {
	return m.alogCache.Parses() + m.improveCache.Parses() + m.checkCache.Parses() + m.memoryCache.Parses()
}

func TestIdleRefreshParsesNoUnchangedFile(t *testing.T) {
	m := idleStore(t)
	m.apply(t, m.load()())
	if m.parses() == 0 || len(m.alogEntries) != 5 || len(m.improveEntries) != 5 ||
		len(m.checklists) != 5 || m.memory.Notes == 0 {
		t.Fatalf("first refresh did not read the store: parses=%d alog=%d improve=%d checks=%d notes=%d",
			m.parses(), len(m.alogEntries), len(m.improveEntries), len(m.checklists), m.memory.Notes)
	}
	before := m.parses()
	links := m.memory.Links
	for tick := 0; tick < 3; tick++ {
		m.apply(t, m.load()())
	}
	if got := m.parses() - before; got != 0 {
		t.Fatalf("three idle refreshes over an unchanged store parsed %d files, want 0", got)
	}
	if len(m.alogEntries) != 5 || len(m.improveEntries) != 5 || len(m.checklists) != 5 || m.memory.Links != links {
		t.Fatal("an idle refresh served from the cache changed what the board shows")
	}
}

func TestIdleRefreshPicksUpNewChangedAndDeletedCards(t *testing.T) {
	m := idleStore(t)
	m.apply(t, m.load()())

	// A new card, written the way an agent writes one, is on the next refresh.
	added, err := alog.Write(m.deps.ALogDir, alog.Entry{Repo: "acme/widget", Branch: "feat/idle", Topic: "Fresh", Body: "Just written."})
	if err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if len(m.alogEntries) != 6 || m.alogEntries[0].ID != added.ID {
		t.Fatalf("new card missing after one refresh: %+v", m.alogEntries)
	}

	// A card edited in place by a plain editor — no listing change, only the
	// file's own size and time — is re-read on the next refresh.
	target := m.alogEntries[3]
	data, err := os.ReadFile(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target.Path, []byte(strings.Replace(string(data), "Started work.", "Edited in place by hand.", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	before := m.parses()
	m.apply(t, m.load()())
	if got := m.parses() - before; got != 1 {
		t.Fatalf("an in-place edit of one card re-parsed %d files, want 1", got)
	}
	found := false
	for _, e := range m.alogEntries {
		if e.ID == target.ID {
			found = e.Body == "Edited in place by hand."
		}
	}
	if !found {
		t.Fatal("edited card not refreshed")
	}

	// A deleted card is gone on the next refresh.
	if err := os.Remove(added.Path); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if len(m.alogEntries) != 5 {
		t.Fatalf("deleted card still listed: %d entries", len(m.alogEntries))
	}
	for _, e := range m.alogEntries {
		if e.ID == added.ID {
			t.Fatal("deleted card still listed")
		}
	}
	if strings.Contains(ansi.Strip(m.View()), "Just written.") {
		t.Fatal("deleted card still drawn")
	}

	// The same holds for a checklist, which is read through its own cache.
	page := m.checklists[0]
	if _, err := checklist.Add(page.Path, checklist.Entry{Text: "ship the fix"}); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	ticked := false
	for _, l := range m.checklists {
		if l.Path == page.Path {
			ticked = len(l.Items) == 1
		}
	}
	if !ticked {
		t.Fatal("checklist edit not refreshed")
	}
}

// feedOf is n cards of varied length, newest first, as the A.LOG would hold
// them.
func feedOf(n int) []alog.Entry {
	out := make([]alog.Entry, n)
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := range out {
		out[i] = alog.Entry{
			ID: fmt.Sprintf("card-%05d", i), Path: fmt.Sprintf("/store/alog/card-%05d.md", i),
			Repo: "acme/widget", Branch: "feat/idle", Topic: fmt.Sprintf("Card %d", i),
			Body:    strings.Repeat("word ", 5+i%40),
			Created: base.Add(-time.Duration(i) * time.Minute), TimeSource: "birthtime",
		}
	}
	return out
}

func TestCardFeedFrameCostDoesNotGrowWithHistory(t *testing.T) {
	type cost struct{ firstDraws, againMeasures, againDraws int }
	frames := func(n int) cost {
		m := newTestModel(t)
		m.selectTab(focusALog)
		m.alogEntries = feedOf(n)
		m.View()
		first := m.cardDraws
		measures, draws := m.cardMeasures, m.cardDraws
		m.View()
		return cost{first, m.cardMeasures - measures, m.cardDraws - draws}
	}
	small, large := frames(721), frames(7000)
	if small != large {
		t.Fatalf("a frame over 7000 cards cost %+v, over 721 cost %+v; want the same", large, small)
	}
	// The pane is 40 rows, and no card is shorter than six, so a frame that
	// drew more than eight cards drew some nobody could see.
	if small.firstDraws == 0 || small.firstDraws > 8 {
		t.Fatalf("first frame drew %d cards, want only the few on screen", small.firstDraws)
	}
	if small.againMeasures != 0 || small.againDraws != 0 {
		t.Fatalf("a repeat frame over an unchanged feed measured %d and drew %d cards, want 0 and 0",
			small.againMeasures, small.againDraws)
	}
}

// The window over a feed drawn on demand must show exactly what the old
// whole-body render showed, at every scroll position and width.
func TestCardFeedWindowMatchesWholeRender(t *testing.T) {
	for _, width := range []int{4, 40, 160} {
		m := newTestModel(t)
		m.selectTab(focusALog)
		m.width = width
		m.alogEntries = feedOf(30)
		m.alogEntries[2].Body = "line one\nline two\n\nafter a gap " + strings.Repeat("x", 90)
		m.alogErr = fmt.Errorf("broken.md: missing YAML frontmatter")

		var whole strings.Builder
		whole.WriteString(outcomeStyles["failed"].Render(m.alogText(
			"Cannot read A.LOG: " + m.alogErr.Error() + ". Fix the file or directory, then press r.")))
		whole.WriteString("\n\n")
		for _, e := range m.alogEntries {
			whole.WriteString(m.alogCard(e) + "\n")
		}
		want := strings.TrimSuffix(whole.String(), "\n")
		if got := m.alogPane(pane{}).text(); got != want {
			t.Fatalf("width %d: feed body differs from the whole render", width)
		}
		for _, e := range m.alogEntries {
			if got, want := m.cardHeight(e), blockRows(m.alogCard(e)); got != want {
				t.Fatalf("width %d: card %s measured %d rows, draws %d", width, e.ID, got, want)
			}
		}
		for _, scroll := range []int{0, 1, 7, 50, 200, maxInt} {
			m.scroll = scroll
			p := m.alogPane(pane{})
			gotView := m.windowLines(*p.lines, 20)
			gotScroll := m.scroll
			m.scroll = scroll
			if wantView := m.windowBody(want, 20); gotView != wantView || gotScroll != m.scroll {
				t.Fatalf("width %d scroll %d: window differs from the whole render", width, scroll)
			}
		}
	}
}

func TestMarqueeTickStopsWhenNothingScrolls(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusALog)
	m.alogEntries = feedOf(3)
	m.marqueeOn = true
	m.View()
	frame := m.frame
	_, cmd := m.Update(marqueeTickMsg{})
	if cmd != nil || m.frame != frame || m.marqueeOn {
		t.Fatalf("marquee tick on a board where nothing scrolls kept running: cmd=%v frame %d->%d on=%v",
			cmd != nil, frame, m.frame, m.marqueeOn)
	}

	// The jobs table has a name too long for its column, so the tick keeps
	// going there and the scroll advances.
	m.jobs[0].Name = strings.Repeat("a name far too long for any column ", 4)
	m.selectTab(focusJobs)
	m.marqueeOn = true
	m.View()
	if !m.scrolling {
		t.Fatal("an overflowing job name did not register as scrolling")
	}
	_, cmd = m.Update(marqueeTickMsg{})
	if cmd == nil || m.frame != frame+1 {
		t.Fatal("marquee tick stopped while text overflowed")
	}
}

func TestMarqueeRestartsWhenTextStartsToScroll(t *testing.T) {
	m := newTestModel(t)
	sent := make(chan tea.Msg, 1)
	m.send = func(msg tea.Msg) { sent <- msg }
	m.marqueeOn = false
	m.jobs[0].Name = strings.Repeat("a name far too long for any column ", 4)
	m.View()
	if !m.marqueeOn {
		t.Fatal("a frame with scrolling text did not restart the stopped tick")
	}
	select {
	case msg := <-sent:
		if _, ok := msg.(marqueeTickMsg); !ok {
			t.Fatalf("restart sent %T, want a marquee tick", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restarted tick never arrived")
	}
	// A second frame while the tick is already running does not start another.
	m.View()
	select {
	case <-sent:
		t.Fatal("a running tick was started twice")
	case <-time.After(2 * marqueeInterval):
	}
}
