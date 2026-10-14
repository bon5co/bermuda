package board

import "strings"

// The pane is chrome plus a body.
//
// Everything used to be rendered as one string and then windowed, which meant
// scrolling up in a thread carried the tab bar off the top of the pane: the
// reader lost the one line saying which list they were in, and the brand line
// saying whether the scheduler was even alive, at exactly the moment they were
// digging through history. Chrome that scrolls away is not chrome.
//
// So a view is now three parts. The top says where you are, the bottom says
// what just happened and which keys do what, and only the middle moves. The
// arithmetic runs in one place because the invariant is one invariant: the
// board must never render more rows than the pane has. A view one row too tall
// makes the terminal scroll, which smears the whole board upwards and leaves a
// trail of half-drawn frames behind it.

// pane is a view split into the parts that stay put and the part that scrolls.
type pane struct {
	// top is pinned above the body: the brand line, the tabs, and on the thread
	// the row of thread names and the holds block.
	top string
	// body is the only part the window trims — the list, the conversation, the
	// picker.
	body string
	// bottom is pinned under the body: the error or status line, the search
	// line, the compose box, and the help.
	bottom string
	// lines, when set, stands in for body: a body too long to build whole,
	// asked only for the rows the window is about to show. See bodyLines.
	lines *bodyLines
}

// bodyLines is a body known by its length and drawn a slice at a time.
//
// A card feed is the case it exists for. A.LOG only ever grows, and building
// every card in it to show the twenty rows that fit cost a board left open in
// a split most of a core: the render ran on every frame, for cards nobody
// could see. With this the window asks for the rows it will show, so what a
// frame costs depends on the pane, not on how long the history has become.
type bodyLines struct {
	// total is how many rows the whole body would be.
	total int
	// slice returns rows [from, to) of that body.
	slice func(from, to int) []string
}

// renderPane stacks the chrome around the body and trims the body to the rows
// the chrome left it.
func (m *Model) renderPane(p pane) string {
	height := m.paneHeight()
	top, bottom := blockLines(p.top), blockLines(p.bottom)

	// The body gets the leftovers, and never fewer than one row: a pane so
	// short that the chrome alone fills it is still a pane the reader is
	// looking at, and showing them a sliver of the conversation is better than
	// showing them none of it.
	avail := height - len(top) - len(bottom)
	if avail < 1 {
		avail = 1
	}
	out := append([]string{}, top...)
	var body string
	if p.lines != nil {
		body = m.windowLines(*p.lines, avail)
	} else {
		body = m.windowBody(p.body, avail)
	}
	// Where the body ended up, for the mouse: the chrome above it is the offset,
	// and the scroll the window just settled on is what the first visible line
	// is. Recorded after the window has run, because that is when the second of
	// those is known.
	m.recordWindow(len(top), m.scroll)
	out = append(out, blockLines(body)...)
	out = append(out, bottom...)

	// The last defence, for the pane too short to hold even the chrome. What is
	// dropped is taken from the bottom, so the help goes before the tab bar
	// does: a reader who cannot see the help can still guess at keys, and a
	// reader who cannot see where they are cannot guess at anything.
	if len(out) > height {
		out = out[:height]
	}
	return strings.Join(out, "\n")
}

// paneHeight is how many rows the board has to fill, falling back to a sane
// terminal before the first WindowSizeMsg arrives.
func (m *Model) paneHeight() int {
	if m.height > 0 {
		return m.height
	}
	return defaultHeight
}

// blockLines splits a rendered block into rows.
//
// Blocks are written with a trailing newline — they are built to be
// concatenated — and splitting one of those naively yields a phantom empty row
// at the end. Counted as chrome, that phantom steals a row from the body on
// every render.
func blockLines(block string) []string {
	if block == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(block, "\n"), "\n")
}

// blockRows is how many rows a block takes.
func blockRows(block string) int {
	return len(blockLines(block))
}

// text is the whole body, however it was given: the string, or every row of a
// body drawn on demand. Building all of it is what the window exists to avoid,
// so this is for asking what a pane holds, not for drawing it.
func (p pane) text() string {
	if p.lines != nil {
		return strings.Join(p.lines.slice(0, p.lines.total), "\n")
	}
	return p.body
}
