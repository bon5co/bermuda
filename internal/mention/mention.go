// Package mention finds @names in a thread message and reaches the agents they
// name.
//
// The thread is durable and passive: an agent learns what is in it only when it
// next runs a `bermuda thread log`. That is the right default for a record, and
// the wrong one for a question — an agent sitting idle in a pane will never
// look, so a message addressed to it is read hours later or not at all. A
// mention is the one case where the thread has to push rather than wait.
//
// Delivery is best effort and is never allowed to fail a post. The thread is
// the record; reaching somebody is a courtesy on top of it. Mentioning an agent
// that has since exited is the normal case, not an error: agents are ephemeral
// and the names of dead ones stay in the log forever.
package mention

import "strings"

// All is the name that means every live agent except whoever is speaking.
const All = "all"

// Mention is one @name in a body, with where it sits so a renderer can colour
// exactly the text that was written.
type Mention struct {
	// Name is the mentioned text without the @, exactly as it was typed. It is
	// matched case-insensitively later; it is kept verbatim here so a report can
	// quote what the author actually wrote.
	Name string
	// Start and End are byte offsets into the body, covering the @ as well.
	Start, End int
}

// Find returns every mention in the body, in order, including repeats.
//
// The two hard cases are both about what is *not* a mention. An email address
// must not parse as one — `dev@example.com` would otherwise try to reach an
// agent called `example.com` on every message that quotes an address — and a
// mention at the end of a sentence must not swallow the punctuation, because
// `@ada.` would then match nothing and read as an agent that has exited.
func Find(body string) []Mention {
	var out []Mention
	prev := rune(0)
	// The loop walks every rune, including the ones inside a name it has just
	// taken, so prev is always the character immediately to the left.
	for i, r := range body {
		if r == '@' && (prev == 0 || !isLocalPart(prev)) {
			// An @ glued to the end of a word is an address, not an address-to.
			// Everything legal in an email local part counts as glue here.
			if name := scanName(body[i+1:]); name != "" {
				out = append(out, Mention{Name: name, Start: i, End: i + 1 + len(name)})
			}
		}
		prev = r
	}
	return out
}

// Names is every distinct name mentioned, in the order they first appear.
//
// Distinct is case-insensitive, because resolution is: `@dotfiles` and
// `@dotfiles` are one agent, and delivering twice would make it answer twice.
func Names(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range Find(body) {
		key := strings.ToLower(m.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m.Name)
	}
	return out
}

// scanName reads the name after an @, and stops where a word does.
//
// Dots and hyphens are legal inside a name — a working directory is a plausible
// mention and `agent-main` is a plausible agent name — but a trailing one is
// punctuation. `@ada.` ends a sentence; `@mira.local` does not.
func scanName(s string) string {
	end := 0
	for i, r := range s {
		if !isNameRune(r) {
			break
		}
		end = i + len(string(r))
	}
	return strings.TrimRight(s[:end], ".-_")
}

func isNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '_', r == '.':
		return true
	}
	return false
}

// isLocalPart reports whether r can appear immediately before the @ of an email
// address.
func isLocalPart(r rune) bool {
	if isNameRune(r) {
		return true
	}
	return r == '+' || r == '%'
}
