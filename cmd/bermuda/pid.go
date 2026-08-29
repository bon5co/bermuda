package main

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/bon5co/bermuda/v2/internal/store"
)

// Which process is this agent?
//
// An interactive agent has no job id and no run id, so before this file every
// session calling itself `ada` was the same holder as every other one. Two
// were running one morning; one released the other's live 45-minute lease and
// was told it had succeeded. Nothing anywhere said a word. The pid is what makes
// them two holders.
//
// The obvious answer — os.Getpid() — is the wrong one, and wrong in the way that
// destroys claims silently. `bermuda thread claim` is a process that lives for
// milliseconds, so the pid it reports is different on every call: two consecutive
// shell invocations from one agent session on this machine reported 322534 and
// 322594, each its own session leader with its own sid. Claim and release would
// never match, and no agent could release its own lease. It is kept only as the
// last resort, where having no discriminator at all is the alternative.
//
// What is wanted is the pid of the *agent*, which outlives the commands it runs.

// pidSources is the resolution order, first hit wins.
//
// BERMUDA_PID is the override, and it comes first for the reason overrides
// exist: when this resolves to the wrong thing, the agent that noticed needs a
// way to say so without a new build.
//
// CLAUDE_PID is exported by Claude Code and is the agent process itself —
// measured stable across every invocation within one session, which is the
// property the whole mechanism needs. HERDR_PANE_ID is stable per pane and is
// the discriminator for an agent running under herdr without Claude Code.
var pidSources = []string{"BERMUDA_PID", "CLAUDE_PID", "HERDR_PANE_ID"}

// resolvePID works out what to write as this agent's pid, and says where the
// answer came from.
//
// The source is returned rather than discarded because this is the field that
// decides whether a release is accepted, and "which of these four rules fired"
// is the first question anyone will ask when it is wrong. `bermuda thread
// whoami` prints it.
func resolvePID() (pid, source string) {
	for _, env := range pidSources {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, "$" + env
		}
	}
	// The session leader is a weaker answer than an agent's own pid — a shell
	// that starts its own session gets a fresh one — but it is stable for the
	// common case of several commands run from one login session, and it is
	// still better than a pid that is guaranteed to differ.
	if sid, err := unix.Getsid(0); err == nil && sid > 0 {
		return strconv.Itoa(sid), "session leader"
	}
	return strconv.Itoa(os.Getpid()), "os.Getpid()"
}

// withPID stamps an interactive identity with the process that owns it.
//
// Only interactive identities come through here. A bermuda-launched run already
// carries a job id and a run id, which make two runs of one job two holders
// without any of this.
func withPID(id store.Identity) store.Identity {
	id.PID, _ = resolvePID()
	return id
}
