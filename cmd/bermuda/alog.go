package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bon5co/bermuda/v3/internal/alog"
)

const alogUsage = `usage: bermuda alog <list|read|write|edit|path>
  list [--json] [--limit 20]        Newest entries first (0 means all)
  read <id> [--json]               Read one entry
  write --repo <repo> --branch <branch> --topic <topic> --body <text|->
  edit <id> [--repo ...] [--branch ...] [--topic ...] [--body <text|->]
  path                            Directory containing the Markdown files
Bodies contain at most 50 whitespace-separated words. --body - reads stdin.`

func alogCmd(argv []string) error {
	return cardFeedCmd(activityCLI, argv)
}

// Both Markdown feeds share parsing and output, while their storage callbacks
// retain the separate validation rules and directories.
type cardFeedCLI struct {
	Name, Label, Usage, BodyHelp string
	HasKind                      bool
	Dir                          func(string) string
	List                         func(string) ([]alog.Entry, error)
	Read                         func(string, string) (alog.Entry, error)
	Write                        func(string, alog.Entry) (alog.Entry, error)
	Update                       func(string, string, func(*alog.Entry) error) (alog.Entry, error)
}

var activityCLI = cardFeedCLI{
	Name: "alog", Label: "A.LOG", Usage: alogUsage,
	BodyHelp: "summary, at most 50 words; - reads stdin",
	Dir:      alog.Dir, List: alog.List, Read: alog.Read, Write: alog.Write, Update: alog.Update,
}

func cardFeedCmd(feed cardFeedCLI, argv []string) error {
	if len(argv) == 0 {
		return errors.New(feed.Usage)
	}
	if argv[0] == "--help" || argv[0] == "-h" || argv[0] == "help" {
		fmt.Println(feed.Usage)
		return nil
	}
	dir := feed.Dir(stateDir())
	switch argv[0] {
	case "path":
		if len(argv) != 1 {
			return fmt.Errorf("usage: bermuda %s path", feed.Name)
		}
		fmt.Println(dir)
		return nil
	case "list":
		fs := flag.NewFlagSet(feed.Name+" list", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "emit JSON")
		limit := fs.Int("limit", 20, "maximum entries; 0 means all")
		if err := fs.Parse(argv[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || *limit < 0 {
			return fmt.Errorf("usage: bermuda %s list [--json] [--limit <nonnegative number>]", feed.Name)
		}
		entries, err := feed.List(dir)
		if *limit > 0 && len(entries) > *limit {
			entries = entries[:*limit]
		}
		if *asJSON {
			if encodeErr := json.NewEncoder(os.Stdout).Encode(entries); encodeErr != nil {
				return encodeErr
			}
			return err
		}
		for i, e := range entries {
			if i > 0 {
				fmt.Println()
			}
			printCardEntry(e)
		}
		return err
	case "read":
		if len(argv) < 2 || strings.HasPrefix(argv[1], "-") {
			return fmt.Errorf("usage: bermuda %s read <id> [--json]", feed.Name)
		}
		fs := flag.NewFlagSet(feed.Name+" read", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(argv[2:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: bermuda %s read <id> [--json]", feed.Name)
		}
		e, err := feed.Read(dir, argv[1])
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(e)
		}
		printCardEntry(e)
		return nil
	case "write", "edit":
		return saveCardFeed(feed, argv[0], argv[1:])
	default:
		return fmt.Errorf("unknown %s command %q\n%s", feed.Label, argv[0], feed.Usage)
	}
}

func alogSave(command string, argv []string) error {
	return saveCardFeed(activityCLI, command, argv)
}

func saveCardFeed(feed cardFeedCLI, command string, argv []string) error {
	id := ""
	if command == "edit" {
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return fmt.Errorf("usage: bermuda %s edit <id> [field flags]", feed.Name)
		}
		id, argv = argv[0], argv[1:]
	}
	fs := flag.NewFlagSet(feed.Name+" "+command, flag.ContinueOnError)
	repo := fs.String("repo", "", "repository name")
	branch := fs.String("branch", "", "working branch")
	topic := fs.String("topic", "", "short update topic")
	body := fs.String("body", "", feed.BodyHelp)
	kind := ""
	if feed.HasKind {
		fs.StringVar(&kind, "kind", "", "discovery, mistake, or recovery (required when writing)")
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%s flags must precede positional arguments", feed.Label)
	}
	fields := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { fields[f.Name] = true })
	if command == "edit" && len(fields) == 0 {
		return fmt.Errorf("%s edit needs at least one field flag", feed.Name)
	}
	if fields["body"] && *body == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		*body = string(b)
	}
	dir := feed.Dir(stateDir())
	e := alog.Entry{Repo: *repo, Branch: *branch, Topic: *topic, Body: *body, Kind: kind}
	var saved alog.Entry
	var err error
	if command == "write" {
		saved, err = feed.Write(dir, e)
	} else {
		saved, err = feed.Update(dir, id, func(old *alog.Entry) error {
			if fields["kind"] {
				old.Kind = kind
			}
			if fields["repo"] {
				old.Repo = *repo
			}
			if fields["branch"] {
				old.Branch = *branch
			}
			if fields["topic"] {
				old.Topic = *topic
			}
			if fields["body"] {
				old.Body = *body
			}
			return nil
		})
	}
	if err != nil {
		return err
	}
	fmt.Println(saved.ID)
	return nil
}

func printCardEntry(e alog.Entry) {
	timeLabel := "created"
	if e.TimeSource != "birthtime" {
		timeLabel = "written (filename fallback)"
	}
	fmt.Printf("%s | %s %s\nrepo: %s\nbranch: %s\ntopic: %s\n", e.ID, timeLabel, e.Created.Local().Format("2006-01-02 15:04:05 MST"), e.Repo, e.Branch, e.Topic)
	if e.Kind != "" {
		fmt.Printf("kind: %s\n", e.Kind)
	}
	fmt.Printf("\n%s\n", e.Body)
}
