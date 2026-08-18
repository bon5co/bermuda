package runner

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A flow run gets its own space, and the steps in it share one thread.
//
// What travels down a flow chain is one line: the note the previous step
// published. That is deliberate — handing B the transcript of A would let B
// inherit every assumption A made — but it means everything else a step learned
// dies with the process. The fourth step rediscovers that the migration already
// ran, that the arm64 failure is in the C shim, that the credentials in the
// README are stale. Each rediscovery costs an agent turn, and some of them fail.
//
// A team of people does not work that way. They say what they found where the
// others can hear it, and the record outlives whoever was in the room. Bermuda
// already has that shape: a thread belongs to a herdr workspace, and every agent
// in that space is in it without doing anything. So a flow run opens a space of
// its own, every step's tab is created inside it, and all of them write to and
// read from the one thread — a chain for the verdict, a thread for the evidence.
//
// The space is per *run*, not per flow. Two runs of the same flow are two
// investigations, and merging their findings would hand step two of the second
// run the stale conclusions of the first.

// EnvThread names the thread a step's shell writes to, so `bermuda thread post`
// inside a step needs no --thread flag. It is read first of everything thread
// resolution consults, which is what makes the space's thread the default for a
// whole step rather than a flag every command has to repeat.
const EnvThread = "BERMUDA_THREAD"

// FlowSpace is the space one flow run's steps share, and the thread that space
// owns.
//
// Both ids are carried because they are used for different things: the workspace
// is where tabs are created, and the thread is what the agents in them write to.
// A zero FlowSpace is the honest answer on a machine with no herdr, and every
// caller treats it as "no space" rather than as an error — a flow that refused to
// run because it could not open a window would be a worse failure than one whose
// steps cannot compare notes.
type FlowSpace struct {
	WorkspaceID string
	Label       string
	Thread      string
	// RootTabID is the tab herdr insists on making when it creates a workspace.
	// Nothing ever runs in it — every step opens a tab of its own — so it sits in
	// the room as an empty shell that an operator has to look past to find the
	// one that matters. Carrying its id is what lets the run reap it once the
	// space holds a tab somebody actually wants.
	//
	// Deliberately not written to the run record. A resume finds the space
	// already there, which means the attempt that created it has already reaped
	// its root tab: there is nothing left to close and nothing worth remembering
	// across processes. Persisting it would add a schema column whose only
	// possible value on read is "already handled".
	RootTabID string
}

// Usable reports whether steps can be pointed at this space.
func (s *FlowSpace) Usable() bool {
	return s != nil && strings.TrimSpace(s.WorkspaceID) != ""
}

// HasThread reports whether there is a conversation to send steps to.
//
// Separate from Usable because the two fail apart: herdr can hand out a space
// while the store cannot name its thread, and a step in a shared space with no
// thread should still run — it just has nobody to tell.
func (s *FlowSpace) HasThread() bool {
	return s.Usable() && strings.TrimSpace(s.Thread) != ""
}

// SpaceLabel names a flow run's space.
//
// The suffix is random rather than derived from the run id because this name
// only has to keep concurrent spaces apart. The run record stores the workspace
// id after creation, so a resume reuses the space it already had instead of
// trying to reconstruct its name — which is why the run is not an argument at
// all: a parameter the label ignores invites the next reader to believe the
// name can be recomputed from it.
func SpaceLabel(flowID string) string {
	id := strings.TrimSpace(flowID)
	if id == "" {
		id = "flow"
	}
	return "FLOWS:" + id + ":" + nanoID6()
}

// parkedMark separates a space's name from the verdict appended when its run
// parked. A separator rather than a rewrite, so the original name is recoverable
// by cutting at it: a resume has to put the label back, and recomputing it is
// not possible — SpaceLabel's suffix is random, and a resumed run that renamed
// its space to a fresh name would be a different room to anyone watching.
const parkedMark = " · "

// LabelParked is the space's name with why its run stopped on the end.
//
// The room is what an operator sees first, and an open space named exactly as it
// was while running says only that something happened. It is the label, not a
// message, because a label survives being scrolled past.
func LabelParked(label, stoppedAt string, reason ParkReason) string {
	base := LabelBase(label)
	verdict := "parked"
	if stoppedAt != "" {
		verdict += " " + stoppedAt
	}
	if reason != "" {
		verdict += " (" + string(reason) + ")"
	}
	return base + parkedMark + verdict
}

// LabelBase is the space's name without any parked verdict, which is what a
// resume renames it back to.
func LabelBase(label string) string {
	base, _, found := strings.Cut(label, parkedMark)
	if !found {
		return label
	}
	return base
}

const nanoIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

// nanoID6 returns six characters from Nano ID's URL-safe alphabet.
//
// There are exactly 64 choices, so taking the low six bits of each
// cryptographically random byte gives every character the same probability.
// crypto/rand.Read on the Go version this module targets either fills the
// buffer or terminates the process if the operating system's random source is
// irrecoverably unavailable, which leaves no weaker fallback id to collide.
func nanoID6() string {
	var random [6]byte
	rand.Read(random[:])
	var id [6]byte
	for i, b := range random {
		id[i] = nanoIDAlphabet[b&63]
	}
	return string(id[:])
}

// threadContract is appended to every agent step's prompt when the flow has a
// thread.
//
// It is stated on every step for the same reason the result contract is: a step
// is a fresh agent that has read nothing, and a convention nobody tells it about
// is a convention it does not follow. The instruction is about *what* to post,
// not only how — an agent given a channel and no brief writes status updates,
// which cost the next step tokens and tell it nothing.
const threadContract = `
---
You are one step of a flow, and its steps share a thread: %[1]s. The steps before
and after you are their own agents with their own context; they cannot see yours,
and the only thing this harness hands the next step is the one-line note in your
result.json. The thread is where everything else you learned survives you.

Read it before you start, so you do not rediscover what an earlier step already
found:

    %[2]s thread log

Post as you learn things, not at the end:

    %[2]s thread post 'the migration already ran — the column is there'
    %[2]s thread event 'deleted the old config; anything cached from it is stale'

Findings, not status. "starting on the tests" tells the next step nothing and
costs it tokens to read; "the arm64 failure is in the C shim, not in the Go" is
why the thread exists. Post what you would want to have been told if you were
the next step: what is actually true, what you ruled out, what surprised you,
and anything that makes a later step's assumption wrong. No thread flag is
needed — this shell is already in that conversation.`

// ThreadContract is the instruction appended to an agent step's prompt telling
// it to publish what it finds into the flow's thread.
func ThreadContract(thread string) string {
	return fmt.Sprintf(threadContract, thread, BermudaBin())
}

// BermudaBin is the command a step should call to reach bermuda.
//
// It is this process's own path, not the bare word: bermuda is not necessarily
// on any PATH — it is commonly run from a checkout — and a prompt telling an
// agent to run `bermuda thread post` on a machine where that resolves to nothing
// is an instruction that fails silently, which is precisely the class of failure
// flows exist to remove. The bare name is the fallback for the case where the
// executable cannot be resolved at all.
func BermudaBin() string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "bermuda"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		return resolved
	}
	return exe
}
