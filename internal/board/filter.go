package board

import (
	"strings"

	"github.com/bon5co/bermuda/internal/store"
)

// Search filters both lists from a single query, and paging keeps a long list
// navigable in a short pane. They are one file because they compose: paging
// always applies to the *filtered* list, so searching narrows the pages rather
// than leaving the reader on a page that no longer exists.

// matchesJob reports whether a job matches the query.
//
// It searches every field a person would think to type: the id, the display
// name, the tags, and the description. Matching is case-insensitive and
// substring-based, because a search box that demands exact prefixes is a
// search box people stop using.
func matchesJob(j store.Job, query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	haystack := []string{
		strings.ToLower(j.ID),
		strings.ToLower(j.Name),
		strings.ToLower(j.Description),
		strings.ToLower(strings.Join(j.Tags, ",")),
		strings.ToLower(j.ScheduleLabel()),
	}
	for _, h := range haystack {
		if strings.Contains(h, q) {
			return true
		}
	}
	return false
}

// matchesRun reports whether a run matches the query.
func matchesRun(r store.Run, query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	haystack := []string{
		strings.ToLower(r.JobID),
		strings.ToLower(r.Outcome),
		strings.ToLower(r.ParkReason),
		strings.ToLower(r.Note),
		strings.ToLower(r.Trigger),
	}
	for _, h := range haystack {
		if strings.Contains(h, q) {
			return true
		}
	}
	return false
}

// visibleJobs is the job list after the active search, and after finished
// one-shots are put away.
func (m *Model) visibleJobs() []store.Job {
	var out []store.Job
	for _, j := range m.jobs {
		if !matchesJob(j, m.query) {
			continue
		}
		if !m.showFinished && m.jobFinished(j) {
			continue
		}
		out = append(out, j)
	}
	return out
}

// jobFinished says whether a job is a one-shot with nothing left to do,
// answered from the last-run map the board already holds.
func (m *Model) jobFinished(j store.Job) bool {
	if r, ok := m.last[j.ID]; ok {
		return j.Finished(&r)
	}
	return j.Finished(nil)
}

// hiddenFinished is how many jobs the finished rule is withholding right now.
//
// It is counted over the jobs the search kept, so the number describes the list
// on screen rather than the store: a job hidden by the query is already
// reported by the match count next to it, and counting it twice would say two
// different things are missing when one is.
func (m *Model) hiddenFinished() int {
	if m.showFinished {
		return 0
	}
	n := 0
	for _, j := range m.jobs {
		if matchesJob(j, m.query) && m.jobFinished(j) {
			n++
		}
	}
	return n
}

// visibleRuns is the run list after the active search.
func (m *Model) visibleRuns() []store.Run {
	if m.query == "" {
		return m.runs
	}
	var out []store.Run
	for _, r := range m.runs {
		if matchesRun(r, m.query) {
			out = append(out, r)
		}
	}
	return out
}

// chromeRows is the fixed part of what a list does not get: the brand line, the
// three tab rows, the column titles, the page counter, the help line, and the
// blank line above it. The footer is not fixed and is measured instead.
const chromeRows = 8

// pageRows is how many rows one page of a list holds.
//
// Only one list is on screen at a time, so it gets every row the chrome does
// not need. There is deliberately no upper bound: a tall pane should show a
// tall list, and capping it left two thirds of a 60-row pane empty while
// paginating a list that already fitted.
//
// The footer is measured rather than assumed: an error, a status, or an open
// search each add rows to the pinned chrome, and a page sized as though they
// had not would be windowed after all — putting a scroll hint under a list that
// is already paged, which is two answers to "is there more" at once.
func (m *Model) pageRows() int {
	rows := m.paneHeight() - chromeRows - blockRows(m.renderFooter())
	if m.flowInput != nil {
		// The open input box is pinned chrome too, and a page sized as though it
		// were not would push the help line off the bottom of the pane at the
		// moment the reader most needs to be told that esc cancels.
		rows -= blockRows(m.renderFlowInput())
	}
	if rows < 3 {
		rows = 3
	}
	return rows
}

// page returns the slice bounds for the page the cursor is on.
//
// The page is derived from the cursor rather than tracked separately, so
// moving the selection past the bottom of a page turns the page, and the two
// can never disagree about where the reader is.
func (m *Model) page(total int) (start, end, pageNum, pages int) {
	rows := m.pageRows()
	if total == 0 {
		return 0, 0, 1, 1
	}
	pages = (total + rows - 1) / rows
	pageNum = m.cursor/rows + 1
	if pageNum > pages {
		pageNum = pages
	}
	start = (pageNum - 1) * rows
	end = start + rows
	if end > total {
		end = total
	}
	return start, end, pageNum, pages
}

// pageLabel renders the page indicator, and says how much the search is
// hiding, so a filtered list never looks like the whole list.
//
// The indicator is always shown, even at 1/1: a counter that appears only once
// a list grows makes the header jump, and leaves the reader unsure whether its
// absence means one page or a missing feature.
func (m *Model) pageLabel(shown, total, pageNum, pages int) string {
	label := "page " + itoa(pageNum) + "/" + itoa(pages)
	if m.query != "" {
		if label != "" {
			label += " · "
		}
		label += itoa(shown) + " of " + itoa(total) + " match"
	}
	// A job that vanishes with no trace is indistinguishable from a job that was
	// deleted, so what the finished rule took away is counted here beside the
	// page indicator rather than left to be discovered.
	if m.focus == focusJobs {
		if n := m.hiddenFinished(); n > 0 {
			label += " · " + itoa(n) + " finished hidden (F shows)"
		}
	}
	return label
}

// movePage steps the cursor a whole page, which is what the paging keys do.
func (m *Model) movePage(delta int) {
	rows := m.pageRows()
	m.cursor += delta * rows
	m.clampCursor()
}
