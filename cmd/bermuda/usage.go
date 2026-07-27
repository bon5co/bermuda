package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	tokens "github.com/bon5co/bermuda/internal/usage"
)

// usageCmd answers "which job is expensive" in one command.
func usageCmd(argv []string) error {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	since := fs.Duration("since", 7*24*time.Hour, "window to total over, e.g. 24h")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	rows, err := s.UsageByJob(context.Background(), time.Now().Add(-*since))
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Printf("no runs in the last %s\n", *since)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "JOB\tRUNS\tIN\tOUT\tCACHE READ\tCACHE WRITE\tMODEL\tLAST")
	for _, u := range rows {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", u.JobID, u.Runs,
			tokens.FormatCount(u.InputTokens), tokens.FormatCount(u.OutputTokens),
			tokens.FormatCount(u.CacheReadTokens), tokens.FormatCount(u.CacheCreationTokens),
			u.Model, u.LastRun.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}
