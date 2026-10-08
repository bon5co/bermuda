package board

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bon5co/bermuda/v3/internal/alog"
)

func TestALogIsTheOpeningTab(t *testing.T) {
	fixture := newTestModel(t)
	m := New(fixture.store, fixture.herdr, Deps{})
	if m.focus != focusALog {
		t.Fatalf("New opens focus %d, want A.LOG", m.focus)
	}
	m.Update(m.load()())
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "No activity yet") || !strings.Contains(view, "--repo acme/widget") || !strings.Contains(view, "--body") {
		t.Fatalf("empty opening feed does not show a valid write command:\n%s", view)
	}
}

func TestALogRefreshReadsNewFilesAndEdits(t *testing.T) {
	m := newTestModel(t)
	m.deps.ALogDir = filepath.Join(t.TempDir(), "alog")
	m.selectTab(focusALog)
	older, err := alog.Write(m.deps.ALogDir, alog.Entry{Repo: "acme/widget", Branch: "feat/cards", Topic: "Older", Body: "Started work."})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := alog.Write(m.deps.ALogDir, alog.Entry{Repo: "acme/widget", Branch: "feat/cards", Topic: "Newer", Body: "Finished work."})
	if err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if len(m.alogEntries) != 2 || m.alogEntries[0].ID != newer.ID || m.alogEntries[1].ID != older.ID {
		t.Fatalf("feed is not newest first: %+v", m.alogEntries)
	}
	if _, err := alog.Edit(m.deps.ALogDir, older.ID, alog.Entry{Repo: "acme/widget", Branch: "feat/cards", Topic: "Edited", Body: "Reviewed changes."}); err != nil {
		t.Fatal(err)
	}
	m.scroll = 2
	m.apply(t, m.load()())
	if m.alogEntries[1].Topic != "Edited" || !m.alogEntries[1].Created.Equal(older.Created) || m.scroll != 2 {
		t.Fatalf("edit refresh lost order, timestamp or scroll: %+v / %d", m.alogEntries, m.scroll)
	}
	// A plain filesystem editor is a second writer, and the feed must see it.
	file, err := os.ReadFile(newer.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer.Path, []byte(strings.Replace(string(file), "Finished work.", "Filesystem edit.", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	if m.alogEntries[0].Body != "Filesystem edit." || !m.alogEntries[0].Created.Equal(newer.Created) {
		t.Fatal("filesystem edit did not refresh in place")
	}
	if err := os.WriteFile(filepath.Join(m.deps.ALogDir, "broken.md"), []byte("bad file"), 0600); err != nil {
		t.Fatal(err)
	}
	m.apply(t, m.load()())
	view := ansi.Strip(m.alogPane(pane{}).body)
	if !strings.Contains(view, "broken.md") || !strings.Contains(view, "Filesystem edit.") || m.err != nil {
		t.Fatalf("A.LOG error hid valid cards or broke other tabs:\n%s", view)
	}
}

func TestALogCardsWrapAllFieldsAndStripTerminalControls(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusALog)
	entry := alog.Entry{Repo: strings.Repeat("repo", 10), Branch: strings.Repeat("branch", 10), Topic: strings.Repeat("topic", 10), Body: strings.Repeat("日本語 ", 49) + "lastword", Created: time.Now(), TimeSource: "birthtime"}
	for _, width := range []int{12, 20, 42, 80, 160} {
		m.width = width
		card := ansi.Strip(m.alogCard(entry))
		for _, line := range strings.Split(strings.TrimSuffix(card, "\n"), "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("card overflows %d columns: %q", width, line)
			}
		}
		// Remove framing and whitespace to assert metadata and body survive wraps.
		text := strings.Map(func(r rune) rune {
			if strings.ContainsRune("╭╮╰╯│─ \n", r) {
				return -1
			}
			return r
		}, card)
		for _, wanted := range []string{entry.Repo, entry.Branch, entry.Topic, "lastword"} {
			if !strings.Contains(text, wanted) {
				t.Fatalf("%d-column card loses text %q:\n%s", width, wanted, card)
			}
		}
	}
	bad := "hello\x1b[2J\x1b[31mred\x1b[0m\x1b]52;c;clipboard\a\r\b"
	if got := alogPlain(bad); got != "hellored" {
		t.Fatalf("unsafe terminal text survived: %q", got)
	}
	entry.TimeSource = "filename"
	if !strings.HasPrefix(alogStamp(entry), "written (filename)") {
		t.Fatal("fallback timestamp is claimed as filesystem creation")
	}
}

func TestALogSearchAndKeyboardMouseScroll(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusALog)
	for i := 0; i < 20; i++ {
		m.alogEntries = append(m.alogEntries, alog.Entry{ID: itoa(i), Repo: "acme/widget", Branch: "feat/cards", Topic: "topic" + itoa(i), Body: "A readable summary with marker ▸ and plenty of text.", Created: time.Now(), TimeSource: "birthtime"})
	}
	m.height = 20
	m.View()
	m.press(t, "j")
	m.View()
	if m.scroll != 1 || m.cursor != 0 {
		t.Fatalf("j does not scroll cards: scroll %d cursor %d", m.scroll, m.cursor)
	}
	m.wheelBy(t, true)
	m.View()
	if m.scroll != 4 {
		t.Fatalf("wheel does not scroll cards: %d", m.scroll)
	}
	m.press(t, "]")
	if m.scroll <= 4 {
		t.Fatal("paging does not scroll cards")
	}
	m.press(t, "1")
	if m.scroll != 0 {
		t.Fatal("1 does not return to latest")
	}
	m.press(t, "/")
	m.press(t, "TOPIC19")
	m.pressSpecial(t, tea.KeyEnter)
	if len(m.visibleALog()) != 1 || m.visibleALog()[0].ID != "19" {
		t.Fatal("case-insensitive topic search failed")
	}
	m.pressSpecial(t, tea.KeyEsc)
	if len(m.visibleALog()) != 20 {
		t.Fatal("esc does not restore feed")
	}
	for _, q := range []string{"ACME", "CARDS", "READABLE", "19"} {
		m.query = q
		if len(m.visibleALog()) == 0 {
			t.Fatalf("search misses %q", q)
		}
	}
	m.query = "no-such-entry"
	if !strings.Contains(m.alogPane(pane{}).body, "No entries match") {
		t.Fatal("search empty state missing")
	}
}

func TestALogDoesNotTriggerJobActions(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusALog)
	for _, key := range []string{"n", "e", "D", "R", "p", "f", "P", "i", "enter", "l"} {
		m.press(t, key)
	}
	if m.editor != nil || m.detail != nil || m.runDetail != nil || m.compose != nil || m.prune != nil || len(m.testRuns) != 0 {
		t.Fatal("A.LOG keys reached another tab's actions")
	}
	m.alogErr = errors.New("test read failure")
	m.selectTab(focusJobs)
	if strings.Contains(m.View(), "test read failure") {
		t.Fatal("A.LOG error leaked into JOBS")
	}
}

func TestALogNarrowViewKeepsTabsAndFooterOnScreen(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusALog)
	m.width, m.height = 25, 24
	m.alogEntries = []alog.Entry{{Repo: "acme/widget", Branch: "feat/cards", Topic: "Progress", Body: strings.Repeat("word ", 50), Created: time.Now(), TimeSource: "birthtime"}}
	for _, key := range []string{"", "j", "]"} {
		if key != "" {
			m.press(t, key)
		}
		view := ansi.Strip(m.View())
		if len(strings.Split(view, "\n")) > m.height {
			t.Fatal("feed overflows pane height")
		}
		if !strings.Contains(view, "A.LOG") || !strings.Contains(view, "newest first") || !strings.Contains(view, "q quit") {
			t.Fatalf("pinned chrome lost:\n%s", view)
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > m.width {
				t.Fatalf("feed overflows pane width: %q", line)
			}
		}
	}
	m.width, m.height = 160, 40
	frame, row := frameRow(t, m, "A.LOG")
	line := strings.Split(frame, "\n")[row]
	for _, tc := range []struct {
		label string
		want  focus
	}{{"A.LOG", focusALog}, {"THREADS", focusThread}, {"MEMORY", focusMemory}} {
		m.clickCell(t, frameColumn(t, line, tc.label), row)
		if m.focus != tc.want {
			t.Fatalf("click %s opens wrong tab", tc.label)
		}
		m.View()
	}
}
