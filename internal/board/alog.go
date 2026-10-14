package board

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bon5co/bermuda/v3/internal/alog"
)

// The file feeds share navigation and rendering. Neither has a selected job,
// so their keys never reach job creation or launch actions.
func (m *Model) handleFeedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.stepTab(1)
	case "shift+tab":
		m.stepTab(-1)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.selectTab(tabOrder[int(msg.String()[0]-'1')])
	case "/":
		m.searching, m.queryDraft = true, m.query
	case "esc":
		m.query, m.scroll = "", 0
	case "j", "down":
		m.scrollBy(1)
	case "k", "up":
		m.scrollBy(-1)
	case "]", "ctrl+f", "ctrl+d", "pgdown":
		m.scrollBy(m.pageSize())
	case "[", "ctrl+b", "ctrl+u", "pgup":
		m.scrollBy(-m.pageSize())
	case "home", "g":
		m.scroll = 0
	case "end", "G":
		m.scroll = maxInt
	case "r":
		return m, m.load()
	}
	return m, nil
}

func (m *Model) isCardFeed() bool {
	return m.focus == focusALog || m.focus == focusImprove
}

func (m *Model) visibleALog() []alog.Entry    { return m.visibleFeed(m.alogEntries) }
func (m *Model) visibleImprove() []alog.Entry { return m.visibleFeed(m.improveEntries) }

func (m *Model) visibleFeed(entries []alog.Entry) []alog.Entry {
	if m.query == "" {
		return entries
	}
	var out []alog.Entry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(strings.Join([]string{
			e.ID, e.Kind, e.Repo, e.Branch, e.Topic, e.Body,
		}, "\n")), strings.ToLower(m.query)) {
			out = append(out, e)
		}
	}
	return out
}

type cardFeed struct {
	label, latestKey, empty string
	entries                 []alog.Entry
	err                     error
}

func (m *Model) alogPane(p pane) pane {
	return m.feedPane(p, cardFeed{
		label: "A.LOG", latestKey: "1", entries: m.alogEntries, err: m.alogErr,
		empty: "No activity yet. Add a summary (50 words maximum): bermuda alog write --repo acme/widget --branch feat/status --topic Progress --body \"Started work.\"",
	})
}

func (m *Model) improvePane(p pane) pane {
	return m.feedPane(p, cardFeed{
		label: "IMPROVE", latestKey: "3", entries: m.improveEntries, err: m.improveErr,
		empty: "No lessons yet. Record a discovery, mistake or recovery (unlimited body): bermuda improve write --kind discovery --repo acme/widget --branch feat/lessons --topic Evidence --body \"Observed and verified behavior.\"",
	})
}

func (m *Model) feedPane(p pane, feed cardFeed) pane {
	entries := m.visibleFeed(feed.entries)
	var head []string
	if feed.err != nil {
		head = append(strings.Split(outcomeStyles["failed"].Render(m.alogText(
			"Cannot read "+feed.label+": "+feed.err.Error()+". Fix the file or directory, then press r.")), "\n"), "")
	}
	if len(entries) == 0 {
		if feed.err == nil {
			if m.query != "" {
				head = []string{m.alogText("No entries match. Esc clears search.")}
			} else {
				head = []string{m.alogText(feed.empty)}
			}
		}
		p.body = strings.Join(head, "\n")
	} else {
		p.lines = m.cardLines(head, entries)
	}
	status := itoa(len(entries)) + " entries · newest first"
	if m.query != "" {
		status = itoa(len(entries)) + " of " + itoa(len(feed.entries)) + " match · newest first"
	}
	p.bottom = dimStyle.Render(m.alogText(status)) + "\n" + m.renderFooter() +
		"\n" + helpStyle.Render(m.alogText("tab lists · / search · j/k scroll · [ ] page · "+feed.latestKey+" latest · r refresh · M mouse · q quit"))
	return p
}

// cardLines lays a feed out as head rows followed by one card per entry, each
// card followed by a blank row, and draws only the cards the window asks for.
//
// Every card's height is known before any card is drawn — measuring is a wrap
// of plain text, drawing is lipgloss — and both are kept per card, so a frame
// over a feed that has not changed measures nothing and draws only what is on
// screen.
func (m *Model) cardLines(head []string, entries []alog.Entry) *bodyLines {
	memos := make([]*cardMemo, len(entries))
	total := len(head)
	for i, e := range entries {
		memos[i] = m.cardMemo(e)
		total += memos[i].height + 1
	}
	return &bodyLines{total: total, slice: func(from, to int) []string {
		out := make([]string, 0, to-from)
		at := 0
		take := func(rows []string) {
			for _, r := range rows {
				if at >= from && at < to {
					out = append(out, r)
				}
				at++
			}
		}
		take(head)
		for _, memo := range memos {
			if at >= to {
				break
			}
			if at+memo.height+1 <= from {
				at += memo.height + 1
				continue
			}
			take(m.drawCard(memo))
			take([]string{""})
		}
		return out
	}}
}

// cardMemo is one card as measured, and as drawn once it has been on screen.
// It belongs to one entry at one width: an entry that changes, or a pane that
// is resized, gets a new one.
type cardMemo struct {
	entry  alog.Entry
	height int
	// lines is the drawn card, nil until it has been asked for.
	lines []string
}

type cardKey struct {
	path  string
	width int
}

func (m *Model) cardMemo(e alog.Entry) *cardMemo {
	key := cardKey{path: e.Path, width: m.alogWidth()}
	if memo, ok := m.cards[key]; ok && memo.entry == e {
		return memo
	}
	if m.cards == nil {
		m.cards = map[cardKey]*cardMemo{}
	}
	m.cardMeasures++
	memo := &cardMemo{entry: e, height: m.cardHeight(e)}
	m.cards[key] = memo
	return memo
}

func (m *Model) drawCard(memo *cardMemo) []string {
	if memo.lines == nil {
		m.cardDraws++
		memo.lines = strings.Split(strings.TrimSuffix(m.alogCard(memo.entry), "\n"), "\n")
	}
	return memo.lines
}

// forgetCards drops the memo once it holds far more than the feeds do, which
// is what cards deleted from disk and widths the pane no longer has leave
// behind. Everything still on screen is re-measured on the next frame.
func (m *Model) forgetCards() {
	if len(m.cards) > 2*(len(m.alogEntries)+len(m.improveEntries))+64 {
		m.cards = nil
	}
}

// cardHeight is how many rows alogCard draws for e, worked out from the same
// wrapping without the styling.
func (m *Model) cardHeight(e alog.Entry) int {
	width := min(m.alogWidth(), threadBubbleMax+4)
	if width < 6 {
		return len(wrapText(alogPlain(e.Repo+" · "+e.Branch+"\n"+cardTopic(e)+"\n"+e.Body+"\n"+
			alogStamp(e)), m.alogWidth()))
	}
	textWidth := width - 4
	rows := 2 // the top and bottom of the frame
	for _, text := range []string{e.Repo + " · " + e.Branch, cardTopic(e), alogStamp(e), "", e.Body} {
		rows += len(wrapText(alogPlain(text), textWidth))
	}
	return rows
}

// alogPlain strips terminal commands before fitting and styling file content.
// Newlines are prose; control characters and terminal escape sequences are not.
func alogPlain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

func (m *Model) alogWidth() int {
	if m.width > 0 {
		return max(m.width, 1)
	}
	return m.contentWidth()
}

// alogText wraps chrome and errors too: no long path or help line can make a
// narrow A.LOG pane silently spend more rows than its window counted.
func (m *Model) alogText(s string) string {
	return strings.Join(wrapText(alogPlain(s), m.alogWidth()), "\n")
}

func (m *Model) alogCard(e alog.Entry) string {
	width := min(m.alogWidth(), threadBubbleMax+4)
	// A split narrower than a box's four framing columns still keeps its text.
	if width < 6 {
		return m.alogText(e.Repo+" · "+e.Branch+"\n"+cardTopic(e)+"\n"+e.Body+"\n"+
			alogStamp(e)) + "\n"
	}
	textWidth := width - 4
	var b strings.Builder
	b.WriteString(dimStyle.Render("╭"+strings.Repeat("─", width-2)+"╮") + "\n")
	write := func(text string, style lipgloss.Style) {
		for _, line := range wrapText(alogPlain(text), textWidth) {
			b.WriteString(dimStyle.Render("│ ") + style.Render(fit(line, textWidth)) + dimStyle.Render(" │") + "\n")
		}
	}
	write(e.Repo+" · "+e.Branch, headerStyle)
	write(cardTopic(e), titleStyle)
	write(alogStamp(e), dimStyle)
	write("", lipgloss.NewStyle())
	write(e.Body, lipgloss.NewStyle())
	b.WriteString(dimStyle.Render("╰"+strings.Repeat("─", width-2)+"╯") + "\n")
	return b.String()
}

func alogStamp(e alog.Entry) string {
	label := "created "
	if e.TimeSource != "birthtime" {
		label = "written (filename) "
	}
	return label + e.Created.Local().Format("2006-01-02 15:04:05 MST")
}

// Kind is a word, not colour alone, so lessons remain distinguishable in any terminal.
func cardTopic(e alog.Entry) string {
	if e.Kind == "" {
		return e.Topic
	}
	return strings.ToUpper(e.Kind) + " · " + e.Topic
}
