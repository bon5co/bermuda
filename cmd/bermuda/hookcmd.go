package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

func hookCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda hook <status|redeliver>")
	}
	switch argv[0] {
	case "status":
		return hookStatus(argv[1:])
	case "redeliver":
		return hookRedeliver(argv[1:])
	default:
		return fmt.Errorf("unknown hook subcommand %q", argv[0])
	}
}

func hookStatus(argv []string) error {
	if len(argv) > 0 {
		return errors.New("usage: bermuda hook status")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	events, err := s.HookEvents(context.Background())
	if err != nil {
		return err
	}
	if len(events) == 0 {
		fmt.Println("no pending, retrying, dead or skipped hook events")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "EVENT\tRUN\tSETTLEMENT\tSTATUS\tATTEMPTS\tNEXT\tERROR")
	for _, e := range events {
		next := e.NextAt
		if e.Status() == "dead" || e.Status() == "skipped" {
			next = ""
		}
		last := strings.ReplaceAll(strings.ReplaceAll(e.LastError, "\n", " "), "\r", " ")
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%d\t%s\t%s\n", e.ID, e.RunID, e.Settlement, e.Status(), e.Attempts, next, last)
	}
	return w.Flush()
}

func hookRedeliver(argv []string) error {
	fs := flag.NewFlagSet("hook redeliver", flag.ContinueOnError)
	dead := fs.Bool("dead", false, "redeliver all dead events")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	args := fs.Args()
	if (*dead && len(args) != 0) || (!*dead && len(args) != 1) {
		return errors.New("usage: bermuda hook redeliver <event-id>|--dead")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	if *dead {
		n, err := s.RedeliverDeadRunEvents(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("requeued %d dead hook event(s)\n", n)
		return nil
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid hook event ID %q", args[0])
	}
	if err := s.RedeliverRunEvent(ctx, id); err != nil {
		return err
	}
	fmt.Printf("requeued hook event %d\n", id)
	return nil
}
