// Package index keeps a searchable copy of a vault: the same notes the memory
// layer anchors, in the one other format a question can be asked of them.
//
// Grep answers "which note contains this word". It cannot answer "which note
// decided this", because the note that decided it used different words. That
// second question is the whole reason this package exists, and a vector index
// is the cheapest thing that answers it. Nothing here rewrites, summarises or
// interprets a note: a chunk is a paragraph of the file, verbatim, with the
// headings above it prepended so the paragraph still knows where it came from.
//
// The store is Chroma, run embedded — a directory, not a server. See helper.py
// and python.go for how a Go binary drives it without one.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// Chunking sizes, in runes.
//
// A paragraph is the floor, deliberately: sentence-level chunks retrieve
// fragments that read as confident and mean nothing without the sentence
// before them. But a vault is full of one-line paragraphs — a list item, a
// heading's single sentence — and embedding those one at a time produces
// vectors with no content in them, so consecutive paragraphs under the same
// heading are merged until there is enough text to mean something.
const (
	// minChunk is the size a merged run has to reach before it is flushed.
	minChunk = 240
	// maxChunk is the point at which a run stops accepting more paragraphs.
	maxChunk = 1500
	// hardCap is the overflow valve for a single paragraph longer than any
	// reasonable chunk — a wall-of-text note, a pasted transcript. It is split
	// on sentence boundaries only because the alternative is not indexing it.
	hardCap = 2400
)

// Chunk is one indexable piece of a note.
type Chunk struct {
	// Ord is the chunk's position in its file, and the half of the id that
	// makes it unique. The other half is the path.
	Ord int
	// Heading is the heading path above this text, joined with " > ". Empty
	// for text before the first heading.
	Heading string
	// Text is the paragraph run, verbatim.
	Text string
}

// ID is the chunk's key in the collection: the file path, then the ordinal.
//
// The path is the unique key for a *file*, so everything belonging to a file
// can be deleted by path alone without knowing how many chunks it had last
// time — which is what makes a reindex a delete-then-insert instead of a diff.
func (c Chunk) ID(path string) string {
	return path + "#" + itoa(c.Ord)
}

// Embedded is what actually gets vectorised: the note's title and the heading
// path, then the text.
//
// The prefix is not decoration. A paragraph reading "it never fired, so it was
// removed" is unretrievable on its own; the same paragraph under
// "browserd > Retirement" is retrievable by every question that names either.
func (c Chunk) Embedded(title string) string {
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
		b.WriteString("\n")
	}
	if c.Heading != "" {
		b.WriteString(c.Heading)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(c.Text)
	return b.String()
}

// Note is a parsed markdown file: its front matter, and its body as chunks.
type Note struct {
	// Title is the front matter's name if it has one, else the file's base
	// name. It is what a search result is labelled with.
	Title string
	// Description is the front matter's description, kept because a memory
	// note's description is the one line written to be read on its own.
	Description string
	// Type is the front matter's metadata.type — user, feedback, project,
	// reference — for the notes that carry one.
	Type string
	// Chunks is the body, paragraph runs in file order.
	Chunks []Chunk
}

// Parse splits a markdown file into front matter and paragraph chunks.
//
// Fenced code blocks survive whole: a blank line inside a fence is not a
// paragraph break, and splitting on it produced chunks that were half a shell
// command. Headings are not chunks of their own — a heading with nothing under
// it is a table of contents entry, not a fact — but they accumulate into the
// heading path carried by every chunk below them.
func Parse(name, body string) Note {
	n := Note{Title: name}
	fm, rest := splitFrontMatter(body)
	if fm != "" {
		n.Title, n.Description, n.Type = readFrontMatter(fm, name)
	}

	var (
		heads   []string // one entry per heading level in force
		run     []string // paragraph run being accumulated
		runSize int
		cur     string // heading path for the run being accumulated
	)
	flush := func() {
		if len(run) == 0 {
			return
		}
		text := strings.Join(run, "\n\n")
		run, runSize = nil, 0
		for _, piece := range overflow(text) {
			n.Chunks = append(n.Chunks, Chunk{Ord: len(n.Chunks), Heading: cur, Text: piece})
		}
	}

	for _, blk := range blocks(rest) {
		if lvl, title, ok := heading(blk); ok {
			// A heading closes whatever ran above it: text under two
			// different headings is two different subjects, however short
			// each one is.
			flush()
			heads = pushHeading(heads, lvl, title)
			cur = joinHeads(heads)
			continue
		}
		if len(run) == 0 {
			cur = joinHeads(heads)
		}
		run = append(run, blk)
		runSize += utf8.RuneCountInString(blk)
		if runSize >= minChunk {
			flush()
		}
	}
	flush()
	return n
}

// blocks splits a body into blank-line-separated blocks, keeping fenced code
// whole and dropping blocks with nothing in them.
func blocks(body string) []string {
	var (
		out    []string
		cur    []string
		fence  string
		inCode bool
	)
	end := func() {
		if len(cur) == 0 {
			return
		}
		if s := strings.TrimRight(strings.Join(cur, "\n"), "\n"); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
		cur = nil
	}
	for _, line := range strings.Split(body, "\n") {
		if f, ok := fenceMark(line); ok {
			switch {
			case !inCode:
				inCode, fence = true, f
			case strings.HasPrefix(f, fence):
				inCode = false
			}
			cur = append(cur, line)
			continue
		}
		if !inCode && strings.TrimSpace(line) == "" {
			end()
			continue
		}
		cur = append(cur, line)
	}
	end()
	return out
}

// fenceMark reports whether a line opens or closes a code fence.
func fenceMark(line string) (string, bool) {
	t := strings.TrimSpace(line)
	for _, mark := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, mark) {
			return mark, true
		}
	}
	return "", false
}

// heading reads an ATX heading, returning its level and text.
func heading(blk string) (int, string, bool) {
	// Only a single-line block can be a heading: "# Title\ntext" is a heading
	// and a paragraph that were never separated by a blank line, and treating
	// the pair as a heading would throw the paragraph away.
	if strings.Contains(blk, "\n") {
		return 0, "", false
	}
	t := strings.TrimSpace(blk)
	lvl := 0
	for lvl < len(t) && t[lvl] == '#' {
		lvl++
	}
	if lvl == 0 || lvl > 6 || lvl >= len(t) || t[lvl] != ' ' {
		return 0, "", false
	}
	return lvl, strings.TrimSpace(t[lvl:]), true
}

// pushHeading sets the heading at one level and drops everything below it.
//
// Holes are kept, not squeezed out: h1 followed by h3 leaves level 2 empty,
// and a later h2 has to land on the rung it belongs to. joinHeads is what
// hides the hole from the reader.
func pushHeading(heads []string, lvl int, title string) []string {
	for len(heads) < lvl {
		heads = append(heads, "")
	}
	heads = heads[:lvl]
	heads[lvl-1] = title
	return heads
}

// joinHeads renders the heading path, skipping levels a note never used.
func joinHeads(heads []string) string {
	var out []string
	for _, h := range heads {
		if h != "" {
			out = append(out, h)
		}
	}
	return strings.Join(out, " > ")
}

// overflow splits a run that is longer than any paragraph should be.
//
// Sentence boundaries, and only when the run is genuinely oversized: this is
// the one place the paragraph floor is broken, and it exists so a 12,000-rune
// note is indexed badly rather than not at all.
func overflow(text string) []string {
	if utf8.RuneCountInString(text) <= hardCap {
		return []string{text}
	}
	var (
		out  []string
		cur  strings.Builder
		size int
	)
	for _, s := range sentences(text) {
		n := utf8.RuneCountInString(s)
		if size > 0 && size+n > maxChunk {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			size = 0
		}
		cur.WriteString(s)
		size += n
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// sentences splits on terminal punctuation, keeping the punctuation.
func sentences(text string) []string {
	var (
		out []string
		cur strings.Builder
	)
	for _, r := range text {
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// splitFrontMatter separates a leading --- block from the body.
func splitFrontMatter(body string) (string, string) {
	s := strings.TrimLeft(body, "\ufeff")
	if !strings.HasPrefix(s, "---\n") {
		return "", body
	}
	rest := s[len("---\n"):]
	i := strings.Index(rest, "\n---")
	if i < 0 {
		// An unterminated fence is not front matter, it is a note that starts
		// with a horizontal rule. Indexing it as body loses nothing.
		return "", body
	}
	end := i + len("\n---")
	for end < len(rest) && rest[end] != '\n' {
		end++
	}
	return rest[:i], strings.TrimPrefix(rest[end:], "\n")
}

// readFrontMatter picks the three fields a search result is labelled with.
//
// This is not a YAML parser and must never become one. It reads name,
// description and metadata.type because those are the fields the memory format
// guarantees; anything else in the front matter is left in the file, where the
// person who wrote it can read it.
func readFrontMatter(fm, fallback string) (title, desc, typ string) {
	title = fallback
	inMeta := false
	for _, line := range strings.Split(fm, "\n") {
		indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if !indented {
			inMeta = key == "metadata" && val == ""
		}
		switch key {
		case "name":
			if val != "" {
				title = val
			}
		case "description":
			if val != "" {
				desc = val
			}
		case "type":
			if val != "" && (inMeta || !indented) {
				typ = val
			}
		}
	}
	return title, desc, typ
}

// Digest is the content hash a stale check compares.
//
// SHA-256 rather than MD5: the cost difference is invisible at vault sizes and
// the failure mode of a collision is a note that silently never reindexes.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// itoa avoids pulling strconv into a file that needs one integer formatted.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
