package flow

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bon5co/bermuda/internal/store"
)

// The chain: A(x) then B(A's result).
//
// Two values travel down a flow and nothing else does. The input is what the
// caller supplied, and every step can see it; the previous result is what the
// step before published, and only the next step sees it.
//
// What deliberately does not travel is the previous step's *context*. The
// runner is explicit about this — a step cannot be handed the previous step's
// context under a different charter — because the agent that reviews something
// must not be the agent that wrote it. A published result is not that: it is
// the one sentence the author chose to hand on, where the context is every
// assumption it made getting there. Forwarding the first is a chain; forwarding
// the second is one agent in three costumes.

// Placeholders a step body may use.
const (
	PlaceholderInput    = "input"
	PlaceholderPrevious = "previous"
)

// Environment variables a run step sees. An agent step interpolates its prompt;
// a shell command has no prompt to interpolate, so it gets the same two values
// where a shell already looks for things.
const (
	EnvInput    = "BERMUDA_INPUT"
	EnvPrevious = "BERMUDA_PREVIOUS"
)

// placeholder matches {{input}} and {{ previous }} alike.
//
// Surrounding whitespace is tolerated because these files are hand-written, and
// `{{ input }}` failing silently — leaving the literal braces in the prompt an
// agent then reads — is a bug nobody would look for.
var placeholder = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// Values are what a running flow can substitute.
type Values struct {
	Input string
	// Previous is the note published by the step before this one, empty for the
	// first step.
	Previous string
}

// Env is the pair a run step is given.
func (v Values) Env() []string {
	return []string{EnvInput + "=" + v.Input, EnvPrevious + "=" + v.Previous}
}

// Expand substitutes the placeholders in a step's prompt.
//
// A run step is returned untouched: its command sees the same values through
// the environment, and rewriting a shell command's text would corrupt any
// command that legitimately contains braces.
func Expand(s store.Step, v Values) store.Step {
	if !s.IsAgent() {
		return s
	}
	s.Agent = placeholder.ReplaceAllStringFunc(s.Agent, func(match string) string {
		switch placeholder.FindStringSubmatch(match)[1] {
		case PlaceholderInput:
			return v.Input
		case PlaceholderPrevious:
			return v.Previous
		default:
			// Left as written. Validation refuses unknown placeholders before a
			// flow ever runs, so reaching here means somebody bypassed it — and
			// substituting an empty string would quietly change the prompt.
			return match
		}
	})
	return s
}

// Unknown returns the placeholders a flow uses that nothing will ever fill.
//
// Checked when the flow is read rather than when it runs. A `{{inpt}}` that is
// only discovered mid-flow has already spent an agent turn, and the prompt it
// produced contains a literal `{{inpt}}` that the agent will do its best to
// interpret — which is worse than not running at all.
func Unknown(f Flow) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range f.Steps {
		if !s.IsAgent() {
			continue
		}
		for _, m := range placeholder.FindAllStringSubmatch(s.Agent, -1) {
			name := m[1]
			switch name {
			case PlaceholderInput, PlaceholderPrevious:
				continue
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// NeedsInput reports whether any step actually references the input.
//
// A flow that declares an input and never uses it is not an error — a run step
// reads it from the environment, and this cannot see inside a shell command —
// so this only reports on what the prompts say.
func NeedsInput(f Flow) bool {
	for _, s := range f.Steps {
		if !s.IsAgent() {
			continue
		}
		for _, m := range placeholder.FindAllStringSubmatch(s.Agent, -1) {
			if m[1] == PlaceholderInput {
				return true
			}
		}
	}
	return false
}

// CheckPlaceholders refuses a flow whose prompts reference something that will
// never be filled.
func CheckPlaceholders(f Flow) error {
	if bad := Unknown(f); len(bad) > 0 {
		return fmt.Errorf("%s uses {{%s}}, which is not a thing a flow can substitute: "+
			"only {{%s}} and {{%s}} exist",
			f.ID, strings.Join(bad, "}}, {{"), PlaceholderInput, PlaceholderPrevious)
	}
	// The first step cannot see a previous result, and a prompt that asks for one
	// would silently become a prompt with a blank where its subject should be.
	if len(f.Steps) > 0 && f.Steps[0].IsAgent() {
		for _, m := range placeholder.FindAllStringSubmatch(f.Steps[0].Agent, -1) {
			if m[1] == PlaceholderPrevious {
				return fmt.Errorf("%s: step %q is first and uses {{%s}}, but nothing ran "+
					"before it — did you mean {{%s}}?",
					f.ID, f.Steps[0].ID, PlaceholderPrevious, PlaceholderInput)
			}
		}
	}
	return nil
}
