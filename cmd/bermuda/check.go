package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bon5co/bermuda/v2/internal/checklist"
)

// The checklist's command line.
//
// It exists because of a question a human should never have to ask: "is that
// done yet". A single session opened two PRs, filed two issues, moved a board
// card, wrote three notes and migrated a cron job — every one of them recorded
// somewhere, and nothing enumerating them or saying which were finished, which
// were blocked, and on whom. Four follow-up questions later the state had been
// reconstructed by hand out of records that each held a piece of it.
//
// So the verbs here are small and there are six of them. Everything they touch
// is a Markdown page in the memory vault, which is what makes this survive the
// agent, the flow run, and bermuda itself: `check ls` is a convenience over a
// folder a person can open in Obsidian and read with no CLI at all.

func checkCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda check <new|add|tick|untick|ls|show>")
	}
	switch argv[0] {
	case "new":
		return checkNew(argv[1:])
	case "add":
		return checkAdd(argv[1:])
	case "tick", "done":
		return checkSet(argv[1:], true)
	case "untick", "undone":
		return checkSet(argv[1:], false)
	case "ls", "list":
		return checkList(argv[1:])
	case "show", "cat":
		return checkShow(argv[1:])
	default:
		return fmt.Errorf("unknown check subcommand %q", argv[0])
	}
}

// checkDir is where this installation keeps its checklists: a folder inside the
// memory directory, so they land in the same vault the notes do and inherit its
// sync, history and search. Resolved through memoryDir() rather than
// independently, so a `memory init --vault` moves the checklists with the notes.
func checkDir() string { return checklist.Dir(memoryDir()) }

func checkNew(argv []string) error {
	fs := flag.NewFlagSet("check new", flag.ExitOnError)
	about := fs.String("about", "", "one line on what this piece of work is")
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return errors.New(`usage: bermuda check new "<title>" [--about '...']`)
	}
	title := argv[0]
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	l, err := checklist.New(checkDir(), title, *about, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(l.Path)
	fmt.Printf("add items: bermuda check add %q '<what has to happen>'\n", l.Name)
	return nil
}

func checkAdd(argv []string) error {
	fs := flag.NewFlagSet("check add", flag.ExitOnError)
	ref := fs.String("ref", "", "url or issue that names the artifact")
	blockedOn := fs.String("blocked-on", "", "who this is waiting on — a person, not a thing")
	why := fs.String("why", "", "what only they can do")
	done := fs.Bool("done", false, "add it already ticked, for work finished before the list existed")
	list, rest := splitListArg(argv, 2)
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		return errors.New(`usage: bermuda check add [<list>] "<item>" [--ref <url>] [--blocked-on <who> --why '...']`)
	}
	text := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return err
	}
	// A reason with nobody to hold it is a note; a blocker with no reason is a
	// stop sign with nothing written on it, and the next agent to read the page
	// has no way to tell whether it still holds.
	if strings.TrimSpace(*blockedOn) == "" && strings.TrimSpace(*why) != "" {
		return errors.New("--why says why an item is blocked, so it needs --blocked-on <who>")
	}

	l, err := checklist.Resolve(checkDir(), list)
	if err != nil {
		return err
	}
	it, err := checklist.Add(l.Path, checklist.Entry{
		Text: text, Ref: *ref, BlockedOn: *blockedOn, Why: *why, Done: *done,
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s  %d. %s %s\n", l.Name, it.Index, it.Box(), it)
	return nil
}

// checkSet ticks or unticks one item.
func checkSet(argv []string, done bool) error {
	verb := "tick"
	if !done {
		verb = "untick"
	}
	list, rest := splitListArg(argv, 2)
	if len(rest) == 0 {
		return fmt.Errorf("usage: bermuda check %s [<list>] <n|prefix>", verb)
	}
	l, err := checklist.Resolve(checkDir(), list)
	if err != nil {
		return err
	}
	it, changed, err := checklist.Set(l.Path, rest[0], done)
	if err != nil {
		return err
	}
	// Said out loud rather than reported as success, because "it was already
	// like that" and "I just changed it" are different answers to the only
	// question anybody asks a checklist, and an agent re-ticking an item it
	// ticked an hour ago should be able to tell.
	state := "already"
	if changed {
		state = "now"
	}
	fmt.Printf("%s  %d. %s %s (%s %s)\n", l.Name, it.Index, it.Box(), it, state, boxWord(done))
	// Re-read rather than counted from the copy above: that one was loaded
	// before the tick, and printing its counts would report the page as it was a
	// moment ago — under a line saying it has just changed.
	if after, err := checklist.Load(l.Path); err == nil {
		fmt.Println(after.Counts().Line())
	}
	return nil
}

// boxWord names the state a tick left an item in.
func boxWord(done bool) string {
	if done {
		return "ticked"
	}
	return "open"
}

func checkList(argv []string) error {
	fs := flag.NewFlagSet("check ls", flag.ExitOnError)
	all := fs.Bool("all", false, "include checklists with nothing left open")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	lists, bad := checklist.All(checkDir())
	// An unreadable page is named before the rest are listed, because it is
	// invisible everywhere else — it simply does not appear, which reads as "I
	// never made that one".
	for _, err := range bad {
		fmt.Fprintln(os.Stderr, "bermuda:", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	shown, hidden := 0, 0
	for _, l := range lists {
		c := l.Counts()
		// Finished work is not what this command is for: the question is what is
		// still outstanding, and a folder that keeps everything forever would
		// answer it with a page of noise inside a month.
		if c.Open() == 0 && !*all {
			hidden++
			continue
		}
		shown++
		fmt.Fprintf(w, "%s\t%s\n", l.Name, c.Line())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if shown == 0 {
		if len(lists) == 0 {
			fmt.Println(`no checklists yet — bermuda check new "<what you are doing>" starts one`)
			return nil
		}
		fmt.Printf("nothing open across %d checklist(s) — --all lists the finished ones\n", len(lists))
		return nil
	}
	if hidden > 0 {
		fmt.Printf("(%d finished, hidden — --all shows them)\n", hidden)
	}
	return nil
}

func checkShow(argv []string) error {
	fs := flag.NewFlagSet("check show", flag.ExitOnError)
	raw := fs.Bool("raw", false, "print the page's bytes, to read or paste back")
	list, rest := splitListArg(argv, 1)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	l, err := checklist.Resolve(checkDir(), list)
	if err != nil {
		return err
	}
	if *raw {
		// The file itself. Whoever asks for this is about to edit the page, and a
		// rendering they cannot paste back is the wrong answer.
		data, err := os.ReadFile(l.Path) // #nosec G304 -- resolved from the checklist folder
		if err != nil {
			return err
		}
		fmt.Print(string(data))
		return nil
	}

	fmt.Println(l.Path)
	for _, line := range l.Head {
		fmt.Println(line)
	}
	// Numbered, because the number is what `tick` takes. The page itself has no
	// numbers and should not: they would be wrong the moment somebody inserted a
	// line in Obsidian.
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	for _, it := range l.Items {
		fmt.Fprintf(w, "%d.\t%s\t%s\n", it.Index, it.Box(), it)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(l.Items) == 0 {
		fmt.Printf("\nno items yet — bermuda check add %q '<what has to happen>'\n", l.Name)
		return nil
	}
	fmt.Println()
	fmt.Println(l.Counts().Line())
	return nil
}

// splitListArg separates an optional leading <list> from the rest of a command
// line.
//
// The list is positional and optional, which sounds ambiguous and is not: the
// arity settles it. `check tick 3` names an item and takes the list from
// $BERMUDA_CHECK or the most recent page; `check tick ship-640 3` names both.
// want is how many positional arguments the verb takes when a list is given, so
// anything shorter is the short form.
//
// Only leading non-flag arguments are counted, the same way every other command
// here reads its positional id, because Go's flag parser stops at the first
// non-flag and would otherwise read the item text as a value.
func splitListArg(argv []string, want int) (list string, rest []string) {
	n := 0
	for n < len(argv) && !strings.HasPrefix(argv[n], "-") {
		n++
	}
	if n >= want && want > 0 {
		return argv[0], argv[1:]
	}
	return "", argv
}
