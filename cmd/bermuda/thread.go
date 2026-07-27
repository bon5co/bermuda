package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/mention"
	"github.com/bon5co/bermuda/internal/store"
)

// The thread's command line.
//
// It is a separate binary entry point rather than a bermuda-runs-only feature on
// purpose: an interactive session, a launcher script, or a one-off shell must be
// able to join the same thread. Any agent that cannot join is a blind spot, and
// a coordination mechanism with blind spots is worse than none — it is the same
// resource contended by two populations, one of which is invisible.

func threadCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda thread <list|new|rm|post|event|claim|release|status|log|with|whoami|register>")
	}
	switch argv[0] {
	case "whoami":
		return threadWhoami(argv[1:])
	case "register":
		return threadRegister(argv[1:])
	case "list":
		return threadList(argv[1:])
	case "new":
		return threadNew(argv[1:])
	case "rm":
		return threadRemove(argv[1:])
	case "post", "note":
		return threadSay(argv[1:], store.KindNote)
	case "event":
		return threadSay(argv[1:], store.KindEvent)
	case "claim":
		return threadClaim(argv[1:])
	case "release":
		return threadRelease(argv[1:])
	case "status":
		return threadStatus(argv[1:])
	case "log":
		return threadLog(argv[1:])
	case "with":
		return threadWith(argv[1:])
	default:
		return fmt.Errorf("unknown thread subcommand %q", argv[0])
	}
}

// roomCmd is the old name for `bermuda thread`, kept working and kept out of
// the usage text.
//
// Scheduled jobs, launcher scripts, and shell history all hold `bermuda room
// ...`, and none of them get re-read when a feature is renamed. Breaking them
// would fail in the worst available way: a claim that never gets taken is the
// two-agents-on-one-browser bug this whole feature exists to stop, and the
// caller would see only a shell error it was not watching for. One line of
// noise is cheaper. It goes to stderr so `room log --json` still pipes into a
// parser unchanged.
func roomCmd(argv []string) error {
	fmt.Fprintln(os.Stderr, "bermuda: `room` was renamed to `thread`; the old name still works")
	return threadCmd(argv)
}

// resolveIdentity works out who is speaking, and refuses to guess.
//
// Order matters. Bermuda's own injected environment is the most trustworthy
// answer because the harness wrote it, not the agent; --as is how anything else
// names itself. There is deliberately no fallback to the user or to a bare
// name: $USER would make every interactive agent on this machine the same
// holder — able to release each other's leases and never told.
//
// A name alone was that same bug wearing a nicer hat, which is why every
// interactive identity is stamped with a pid here. A bermuda run is not: it
// already carries a job id and a run id, which do the same work.
// It also registers the name with herdr on the way past. That is a side effect
// in a resolver, and it is deliberate: registering is what makes `@name` reach
// this agent instead of every session that happens to share its directory, and
// a step an agent has to remember is a step that gets skipped. Doing it here
// means naming yourself to bermuda is the same act as naming yourself to herdr,
// and there is no third place it can be forgotten. It is best-effort and silent
// — see announceName.
func resolveIdentity(as string) (store.Identity, error) {
	if as = strings.TrimSpace(as); as != "" {
		id := withPID(store.Identity{Name: as})
		announceName(id.Name)
		return id, nil
	}
	if job := os.Getenv("BERMUDA_JOB_ID"); job != "" {
		id := store.Identity{Name: job, JobID: job}
		if dir := os.Getenv("BERMUDA_RUN_DIR"); dir != "" {
			id.RunID = filepath.Base(dir)
		}
		return id, nil
	}
	// BERMUDA_ROOM_AGENT is the pre-rename name, still read because it is
	// exported by shells that were started before the rename and by scripts
	// nobody has revisited. Dropping it would not error — it would fall through
	// to the refusal below and stop an agent that had named itself correctly.
	for _, env := range []string{"BERMUDA_THREAD_AGENT", "BERMUDA_ROOM_AGENT"} {
		if agent := strings.TrimSpace(os.Getenv(env)); agent != "" {
			id := withPID(store.Identity{Name: agent})
			announceName(id.Name)
			return id, nil
		}
	}
	return store.Identity{}, errors.New(
		"cannot tell who you are: pass --as <name> or export BERMUDA_THREAD_AGENT " +
			"(a claim nobody can be asked about is not a claim)")
}

// resolveThread works out which conversation a command is about: --thread,
// then $BERMUDA_THREAD, then global.
//
// Unlike identity this has a final fallback, and it is safe to have one.
// Guessing an identity wrongly makes two agents one lease-holder; guessing a
// thread wrongly puts a message somewhere legible, in the conversation
// everything unqualified already shares.
//
// $BERMUDA_THREAD is what makes a whole shell — or a whole job — belong to one
// conversation without every command repeating the flag, which is the shape
// this is actually used in: an agent is working on one project for its whole
// run.
func resolveThread(flagged string) (string, error) {
	if id := strings.TrimSpace(flagged); id != "" {
		return store.ParseThreadID(id)
	}
	if id := strings.TrimSpace(os.Getenv("BERMUDA_THREAD")); id != "" {
		return store.ParseThreadID(id)
	}
	return store.GlobalThread, nil
}

// threadFlag registers --thread the same way on every subcommand that takes it,
// so the help text cannot drift between them.
func threadFlag(fs *flag.FlagSet) *string {
	return fs.String("thread", "", "which thread to use; defaults to $BERMUDA_THREAD, then global")
}

// claimsAreGlobal says so, once, when --thread was passed to a command it does
// not narrow.
//
// The flag is accepted rather than rejected because a caller that exports
// $BERMUDA_THREAD and scripts `--thread "$BERMUDA_THREAD"` everywhere is doing
// the sensible thing, and an exit-2 flag error is a poor answer to it. It is
// not silently ignored either: a filter that does nothing is exactly the sort
// of thing somebody then trusts.
func claimsAreGlobal(flagged, why string) {
	if strings.TrimSpace(flagged) == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "bermuda: claims are global — one browser is one browser "+
		"in every thread — so --thread "+why)
}

// threadList shows every conversation and how busy it is.
func threadList(argv []string) error {
	fs := flag.NewFlagSet("thread list", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	threads, err := s.Threads(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(threads)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "THREAD\tMESSAGES\tLAST\tABOUT")
	for _, t := range threads {
		// A thread nobody has written in reads as "never" rather than as a
		// timestamp from 1970, which looks like a bug in the store.
		last := "never"
		if !t.LastAt.IsZero() {
			last = t.LastAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", t.ID, t.Messages, last, t.About)
	}
	return w.Flush()
}

// threadNew creates a conversation.
func threadNew(argv []string) error {
	fs := flag.NewFlagSet("thread new", flag.ExitOnError)
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: bermuda thread new <id> [--about <text>]")
	}
	id := argv[0]
	about := fs.String("about", "", "what this thread is for, shown in the picker and in `thread list`")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	t, err := s.NewThread(context.Background(), id, *about)
	if err != nil {
		return err
	}
	fmt.Printf("created thread %s\n", t.ID)
	return nil
}

// threadRemove deletes a conversation and everything said in it.
func threadRemove(argv []string) error {
	fs := flag.NewFlagSet("thread rm", flag.ExitOnError)
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: bermuda thread rm <id> [--force]")
	}
	id := argv[0]
	force := fs.Bool("force", false, "delete it even though it still holds messages")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	n, err := s.RemoveThread(context.Background(), id, *force)
	if err != nil {
		return err
	}
	fmt.Printf("removed thread %s and the %d messages in it\n", id, n)
	return nil
}

// threadSay posts a note or an event.
func threadSay(argv []string, kind store.ThreadKind) error {
	fs := flag.NewFlagSet("thread "+string(kind), flag.ExitOnError)
	as := fs.String("as", "", "identity to speak as, when not inside a bermuda run")
	thread := threadFlag(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	body := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if body == "" {
		return fmt.Errorf("usage: bermuda thread %s [--as <name>] <text>", kind)
	}
	// Go's flag parsing stops at the first word of the message, so a flag typed
	// after the text is silently part of the text. Say so rather than letting
	// `thread event removed camoufox --as setup-agent` post its own identity.
	for _, arg := range fs.Args() {
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			return fmt.Errorf("%q came after the message text, so it was read as part of it: "+
				"flags go before the text, as `bermuda thread %s %s ... <text>`", arg, kind, arg)
		}
	}
	id, err := resolveIdentity(*as)
	if err != nil {
		return err
	}
	into, err := resolveThread(*thread)
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	m, err := s.ThreadPost(context.Background(), store.ThreadMessage{
		Thread: into, Kind: kind, By: id, Body: body})
	if err != nil {
		return err
	}
	// The thread is named back, because the one that was written to is the one
	// thing the caller cannot see from here and the thing a typo changes.
	fmt.Printf("%s %s (%s): %s\n", m.CreatedAt.Format("15:04"), m.Kind, m.Thread, m.Body)
	// After the post, and never before it. Delivery is the courtesy; the thread
	// is the record.
	announce(os.Stderr, theHerd(), m)
	return nil
}

// theHerd is how the CLI reaches live agents.
//
// It is a variable so tests can put a fake in its place. A test that built the
// real one would prompt whichever agents happen to be running on this machine,
// which is a test that types into somebody's live session.
var theHerd = func() mention.Herd { return mention.FromHerdr(herdrcli.New()) }

// announce pushes a posted message into every live agent it mentions, and says
// on stderr who was reached and who was not.
//
// Nothing here can fail the command. The message is already in the thread by
// the time this runs, and an agent that has exited since somebody last wrote
// its name is the ordinary case rather than an error — the log is full of names
// of agents that finished hours ago.
func announce(w io.Writer, h mention.Herd, m store.ThreadMessage) {
	res, err := mention.Deliver(context.Background(), h, mention.Message{
		Thread: m.Thread, Author: m.By.String(), Body: m.Body,
	}, mention.Me(m.By.Name))
	if err != nil {
		// Only herdr being unreachable lands here — no herdr server, or a
		// bermuda command run from cron. Worth one line, because a mention that
		// silently reached nobody looks exactly like one that was answered.
		fmt.Fprintln(w, "bermuda: could not ask herdr who is live:", err,
			"— the message is in the thread, nobody was told")
		return
	}
	mention.Report(w, res)
}

// threadClaim takes a resource.
func threadClaim(argv []string) error {
	fs := flag.NewFlagSet("thread claim", flag.ExitOnError)
	req, wait, err := parseClaimFlags(fs, argv, "bermuda thread claim <resource> [--ttl 20m] [--why ...] [--wait 5m] [--thread <id>]")
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	c, err := s.ThreadClaimWait(context.Background(), req, wait)
	if err != nil {
		return err
	}
	fmt.Printf("claimed %s as %s (%s)\n", c.Resource, c.Holder, c.ExpiryLabel(time.Now()))
	return nil
}

// parseClaimFlags reads the flags claim and with share, so the two cannot drift
// apart in what they mean by a lease.
//
// The resource is positional and comes first, before any flag, because that is
// how the command reads aloud: claim the browser, for twenty minutes, because.
func parseClaimFlags(fs *flag.FlagSet, argv []string, usage string) (store.ClaimRequest, time.Duration, error) {
	var req store.ClaimRequest
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return req, 0, errors.New("usage: " + usage)
	}
	req.Resource = argv[0]
	as := fs.String("as", "", "identity to claim as, when not inside a bermuda run")
	ttl := fs.Duration("ttl", 0, "lease length; without one the claim never lapses")
	why := fs.String("why", "", "why the resource is needed, shown to whoever is waiting")
	wait := fs.Duration("wait", 0, "block up to this long for the resource to come free")
	thread := fs.String("thread", "", "which thread to record the claim in; the claim itself is global")
	if err := fs.Parse(argv[1:]); err != nil {
		return req, 0, err
	}
	id, err := resolveIdentity(*as)
	if err != nil {
		return req, 0, err
	}
	// The thread only decides where the claim is written down. The resource is
	// taken from everybody either way, which is why this is not called a scope.
	into, err := resolveThread(*thread)
	if err != nil {
		return req, 0, err
	}
	req.By, req.TTL, req.Why, req.Thread = id, *ttl, *why, into
	return req, *wait, nil
}

// threadRelease gives a resource back, and fails when the caller does not hold it.
func threadRelease(argv []string) error {
	fs := flag.NewFlagSet("thread release", flag.ExitOnError)
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New("usage: bermuda thread release <resource> [--as <name>]")
	}
	resource := argv[0]
	as := fs.String("as", "", "identity to release as, when not inside a bermuda run")
	thread := fs.String("thread", "", "accepted and not used: the release is recorded where the claim was")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	claimsAreGlobal(*thread, "is not used here: the release is recorded in the thread "+
		"the claim was made in, so the pair reads as one exchange")
	id, err := resolveIdentity(*as)
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	c, err := s.ThreadRelease(context.Background(), resource, id, time.Time{})
	if err != nil {
		return err
	}
	fmt.Printf("released %s, held %s\n", c.Resource, store.ShortDuration(time.Since(c.Since)))
	return nil
}

// threadStatus answers "who holds what, and until when".
func threadStatus(argv []string) error {
	fs := flag.NewFlagSet("thread status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	thread := fs.String("thread", "", "accepted and not used: holds are the same in every thread")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	claimsAreGlobal(*thread, "does not narrow this: every hold is shown, whichever "+
		"thread it was taken from")
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	now := time.Now()
	claims, err := s.ThreadClaims(context.Background(), now)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(claims)
	}
	if len(claims) == 0 {
		fmt.Println("nothing is claimed")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RESOURCE\tHOLDER\tSINCE\tHELD\tEXPIRES\tWHY")
	for _, c := range claims {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Resource, c.Holder,
			c.Since.Format("15:04"), store.ShortDuration(now.Sub(c.Since)),
			c.ExpiryLabel(now), c.Why)
	}
	return w.Flush()
}

// threadWhoami prints the identity this shell would claim under, and where its
// pid came from.
//
// It exists because the pid is the field that decides whether a release is
// accepted, and it is resolved from the environment rather than stated. When it
// resolves to the wrong thing the symptom is a refused release with no
// explanation attached, so there has to be one command that shows the working.
func threadWhoami(argv []string) error {
	fs := flag.NewFlagSet("thread whoami", flag.ExitOnError)
	as := fs.String("as", "", "identity to test, as it would be passed to a claim")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	id, err := resolveIdentity(*as)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "identity\t%s\n", id)
	fmt.Fprintf(w, "name\t%s\n", id.Name)
	if id.JobID != "" {
		// A bermuda run is identified by its job and run, and carries no pid:
		// saying so here stops the reader hunting for a pid that is absent on
		// purpose.
		fmt.Fprintf(w, "job\t%s\n", id.JobID)
		fmt.Fprintf(w, "run\t%s\n", id.RunID)
		fmt.Fprintf(w, "pid\t(none: a bermuda run is told apart by its run id)\n")
		return w.Flush()
	}
	pid, source := resolvePID()
	fmt.Fprintf(w, "pid\t%s\n", pid)
	fmt.Fprintf(w, "pid source\t%s\n", source)
	fmt.Fprintf(w, "override\texport BERMUDA_PID=<value> to force it\n")
	return w.Flush()
}

// threadLog prints the thread, oldest first, through a bounded read window.
//
// The window is the point. Two hundred lines of thread is a large share of the
// context an agent has to spend, and most of it is settled history; the default
// is the last fifty messages of the last day, whichever bites first. Asking for
// more is allowed up to a ceiling and clamped past it, and a read that was cut
// short says so — see logWindowNotice for why that line is not optional.
func threadLog(argv []string) error {
	fs := flag.NewFlagSet("thread log", flag.ExitOnError)
	since := fs.Duration("since", 0, "how far back to reach, e.g. 1h (default 24h, ceiling 7d)")
	kinds := fs.String("kind", "", "comma-separated kinds: claim,release,event,ask,note")
	limit := fs.Int("limit", 0, "at most this many, newest kept (default 50, ceiling 200)")
	asJSON := fs.Bool("json", false, "emit JSON")
	thread := threadFlag(fs)
	all := fs.Bool("all", false, "every thread at once, rather than one conversation")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	window := store.ThreadReadWindow(*limit, *since, time.Now())
	if window.LimitClamped {
		fmt.Fprintf(os.Stderr, "bermuda: --limit %d is above the ceiling; reading the last %d\n",
			*limit, window.Limit)
	}
	if window.AgeClamped {
		fmt.Fprintf(os.Stderr, "bermuda: --since %s reaches past the ceiling; reading the last %s\n",
			ageLabel(*since), ageLabel(window.Age))
	}
	f := window.Apply(store.ThreadFilter{})
	if !*all {
		// Without --all the log is one conversation, because reading somebody
		// else's project is the cost this feature exists to remove.
		into, err := resolveThread(*thread)
		if err != nil {
			return err
		}
		f.Thread = into
	}
	for _, k := range splitList(*kinds) {
		kind, err := store.ParseThreadKind(k)
		if err != nil {
			return err
		}
		f.Kinds = append(f.Kinds, kind)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	msgs, err := s.ThreadLog(ctx, f)
	if err != nil {
		return err
	}
	if err := reportLogWindow(ctx, s, f, window, len(msgs)); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(msgs)
	}
	if len(msgs) == 0 {
		// Named, because "the thread is empty" about a thread the caller did not
		// mean to be reading is indistinguishable from nothing having happened.
		if f.Thread == "" {
			fmt.Println("every thread is empty")
		} else {
			fmt.Printf("%s is empty\n", f.Thread)
		}
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, m := range msgs {
		// The thread column appears only when more than one is being shown: in
		// a single conversation it is the same word on every line.
		if f.Thread == "" {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				m.CreatedAt.Format("2006-01-02 15:04"), m.Thread, m.Kind, m.By,
				m.Resource, threadBody(m))
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			m.CreatedAt.Format("2006-01-02 15:04"), m.Kind, m.By, m.Resource,
			threadBody(m))
	}
	return w.Flush()
}

// reportLogWindow tells the reader what the window kept out.
//
// It counts twice: what the filter matches inside the window, and what it
// matches with the age bound lifted. Two numbers, because the two bounds are cut
// short in different ways and a reader deciding whether to widen needs to know
// which one bit.
func reportLogWindow(ctx context.Context, s *store.Store, f store.ThreadFilter, w store.ThreadWindow, shown int) error {
	inWindow, err := s.ThreadCount(ctx, f)
	if err != nil {
		return err
	}
	unbounded := f
	unbounded.Since = time.Time{}
	total, err := s.ThreadCount(ctx, unbounded)
	if err != nil {
		return err
	}
	if notice := logWindowNotice(shown, inWindow, total-inWindow, w); notice != "" {
		// stderr, so `thread log --json` still pipes into a parser unchanged.
		fmt.Fprintln(os.Stderr, notice)
	}
	return nil
}

// logWindowNotice is the line a truncated log prints, and is empty when nothing
// was left out.
//
// A truncated log that looks complete is the failure mode this whole window
// risks introducing: an agent reads fifty lines, concludes it has the whole
// picture, and acts on a thread whose load-bearing message was number
// fifty-one. Saying so costs one line and removes the failure.
func logWindowNotice(shown, inWindow, older int, w store.ThreadWindow) string {
	if inWindow <= shown && older <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("bermuda: ")
	if inWindow > shown {
		fmt.Fprintf(&b, "showing the last %d of %d messages in the last %s",
			shown, inWindow, ageLabel(w.Age))
	} else {
		fmt.Fprintf(&b, "showing all %d in the last %s", shown, ageLabel(w.Age))
	}
	if older > 0 {
		fmt.Fprintf(&b, ", and %d older than that", older)
	}
	ceiling := fmt.Sprintf("%d messages / %s", store.ThreadMaxLimit, ageLabel(store.ThreadMaxAge))
	if w.Limit >= store.ThreadMaxLimit && w.Age >= store.ThreadMaxAge {
		// Advising --limit at the ceiling would be advice that does nothing.
		fmt.Fprintf(&b, "; that is the ceiling (%s)", ceiling)
	} else {
		fmt.Fprintf(&b, "; --since/--limit to widen, ceiling is %s", ceiling)
	}
	return b.String()
}

// ageLabel renders a window's reach the way the flag that sets it is written, so
// the ceiling reads as 7d rather than as 168h.
func ageLabel(d time.Duration) string {
	const day = 24 * time.Hour
	if d >= 2*day && d%day == 0 {
		return fmt.Sprintf("%dd", int(d/day))
	}
	return store.ShortDuration(d)
}

// threadBody renders what a message says, folding a claim's lease into its line so
// the log answers "for how long" without a second lookup.
func threadBody(m store.ThreadMessage) string {
	body := m.Body
	if m.Kind == store.KindClaim {
		lease := "no expiry"
		if ttl := m.TTL(); ttl > 0 {
			lease = "ttl " + store.ShortDuration(ttl)
		}
		if body == "" {
			return lease
		}
		return body + " (" + lease + ")"
	}
	return body
}

// threadWith is the enforceable form of a lease: acquire, run, release even on
// failure.
//
// This is the shape that matters. A lease that is merely advertised in a prompt
// gets skipped, because skipping it produces no error — the guard-main.sh
// lesson. `thread with browser --ttl 20m -- camoufox.sh` cannot be forgotten by
// an agent that uses it, and a launcher script that wraps itself in one cannot
// be bypassed by an agent that never read the rule.
func threadWith(argv []string) error {
	sep := -1
	for i, a := range argv {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep == len(argv)-1 {
		return errors.New("usage: bermuda thread with <resource> [--ttl 20m] [--why ...] [--wait 5m] [--thread <id>] -- <command> [args...]")
	}
	fs := flag.NewFlagSet("thread with", flag.ExitOnError)
	req, wait, err := parseClaimFlags(fs, argv[:sep],
		"bermuda thread with <resource> [--ttl 20m] [--why ...] [--wait 5m] [--thread <id>] -- <command> [args...]")
	if err != nil {
		return err
	}
	command := argv[sep+1:]
	if req.TTL <= 0 {
		// Every other way of losing this lease is handled — the command failing,
		// a signal, the wrapper exiting — and a TTL is the only thing that
		// covers the one that is not: this process being killed outright. A
		// wrapper holding a resource forever after being SIGKILLed is the
		// orphaned browser this command exists to prevent.
		return errors.New("thread with needs --ttl: without one a killed wrapper holds " +
			req.Resource + " forever, with nothing left to release it")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	c, err := s.ThreadClaimWait(ctx, req, wait)
	if err != nil {
		// Nothing has run, so this is a plain refusal: the message names the
		// holder and when the lease ends, which is what the caller needs to
		// decide whether to wait or go away.
		return err
	}
	fmt.Fprintf(os.Stderr, "bermuda: holding %s as %s (%s)\n",
		c.Resource, c.Holder, c.ExpiryLabel(time.Now()))

	code := runUnderClaim(command)

	// Release whatever happened to the command, including a signal: the lease
	// exists to be given back, and the failure path is the one where an agent
	// would otherwise forget.
	if _, err := s.ThreadRelease(ctx, req.Resource, req.By, time.Time{}); err != nil {
		// The command has already run and its exit code is the honest answer to
		// what the caller asked for, so this does not become the exit status.
		// It is still loud: losing a lease mid-command means the resource may
		// have been handed to somebody else while this command was using it.
		fmt.Fprintf(os.Stderr, "bermuda: could not release %s: %v\n", req.Resource, err)
		fmt.Fprintf(os.Stderr, "bermuda: the lease did not outlast the command — "+
			"raise --ttl, and check whether anything else used %s meanwhile\n", req.Resource)
	}

	s.Close()
	os.Exit(code)
	return nil
}

// runUnderClaim runs the command and returns its exit status.
//
// Signals are forwarded rather than caught and dropped: a Ctrl-C or a systemd
// stop must reach the child, and this process has to survive long enough
// afterwards to release the lease. Only SIGKILL escapes that, which is what the
// TTL is for.
func runUnderClaim(command []string) int {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "bermuda:", err)
		return 127
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigs:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()

	err := cmd.Wait()
	close(done)
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return code
		}
		// Killed by a signal: report it the way a shell does.
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return 1
	}
	fmt.Fprintln(os.Stderr, "bermuda:", err)
	return 1
}
