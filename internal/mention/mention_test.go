package mention

import (
	"strings"
	"testing"
)

// A message names several agents at once far more often than one, and the whole
// point is that each of them is reached: a note saying "@ada and @tiktok,
// the browser is free" that reaches only the first leaves the second waiting on
// a resource nobody is holding.
func TestEveryNameInAMessageIsFound(t *testing.T) {
	got := Names("@ada and @tiktok-deals, the browser is free (@handler fyi)")
	want := []string{"ada", "tiktok-deals", "handler"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("found %v, want %v", got, want)
	}
}

// Mentions sit in prose, so they end at commas and full stops. If the
// punctuation came with the name, `@ada.` would resolve to an agent called
// "ada." — which matches nothing, and reads on stderr exactly like the
// agent having exited.
func TestPunctuationIsNotPartOfTheName(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"ping @ada.", "ada"},
		{"ping @ada, please", "ada"},
		{"(@ada)", "ada"},
		{"'@ada'", "ada"},
		{"@ada's turn", "ada"},
		{"@ada:", "ada"},
		{"see @mira.local for the host", "mira.local"},
	} {
		got := Names(tc.body)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%q gave %v, want [%s]", tc.body, got, tc.want)
		}
	}
}

// An email address in a body is common — signup notes, gog output, anything
// quoting a login. Reading one as a mention would try to prompt an agent called
// "gmail.com" on every message that mentions an inbox, and the stderr noise
// would be there on messages that addressed nobody at all.
func TestAnEmailAddressIsNotAMention(t *testing.T) {
	for _, body := range []string{
		"someone@example.com is signed in",
		"mail dev+bermuda@example.com about it",
		"100%off@example.com",
	} {
		if got := Names(body); len(got) != 0 {
			t.Errorf("%q parsed %v as mentions; an address is not an address-to", body, got)
		}
	}
}

// @all is the broadcast, and it has to survive being written the way people
// write it — at the start of a line, in the middle of a sentence, capitalised.
func TestAllIsJustAName(t *testing.T) {
	for _, body := range []string{"@all camoufox is gone", "heads up @All", "…@all."} {
		got := Names(body)
		if len(got) != 1 || !strings.EqualFold(got[0], All) {
			t.Errorf("%q gave %v, want the broadcast", body, got)
		}
	}
}

// The same agent named twice is one agent. Without the fold it would be
// prompted twice for one message, and an agent that receives a question twice
// answers it twice — two runs, two sets of tokens, possibly two edits.
func TestTheSameNameTwiceIsOneMention(t *testing.T) {
	got := Names("@dotfiles ping, and @dotfiles again")
	if len(got) != 1 {
		t.Fatalf("got %v, want one name: resolution is case-insensitive, so these are one agent", got)
	}
}

// The board colours the exact characters that were typed, so the offsets have
// to cover the @ and stop at the end of the name. An offset that is off by one
// paints over the space or eats the first letter, and the bubble's right border
// stops lining up with every other bubble.
func TestFindPointsAtTheTextItMatched(t *testing.T) {
	body := "ok @ada, done"
	found := oneMention(t, body)
	if body[found.Start:found.End] != "@ada" {
		t.Errorf("the span covers %q, want @ada", body[found.Start:found.End])
	}
}

// oneMention is the single mention in a body, or a failure naming what was
// there instead.
func oneMention(t *testing.T, body string) Mention {
	t.Helper()
	ms := Find(body)
	if len(ms) != 1 {
		t.Fatalf("%q holds %d mentions, want one", body, len(ms))
	}
	return ms[0]
}

// A bare @ is a shrug, a price, or the start of a word somebody deleted. Taking
// it as a mention would mean resolving the empty name, which matches every
// agent with an empty label.
func TestABareAtIsNothing(t *testing.T) {
	for _, body := range []string{"@", "@ ada", "cost @ 3 dollars", "@!"} {
		if got := Names(body); len(got) != 0 {
			t.Errorf("%q parsed %v as mentions", body, got)
		}
	}
}
