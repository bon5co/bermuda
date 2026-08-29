package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/mention"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// deadHerd answers every question the way a machine with no herdr server does.
//
// It is a fake because the real one would prompt whichever agents are running
// on this machine, and BERMUDA_STATE_DIR — never BERMUDA_HOME, which is
// ignored — keeps the store off the live database.
type deadHerd struct{ err error }

func (d deadHerd) Live(context.Context) ([]mention.Agent, error) { return nil, d.err }
func (d deadHerd) Deliver(context.Context, string, string) error { return d.err }

// The thread is the record and delivery is a courtesy on top of it. If a failed
// delivery could fail the post, then herdr being down would mean the one thing
// that survives an agent — what it wrote down — is the thing that gets lost.
func TestAPostLandsEvenWhenNobodyCanBeTold(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	old := theHerd
	theHerd = func() mention.Herd { return deadHerd{err: errors.New("no herdr server")} }
	t.Cleanup(func() { theHerd = old })

	if err := threadSay([]string{"--as", "tester", "@ada", "camoufox", "is", "gone"},
		store.KindEvent); err != nil {
		t.Fatalf("a delivery that could not happen failed the post: %v", err)
	}

	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	msgs, err := s.ThreadLog(context.Background(), store.ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "camoufox is gone") {
		t.Fatalf("the thread holds %v, want the message that was posted", msgs)
	}
}

// The report is what tells an agent whether the thing it just said reached
// anybody. Silence would be indistinguishable from a delivered message, and the
// author would sit waiting for an answer from an agent that was never told.
func TestTheReportSaysHerdrCouldNotBeAsked(t *testing.T) {
	var out strings.Builder
	announce(&out, deadHerd{err: errors.New("no herdr server")}, store.ThreadMessage{
		Thread: "global", By: store.Identity{Name: "tester"}, Body: "@ada ping"}, "")
	if !strings.Contains(out.String(), "the message is in the thread") {
		t.Errorf("the report %q does not say the message was posted anyway", out.String())
	}
}

// A message that mentions nobody must produce no output at all. `bermuda thread
// post` is run by scripts and by every job that records what it did, and a line
// of stderr on each of them is noise that trains everybody to stop reading it.
func TestAMessageWithNoMentionsSaysNothing(t *testing.T) {
	var out strings.Builder
	announce(&out, deadHerd{err: errors.New("must not be asked")}, store.ThreadMessage{
		Thread: "global", By: store.Identity{Name: "tester"}, Body: "gog configured"}, "")
	if out.String() != "" {
		t.Errorf("a message addressed to nobody printed %q", out.String())
	}
}
