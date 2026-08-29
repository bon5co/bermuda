package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bon5co/bermuda/v3/internal/herdrcli"
)

// Telling herdr who this agent is.
//
// This is the piece that makes @mentions land. Herdr detects agents but does
// not name them: `herdr agent list` reports the kind (`claude`), the pane, the
// working directory and the terminal title, and the name field is empty unless
// something sets it. So a mention could only be resolved by directory, and with
// three sessions open in ~/dotfiles every one of them answered to @dotfiles and
// all three were told — which is not addressing anybody, it is broadcasting.
//
// Registering is one call to `herdr agent rename`. The identity is the same one
// claims use, so the name a mention has to say is the name in `thread status`.

// registerTimeout bounds the herdr call. Registration rides on the path of a
// command that has already done its real work, so a wedged herdr must cost that
// command nothing it cannot afford.
const registerTimeout = 3 * time.Second

// paneID is the pane this process is running in, which is what herdr renames.
// Every pane herdr starts carries it, so an interactive agent, a bermuda run
// and a shell a person opened are all covered.
func paneID() string { return os.Getenv("HERDR_PANE_ID") }

// registerName tells herdr what to call the agent in this pane.
func registerName(ctx context.Context, name string) error {
	pane := paneID()
	if pane == "" {
		return errors.New("no HERDR_PANE_ID: this is not running inside a herdr pane, " +
			"so there is no agent for a mention to reach")
	}
	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()
	return herdrcli.New().AgentRename(ctx, pane, name)
}

// announce registers the identity best-effort and says nothing when it works.
//
// It is called on every interactive write to the thread, and deliberately
// swallows its own failure. An agent that posts a message has just told the
// world something true; failing that post because herdr would not take a name
// would trade the durable record for the convenience of being mentionable. The
// reverse — registering silently — costs one local call and means nobody has to
// remember a setup step, which is the failure mode this whole harness exists to
// remove.
func announceName(name string) {
	if name == "" || paneID() == "" {
		return
	}
	_ = registerName(context.Background(), name)
}

// threadRegister is the explicit form, for a session that wants to be
// addressable before it has anything to say.
func threadRegister(argv []string) error {
	fs := flag.NewFlagSet("thread register", flag.ExitOnError)
	as := fs.String("as", "", "name to register; defaults to the thread identity")
	clear := fs.Bool("clear", false, "remove this agent's name, so it is identified by its directory again")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	pane := paneID()
	if pane == "" {
		return errors.New("no HERDR_PANE_ID: this is not running inside a herdr pane, " +
			"so there is no agent for a mention to reach")
	}

	ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
	defer cancel()

	if *clear {
		// Worth doing on the way out: a name left behind resolves to a pane whose
		// session has ended, and the mention is reported as delivered.
		if err := herdrcli.New().AgentClearName(ctx, pane); err != nil {
			return err
		}
		fmt.Printf("%s is no longer named; it answers to its directory again\n", pane)
		return nil
	}

	id, err := resolveIdentity(*as)
	if err != nil {
		return err
	}
	if err := herdrcli.New().AgentRename(ctx, pane, id.Name); err != nil {
		return err
	}
	fmt.Printf("%s is registered as %s — @%s now reaches it\n", pane, id.Name, id.Name)
	return nil
}
