package board

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/internal/version"
)

// Status dot colours: green when the board is functioning, red when something
// stops it working.
var (
	statusOK  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	statusBad = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// renderBrand draws the board name and a health dot.
//
// The dot answers "is this thing working" before the reader looks at anything
// else, and states what is wrong beside it — a red dot with no reason would
// only prompt the question it was meant to answer.
func (m *Model) renderBrand() string {
	// The dot leads, so health is the first thing read. The version follows the
	// name: it identifies what is running, which belongs with the name.
	brand := titleStyle.Render("Bermuda") + " " + dimStyle.Render(version.String())
	if problem := m.health(); problem != "" {
		return statusBad.Render("●") + " " + brand + " " + statusBad.Render(problem)
	}
	return statusOK.Render("●") + " " + brand
}

// health returns what is stopping the board working, or "" when all is well.
//
// The scheduler is checked first: without it nothing on screen will ever fire,
// which is a more serious condition than a failed read.
func (m *Model) health() string {
	if !m.daemonUp {
		return "scheduler stopped"
	}
	if m.err != nil {
		return "store error"
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// tabOrder is the tabs left to right as they are drawn, and tabLabels names
// them in the same order.
//
// Threads first: it is the tab that is read, where the others are checked.
// Tab walks this list and the number keys count it, rather than following the
// order the focus constants happen to be declared in — a tab appended to that
// enum would otherwise be reached by pressing a number pointing somewhere else,
// and there is nothing on screen a reader could use to notice.
var (
	tabOrder  = []focus{focusThread, focusJobs, focusRuns, focusFlows}
	tabLabels = []string{"THREADS", "JOBS", "RUNS", "FLOWS"}
)

// tabIndex is where the current focus sits in the drawn order.
func (m *Model) tabIndex() int {
	for i, f := range tabOrder {
		if f == m.focus {
			return i
		}
	}
	return 0
}

// selectTab moves to one, dropping everything that described a position in the
// list being left: a cursor index means nothing in a different list, and a
// scroll offset into a conversation means less still.
func (m *Model) selectTab(f focus) {
	m.focus, m.cursor, m.scroll = f, 0, 0
	// The thread is always entered at its live end, wherever it was left.
	m.threadFollow = true
}

// stepTab walks the drawn order, wrapping at both ends so shift+tab from the
// leftmost tab lands on the rightmost rather than doing nothing.
func (m *Model) stepTab(delta int) {
	n := len(tabOrder)
	m.selectTab(tabOrder[((m.tabIndex()+delta)%n+n)%n])
}

// renderTabs draws the list selector as overlapping folder tabs.
//
// Adjacent folders share a single edge rather than each drawing their own, so
// they read as files overlapping in a drawer instead of separate boxes set
// side by side. The active folder's bottom edge is open, joining it to the
// table below; the others are closed off by the rule passing under them.
//
// The frame is deliberately one colour. Colouring each folder separately made
// the shared edges ambiguous — a ┬ or ┴ belongs to two tabs at once, so
// whichever hue it took bled across the join and made the wrong tab look lit.
func (m *Model) renderTabs(suffix string) string {
	labels := tabLabels
	active := m.tabIndex()

	var top, mid, bot strings.Builder
	for i, label := range labels {
		body := strings.Repeat("─", len(label)+2)

		if i == 0 {
			top.WriteString("╭")
			mid.WriteString(dimStyle.Render("│"))
			if active == 0 {
				bot.WriteString("┘")
			} else {
				bot.WriteString("┴")
			}
		}

		top.WriteString(body)
		text := dimStyle
		if i == active {
			text = selectedStyle
		}
		mid.WriteString(text.Render(" " + label + " "))
		if i == active {
			bot.WriteString(strings.Repeat(" ", len(label)+2))
		} else {
			bot.WriteString(body)
		}

		switch {
		case i == len(labels)-1:
			top.WriteString("╮")
			mid.WriteString(dimStyle.Render("│"))
			if i == active {
				bot.WriteString("└")
			} else {
				bot.WriteString("┴")
			}
		default:
			// One edge between two folders, not two.
			top.WriteString("┬")
			mid.WriteString(dimStyle.Render("│"))
			// The join carries a wall upwards either way; what changes is
			// whether the rule runs into it from each side. Two closed folders
			// side by side keep the rule continuous, which a third tab is the
			// first arrangement to require: with only two, one of any pair was
			// always the open one.
			switch {
			case i == active:
				bot.WriteString("└")
			case i+1 == active:
				bot.WriteString("┘")
			default:
				bot.WriteString("┴")
			}
		}
	}

	line := bot.String()
	if pad := m.tableWidth() - lipgloss.Width(line); pad > 0 {
		line += strings.Repeat("─", pad)
	}
	// The middle row is assembled already styled — its frame runes are dimmed as
	// they are written, so the labels keep their own colour.
	out := dimStyle.Render(top.String()) + "\n" +
		mid.String() + "\n" +
		dimStyle.Render(line)
	if suffix != "" {
		out += " " + suffix
	}
	return out
}
