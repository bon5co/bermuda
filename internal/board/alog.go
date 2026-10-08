package board

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bon5co/bermuda/v3/internal/alog"
)

// A.LOG is a continuous, newest-first card feed. It has no selected job, so
// its keyboard never falls through to job creation or launch actions.
func (m *Model) handleALogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.stepTab(1)
	case "shift+tab":
		m.stepTab(-1)
	case "1", "2", "3", "4", "5", "6", "7":
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

func (m *Model) visibleALog() []alog.Entry {
	if m.query == "" {
		return m.alogEntries
	}
	var out []alog.Entry
	for _, e := range m.alogEntries {
		if strings.Contains(strings.ToLower(strings.Join([]string{
			e.ID, e.Repo, e.Branch, e.Topic, e.Body,
		}, "\n")), strings.ToLower(m.query)) {
			out = append(out, e)
		}
	}
	return out
}

func (m *Model) alogPane(p pane) pane {
	entries := m.visibleALog()
	var body strings.Builder
	if m.alogErr != nil {
		body.WriteString(outcomeStyles["failed"].Render(m.alogText(
			"Cannot read A.LOG: " + m.alogErr.Error() + ". Fix the file or directory, then press r.")))
		body.WriteString("\n\n")
	}
	if len(entries) == 0 && m.alogErr == nil {
		if m.query != "" {
			body.WriteString(m.alogText("No entries match. Esc clears search."))
		} else {
			body.WriteString(m.alogText("No activity yet. Add a summary (50 words maximum): bermuda alog write --repo acme/widget --branch feat/status --topic Progress --body \"Started work.\""))
		}
	}
	for _, entry := range entries {
		body.WriteString(m.alogCard(entry))
		body.WriteString("\n")
	}
	p.body = strings.TrimSuffix(body.String(), "\n")
	status := itoa(len(entries)) + " entries · newest first"
	if m.query != "" {
		status = itoa(len(entries)) + " of " + itoa(len(m.alogEntries)) + " match · newest first"
	}
	p.bottom = dimStyle.Render(m.alogText(status)) + "\n" + m.renderFooter() +
		"\n" + helpStyle.Render(m.alogText("tab lists · / search · j/k scroll · [ ] page · 1 latest · r refresh · M mouse · q quit"))
	return p
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
		return m.alogText(e.Repo+" · "+e.Branch+"\n"+e.Topic+"\n"+e.Body+"\n"+
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
	write(e.Topic, titleStyle)
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
