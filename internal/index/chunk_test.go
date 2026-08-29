package index

import (
	"strings"
	"testing"
)

const note = `---
name: browserd-bx
description: browserd is retired; browser-use is the only browser tool
metadata:
  type: reference
---

browserd was replaced.

# Why

It launched its own browser.

Sessions lived in the wrong profile.

## The fix

Attach over CDP instead.

` + "```bash\nbrowser-use skill\n\nsystemctl --user restart chromium-cdp\n```" + `
`

func TestParseReadsTheFrontMatterFieldsAResultIsLabelledWith(t *testing.T) {
	n := Parse("fallback", note)
	if n.Title != "browserd-bx" {
		t.Errorf("Title = %q, want the front matter name", n.Title)
	}
	if !strings.HasPrefix(n.Description, "browserd is retired") {
		t.Errorf("Description = %q", n.Description)
	}
	if n.Type != "reference" {
		t.Errorf("Type = %q, want the nested metadata.type", n.Type)
	}
	for _, c := range n.Chunks {
		if strings.Contains(c.Text, "metadata:") {
			t.Fatalf("front matter leaked into a chunk: %q", c.Text)
		}
	}
}

// A note whose front matter is missing keeps the name it was given, which is
// the filename — every non-memory note in a vault is in this shape.
func TestParseFallsBackToTheGivenTitle(t *testing.T) {
	n := Parse("Raphael Log", "# Monday\n\nSomething happened.\n")
	if n.Title != "Raphael Log" {
		t.Errorf("Title = %q, want the fallback", n.Title)
	}
}

func TestParseCarriesTheHeadingPathOntoEveryChunk(t *testing.T) {
	n := Parse("n", note)
	var deep *Chunk
	for i := range n.Chunks {
		if strings.Contains(n.Chunks[i].Text, "Attach over CDP") {
			deep = &n.Chunks[i]
		}
	}
	if deep == nil {
		t.Fatal("the paragraph under the nested heading was not chunked")
	}
	if deep.Heading != "Why > The fix" {
		t.Errorf("Heading = %q, want the full path", deep.Heading)
	}
}

// The paragraph on its own says nothing retrievable; the heading above it is
// what makes it findable, so it has to be in the embedded text.
func TestEmbeddedCarriesTitleAndHeadingAheadOfTheText(t *testing.T) {
	c := Chunk{Ord: 3, Heading: "Why > The fix", Text: "Attach over CDP instead."}
	got := c.Embedded("browserd-bx")
	if !strings.HasPrefix(got, "browserd-bx\nWhy > The fix\n") {
		t.Errorf("Embedded = %q, want the title and heading first", got)
	}
	if !strings.HasSuffix(got, "Attach over CDP instead.") {
		t.Errorf("Embedded = %q, want the text last", got)
	}
}

func TestChunkIDIsThePathAndTheOrdinal(t *testing.T) {
	if got := (Chunk{Ord: 12}).ID("memory/goals.md"); got != "memory/goals.md#12" {
		t.Errorf("ID = %q", got)
	}
}

// A blank line inside a fence is not a paragraph break. Splitting on it
// produced chunks that were half a shell command.
func TestParseKeepsAFencedCodeBlockWhole(t *testing.T) {
	n := Parse("n", note)
	var found bool
	for _, c := range n.Chunks {
		if strings.Contains(c.Text, "browser-use skill") {
			found = true
			if !strings.Contains(c.Text, "chromium-cdp") {
				t.Errorf("the fence was split in half: %q", c.Text)
			}
		}
	}
	if !found {
		t.Fatal("the code block was not indexed at all")
	}
}

// Consecutive one-line paragraphs are merged: embedding "It launched its own
// browser." alone produces a vector with nothing in it.
func TestParseMergesShortParagraphsUnderTheSameHeading(t *testing.T) {
	body := "# H\n\nOne short line.\n\nAnother short line.\n\nA third short line.\n"
	n := Parse("n", body)
	if len(n.Chunks) != 1 {
		t.Fatalf("got %d chunks, want the short lines merged into one: %#v", len(n.Chunks), n.Chunks)
	}
	if !strings.Contains(n.Chunks[0].Text, "third") {
		t.Errorf("merge dropped a paragraph: %q", n.Chunks[0].Text)
	}
}

// Merging must not cross a heading: two headings are two subjects however
// short each one is.
func TestParseNeverMergesAcrossAHeading(t *testing.T) {
	body := "# A\n\nShort one.\n\n# B\n\nShort two.\n"
	n := Parse("n", body)
	if len(n.Chunks) != 2 {
		t.Fatalf("got %d chunks, want one per heading: %#v", len(n.Chunks), n.Chunks)
	}
	if n.Chunks[0].Heading != "A" || n.Chunks[1].Heading != "B" {
		t.Errorf("headings = %q, %q", n.Chunks[0].Heading, n.Chunks[1].Heading)
	}
}

// The paragraph is the floor. A long one stays whole rather than being cut
// into sentences that read as confident and mean nothing alone.
func TestParseDoesNotSplitAParagraphThatIsMerelyLong(t *testing.T) {
	long := strings.Repeat("a sentence that carries real content. ", 40) // ~1500 runes
	n := Parse("n", long)
	if len(n.Chunks) != 1 {
		t.Fatalf("a %d-rune paragraph was split into %d chunks", len(long), len(n.Chunks))
	}
}

// ...but a paragraph past the hard cap is split rather than dropped.
func TestParseSplitsAParagraphPastTheHardCap(t *testing.T) {
	huge := strings.Repeat("a sentence that carries real content. ", 200)
	n := Parse("n", huge)
	if len(n.Chunks) < 2 {
		t.Fatalf("a %d-rune paragraph was not split at all", len(huge))
	}
	for i, c := range n.Chunks {
		if c.Ord != i {
			t.Errorf("chunk %d has Ord %d", i, c.Ord)
		}
	}
}

// A heading with nothing under it is a table of contents entry, not a fact.
func TestParseDoesNotIndexHeadingsAsChunks(t *testing.T) {
	n := Parse("n", "# Only a heading\n\n## And another\n")
	if len(n.Chunks) != 0 {
		t.Errorf("got %d chunks from headings alone: %#v", len(n.Chunks), n.Chunks)
	}
}

// An h1 followed by an h3 leaves level 2 empty; the path must not render the
// hole, and a later h2 must still land on its own rung.
func TestParseRendersAHeadingPathWithSkippedLevels(t *testing.T) {
	body := "# Top\n\n### Deep\n\nSomething worth a sentence or two about it here.\n"
	n := Parse("n", body)
	if len(n.Chunks) != 1 {
		t.Fatalf("got %d chunks", len(n.Chunks))
	}
	if n.Chunks[0].Heading != "Top > Deep" {
		t.Errorf("Heading = %q, want no empty rung", n.Chunks[0].Heading)
	}
}

func TestDigestIsStableAndContentAddressed(t *testing.T) {
	a, b := Digest([]byte("one")), Digest([]byte("one"))
	if a != b {
		t.Fatal("the same bytes hashed differently")
	}
	if len(a) != 64 {
		t.Errorf("digest is %d chars, want a sha256 hex string", len(a))
	}
	if Digest([]byte("two")) == a {
		t.Error("different bytes hashed the same")
	}
}
