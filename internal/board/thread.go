package board

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The thread, as a third list beside JOBS and RUNS.
//
// It is rendered as a chatroom rather than as a table, because that is what it
// is: a bubble per message, oldest at the top, the newest at the bottom where
// the eye finishes. The two existing lists are newest-first because they are
// histories being searched; the thread is a conversation being followed.
//
// The first version was a table, and it failed for a reason worth keeping
// written down: a message is prose, and a table gives prose a fixed cell. The
// body ended up in a twenty-column column and then *marqueed* — the text
// scrolled sideways and wrapped around onto itself, so no single message could
// be read at all, while `ada` was printed down a column twelve times. A
// bubble that wraps costs three rows instead of one and is the only shape in
// which the thread can actually be read.
//
// Live claims are pinned above the thread rather than left to be found in it. A
// claim is the only thing here that is *currently true*, and hunting for the
// last unreleased claim in a scrolling log is exactly the work the thread
// exists to remove.

// threadKindStyles colour a message by what it is for.
//
// The palette says how much attention a line wants. An event invalidates
// somebody's memory and an ask is waiting on a person, so both take warm
// colours; a note is background and takes the dim one the rest of the board uses
// for secondary text.
var threadKindStyles = map[store.ThreadKind]lipgloss.Style{
	store.KindClaim:   lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
	store.KindRelease: lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
	store.KindEvent:   lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	store.KindAsk:     lipgloss.NewStyle().Foreground(lipgloss.Color("212")),
	store.KindNote:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
}

// threadKindStyle is the colour a message's frame is drawn in.
func threadKindStyle(kind store.ThreadKind) lipgloss.Style {
	if s, ok := threadKindStyles[kind]; ok {
		return s
	}
	return dimStyle
}

// Bubble geometry.
const (
	// threadBubbleMax caps how wide a bubble grows. Beyond about this, a line of
	// prose takes more than one eye movement to read, and the thread is read far
	// more often than it is written.
	threadBubbleMax = 74
	// threadHumanIndent pushes a human's bubble to the right. Which side a bubble
	// sits on is what tells a person from an agent at a glance, before any name
	// has been read — the one thing every chat client agrees on.
	threadHumanIndent = 8
	// threadMargin is the gutter every view on this board leaves on the left, so
	// the thread lines up with the holds block above it.
	threadMargin = 2
)

// humanStyle marks a message from a person. A person's instructions must not
// read as one more agent muttering into the log.
var humanStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))

// handleThreadKey drives the thread view.
//
// It handles almost nothing on purpose. Every key the jobs and runs lists carry
// acts on a selected job — run it, pause it, delete it — and the thread has no
// selection for them to act on. Falling through to that switch would let `R` run
// whichever job happened to be under a cursor the reader cannot see.
func (m *Model) handleThreadKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the input box is open the keyboard belongs to it: `q` is a letter
	// in a message before it is a command.
	if m.compose != nil {
		return m.handleComposeKey(msg)
	}
	// The picker covers the thread, so it owns the keys the thread would
	// otherwise take.
	if m.picker != nil {
		return m.handlePickerKey(msg)
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "i":
		m.openCompose()
		return m, nil
	case "t":
		m.openPicker()
		return m, nil
	// The row under the tabs is a line of names with a current one, so the keys
	// that move along it are the ones that point along a line. `h`/`l` mean
	// depth on this board and `[`/`]` page, so neither was free.
	case "<":
		return m, m.stepThread(-1)
	case ">":
		return m, m.stepThread(1)
	case "/":
		m.searching, m.queryDraft = true, m.query
		return m, nil
	case "esc":
		// Clearing a filter puts the whole thread back, and the end of it is
		// where the reader wants to be — the same place `1` goes.
		if m.query != "" {
			m.query, m.scroll, m.threadFollow = "", 0, true
		}
		return m, nil
	case "tab":
		m.stepTab(1)
		return m, nil
	case "shift+tab":
		m.stepTab(-1)
		return m, nil
	case "1":
		// Already here: the threads key means "jump back to live", which is what
		// a reader who scrolled up into the history wants next.
		m.threadFollow = true
		return m, nil
	case "2", "3", "4":
		m.selectTab(tabOrder[int(msg.String()[0]-'1')])
		return m, nil
	case "h", "left":
		// Horizontal keys mean depth, and there is nothing under a message.
		return m, nil
	case "j", "down", "ctrl+d", "pgdown":
		m.scrollBy(1)
		return m, nil
	case "k", "up", "ctrl+u", "pgup":
		m.scrollUp(1)
		return m, nil
	case "]", "ctrl+f":
		m.scrollBy(m.pageSize())
		return m, nil
	case "[", "ctrl+b":
		m.scrollUp(m.pageSize())
		return m, nil
	case "r":
		return m, m.load()
	}
	return m, nil
}

// visibleThread is the thread after the active search.
func (m *Model) visibleThread() []store.ThreadMessage {
	if m.query == "" {
		return m.thread
	}
	var out []store.ThreadMessage
	for _, msg := range m.thread {
		if matchesThreadMessage(msg, m.query) {
			out = append(out, msg)
		}
	}
	return out
}

// matchesThreadMessage reports whether a message matches the query, searching
// every field a person would think to type.
func matchesThreadMessage(msg store.ThreadMessage, query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	for _, h := range []string{
		strings.ToLower(string(msg.Kind)),
		strings.ToLower(msg.By.Name),
		strings.ToLower(msg.By.JobID),
		strings.ToLower(msg.By.RunID),
		strings.ToLower(msg.By.PID),
		strings.ToLower(msg.Resource),
		strings.ToLower(msg.Body),
	} {
		if strings.Contains(h, q) {
			return true
		}
	}
	return false
}

// renderThreadBody draws the conversation itself, and nothing else.
//
// Which thread this is and what is currently held are drawn by the pane as
// pinned chrome rather than from here: both answer "where am I and what is true
// right now", and an answer that scrolls off the top while the reader is
// reading history is no answer at all.
func (m *Model) renderThreadBody() string {
	var b strings.Builder
	now := time.Now()

	msgs := m.visibleThread()
	if len(msgs) == 0 {
		if m.query != "" {
			b.WriteString(dimStyle.Render("  nothing in "+m.currentThread()+" matches "+m.query) + "\n")
		} else {
			b.WriteString(dimStyle.Render("  "+m.currentThread()+" is empty — "+
				"`bermuda thread event --thread "+m.currentThread()+" <text>` starts it") + "\n")
		}
		return b.String()
	}

	// The whole thread is rendered and the window trims it to the pane. Unlike
	// the two tables there is no cursor to page around, so the reader scrolls a
	// continuous log rather than stepping through pages of one.
	for _, msg := range msgs {
		b.WriteString(m.threadBubble(msg, now))
	}
	return b.String()
}

// threadBubble renders one message.
func (m *Model) threadBubble(msg store.ThreadMessage, now time.Time) string {
	indent := threadMargin
	border := threadKindStyle(msg.Kind)
	head := border
	if m.isHuman(msg.By) {
		indent, border, head = m.humanIndent(), humanStyle, humanStyle.Bold(true)
	}

	header := bubbleHeader(msg)
	stamp := threadWhen(msg.CreatedAt, now)
	textMax := m.bubbleTextMax(indent)
	lines := wrapText(threadSays(msg), textMax)

	// Every bubble takes the whole width it is allowed, rather than shrinking to
	// its content. A thread of boxes each sized to its own text has a ragged
	// right edge that reads as damage, and the eye re-measures the column on
	// every message; one edge down the whole thread is read once.
	return bubbleBox{
		header: header, footer: stamp, lines: lines,
		textW: textMax, maxW: textMax,
		border: border, head: head, indent: indent,
		// An @name in a body reached a live agent, so it is coloured: a reader
		// scrolling past can see that a message was addressed to somebody
		// without reading it.
		decorate: highlightMentions,
	}.render()
}

// bubbleTextMax is the text column every bubble at this indent uses — a ceiling
// that nothing shrinks back from, so it is also the width.
//
// It measures against the pane rather than contentWidth alone, because
// contentWidth has a floor of 24 that a genuinely tiny split is under. A table
// that overruns loses the end of a cell; a bubble that overruns wraps in the
// terminal and every border in the thread below it goes crooked.
func (m *Model) bubbleTextMax(indent int) int {
	avail := m.contentWidth()
	if m.width > 0 && m.width-1 < avail {
		avail = m.width - 1
	}
	// Two border columns and two padding spaces are not text.
	return max(min(avail-indent-4, threadBubbleMax), 2)
}

// humanIndent is how far a person's bubble sits from the left, shrunk on a
// narrow pane so the indent never costs more than the message.
func (m *Model) humanIndent() int {
	shift := threadHumanIndent
	if third := m.contentWidth() / 3; third < shift {
		shift = max(third, 0)
	}
	return threadMargin + shift
}

// bubbleHeader is the line set into the top border: who is speaking, what kind
// of thing they said, and what it was about.
func bubbleHeader(msg store.ThreadMessage) string {
	parts := []string{msg.By.String(), string(msg.Kind)}
	if msg.Resource != "" {
		parts = append(parts, msg.Resource)
	}
	return strings.Join(parts, " · ")
}

// isHuman reports whether a message was written by a person at this board.
//
// The store has no such flag, and inventing one would be a lie: an agent can
// pass `--as handler` at any time. What the board can honestly say is that the
// author is the name it posts under itself, with no job or run behind it — a
// name typed by whoever is sitting here.
func (m *Model) isHuman(id store.Identity) bool {
	if id.JobID != "" || id.RunID != "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(id.Name), m.composeAs())
}

// holdColumns is the pinned claims block. It is a table like the others so its
// cells are fitted before they are styled: a hand-padded line carrying escape
// sequences cannot be measured, and would run past the edge of a narrow pane.
func holdColumns() []column {
	return []column{
		{title: "RESOURCE", width: 14},
		{title: "HOLDER", width: 18},
		{title: "EXPIRES", width: 10},
		{title: "HELD", width: 6},
		{title: "WHY", flex: true, width: 20, max: 60},
	}
}

// renderHolds pins what is currently held above the thread.
//
// This is the answer to the question the thread exists for — who has the
// browser, and for how much longer — so it sits above the log rather than
// inside it.
//
// It is the same block above every thread. There is one browser on this machine
// whichever conversation is discussing it, so holds do not divide by thread;
// a per-thread holds block would let two agents in two threads each read that
// the browser was free.
func (m *Model) renderHolds(now time.Time) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("  HOLDS") + "\n")
	if len(m.claims) == 0 {
		b.WriteString(dimStyle.Render("  nothing is claimed") + "\n\n")
		return b.String()
	}
	widths := layout(holdColumns(), m.contentWidth())
	shown := m.claims
	// The block is pinned, so every row it takes is a row the conversation does
	// not get. A machine holding a dozen leases must not push the thread off the
	// pane entirely — the count of what is hidden is enough to send the reader
	// to `bermuda thread holds` for the rest.
	if budget := m.holdsBudget(); len(shown) > budget {
		shown = shown[:budget]
	}
	for _, c := range shown {
		// The holder is rendered with its pid. Two interactive agents can share
		// a name, and this block exists to tell a blocked reader who to go and
		// ask — `ada` alone does not answer that when three are running.
		cells := []string{c.Resource, c.Holder.Short(), c.ExpiryLabel(now),
			store.ShortDuration(now.Sub(c.Since)), c.Why}
		sized := make([]string, len(cells))
		for k, cell := range cells {
			sized[k] = fitMarquee(cell, widths[k], m.frame)
		}
		sized[0] = selectedStyle.Render(sized[0])
		// The expiry is what a reader is really checking, so it is coloured by
		// how much lease is left: about to lapse, healthy, and never lapsing are
		// three different situations.
		sized[2] = expiryStyle(c, now).Render(sized[2])
		sized[3] = dimStyle.Render(sized[3])
		sized[4] = dimStyle.Render(sized[4])
		b.WriteString("  " + strings.Join(sized, " ") + "\n")
	}
	if hidden := len(m.claims) - len(shown); hidden > 0 {
		b.WriteString(dimStyle.Render("  … "+itoa(hidden)+
			" more held — `bermuda thread holds`") + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// holdsBudget is how many held resources the pinned block will list.
//
// A third of the pane: enough that the usual one or two leases are simply
// there, and little enough that the block can never be the reason there is no
// conversation on screen.
func (m *Model) holdsBudget() int {
	return max(m.paneHeight()/3, 1)
}

// expiryStyle colours a lease by how much of it is left.
func expiryStyle(c store.Claim, now time.Time) lipgloss.Style {
	switch {
	case c.ExpiresAt == nil:
		// A lease that never lapses is the condition that outlived a killed
		// agent, so it is flagged rather than shown as healthy.
		return outcomeStyles["parked"]
	case c.Remaining(now) < time.Minute:
		return outcomeStyles["failed"]
	default:
		return outcomeStyles["running"]
	}
}

// threadWhen renders a timestamp the way a thread reads it: the clock for today,
// and the date as well for anything older, since "14:02" alone does not say
// which afternoon.
func threadWhen(t, now time.Time) string {
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// threadSays folds a claim's lease into its line, so the thread answers "for how
// long" without anyone opening another view.
func threadSays(msg store.ThreadMessage) string {
	if msg.Kind != store.KindClaim {
		return msg.Body
	}
	lease := "no expiry"
	if ttl := msg.TTL(); ttl > 0 {
		lease = "ttl " + store.ShortDuration(ttl)
	}
	if msg.Body == "" {
		return lease
	}
	return msg.Body + " (" + lease + ")"
}
