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
	if len(argv) == 0 {
		return errors.New(alogUsage)
	}
	if argv[0] == "--help" || argv[0] == "-h" || argv[0] == "help" {
		fmt.Println(alogUsage)
		return nil
	}
	dir := alog.Dir(stateDir())
	switch argv[0] {
	case "path":
		if len(argv) != 1 {
			return errors.New("usage: bermuda alog path")
		}
		fmt.Println(dir)
		return nil
	case "list":
		fs := flag.NewFlagSet("alog list", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "emit JSON")
		limit := fs.Int("limit", 20, "maximum entries; 0 means all")
		if err := fs.Parse(argv[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || *limit < 0 {
			return errors.New("usage: bermuda alog list [--json] [--limit <nonnegative number>]")
		}
		entries, err := alog.List(dir)
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
			printALog(e)
		}
		return err
	case "read":
		if len(argv) < 2 || strings.HasPrefix(argv[1], "-") {
			return errors.New("usage: bermuda alog read <id> [--json]")
		}
		fs := flag.NewFlagSet("alog read", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(argv[2:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("usage: bermuda alog read <id> [--json]")
		}
		e, err := alog.Read(dir, argv[1])
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(e)
		}
		printALog(e)
		return nil
	case "write", "edit":
		return alogSave(argv[0], argv[1:])
	default:
		return fmt.Errorf("unknown A.LOG command %q\n%s", argv[0], alogUsage)
	}
}

func alogSave(command string, argv []string) error {
	id := ""
	if command == "edit" {
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
			return errors.New("usage: bermuda alog edit <id> [--repo ...] [--branch ...] [--topic ...] [--body ...]")
		}
		id, argv = argv[0], argv[1:]
	}
	fs := flag.NewFlagSet("alog "+command, flag.ContinueOnError)
	repo := fs.String("repo", "", "repository name")
	branch := fs.String("branch", "", "working branch")
	topic := fs.String("topic", "", "short update topic")
	body := fs.String("body", "", "summary, at most 50 words; - reads stdin")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("A.LOG flags must precede positional arguments")
	}
	fields := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { fields[f.Name] = true })
	if command == "edit" && len(fields) == 0 {
		return errors.New("alog edit needs at least one field flag")
	}
	if fields["body"] && *body == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		*body = string(b)
	}
	dir := alog.Dir(stateDir())
	e := alog.Entry{Repo: *repo, Branch: *branch, Topic: *topic, Body: *body}
	var saved alog.Entry
	var err error
	if command == "write" {
		saved, err = alog.Write(dir, e)
	} else {
		saved, err = alog.Update(dir, id, func(old *alog.Entry) error {
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

func printALog(e alog.Entry) {
	timeLabel := "created"
	if e.TimeSource != "birthtime" {
		timeLabel = "written (filename fallback)"
	}
	fmt.Printf("%s | %s %s\nrepo: %s\nbranch: %s\ntopic: %s\n\n%s\n", e.ID, timeLabel, e.Created.Local().Format("2006-01-02 15:04:05 MST"), e.Repo, e.Branch, e.Topic, e.Body)
}
