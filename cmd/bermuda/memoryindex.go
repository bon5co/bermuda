package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bon5co/bermuda/v2/internal/index"
)

// The searchable half of the memory layer.
//
// `memory` anchors where the notes live; this is the same notes in the one
// other format a question can be asked of them. Grep answers "which note
// contains this word", and every question worth asking a vault is the other
// one — "which note decided this" — asked by someone who does not remember the
// words the note used. A vector index is the cheapest thing that answers it,
// and Chroma runs embedded against a directory, so answering it costs no
// server, no port and nothing to supervise.
//
// The index is derived data throughout. It is rebuilt from the vault, never
// the other way round, and deleting its directory is a supported way to make
// it go away.

// indexClient builds a client over the resolved vault root.
func indexClient() *index.Client {
	return index.New(index.DefaultDir(stateDir()), index.ResolveRoot(memoryDir()))
}

// memoryIndexCmd is `bermuda memory index`.
func memoryIndexCmd(argv []string) error {
	fs := flag.NewFlagSet("memory index", flag.ContinueOnError)
	rebuild := fs.Bool("rebuild", false, "drop the collection and index every note again")
	status := fs.Bool("status", false, "report what is indexed and what is stale, without indexing")
	drop := fs.Bool("drop", false, "delete the collection and forget everything indexed")
	asJSON := fs.Bool("json", false, "machine-readable output")
	quiet := fs.Bool("quiet", false, "print only the summary line")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	c := indexClient()
	ctx := context.Background()

	switch {
	case *status:
		return reportIndexStatus(os.Stdout, c, ctx, *asJSON)
	case *drop:
		if err := c.Drop(ctx); err != nil {
			return helperAdvice(err)
		}
		fmt.Printf("index dropped; %s can be deleted\n", c.Dir())
		return nil
	}

	progress := func(s string) { fmt.Println("bermuda:", s) }
	if *quiet || *asJSON {
		progress = func(string) {}
	}
	rep, err := c.Sweep(ctx, *rebuild, progress)
	if err != nil {
		return helperAdvice(err)
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(rep)
	}
	printSweep(os.Stdout, rep)
	return nil
}

// printSweep says what the sweep did, in one line when it did nothing.
func printSweep(w io.Writer, rep index.Report) {
	if rep.Indexed == 0 && rep.Removed == 0 {
		fmt.Fprintf(w, "index up to date: %d note(s) in %s, nothing changed (%s)\n",
			rep.Unchanged, rep.Root, rep.Took.Round(time.Millisecond))
		return
	}
	fmt.Fprintf(w, "indexed %d note(s) (%d new, %d changed) as %d chunk(s)",
		rep.Indexed, rep.New, rep.Changed, rep.Chunks)
	if rep.Removed > 0 {
		fmt.Fprintf(w, ", removed %d", rep.Removed)
	}
	fmt.Fprintf(w, "; %d unchanged (%s)\n", rep.Unchanged, rep.Took.Round(time.Millisecond))
	for _, f := range rep.Skipped {
		fmt.Fprintf(w, "  skipped %s: %s\n", f.Path, f.Skipped)
	}
}

// reportIndexStatus prints where the index stands. It embeds nothing, so it is
// safe to run when no helper is installed at all.
func reportIndexStatus(w io.Writer, c *index.Client, ctx context.Context, asJSON bool) error {
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(w).Encode(st)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "vault\t%s\n", st.Root)
	fmt.Fprintf(tw, "index\t%s\n", st.Dir)
	fmt.Fprintf(tw, "indexed\t%d note(s), %d chunk(s)\n", st.Files, st.Chunks)
	if !st.Swept.IsZero() {
		// What the sweep costs is the number that decides whether running it
		// on the daemon's timer was a good idea. It belongs on screen, not in
		// a benchmark nobody reruns.
		fmt.Fprintf(tw, "last sweep\t%s in %s, %d note(s) hashed\n",
			st.Swept.Format(time.RFC3339), st.Took.Round(time.Millisecond), st.Seen)
	}
	if !st.Wrote.IsZero() {
		fmt.Fprintf(tw, "last write\t%s — %d note(s), %d chunk(s) in %s\n",
			st.Wrote.Format(time.RFC3339), st.WroteNotes, st.WroteChunks,
			st.WroteTook.Round(time.Millisecond))
	} else if !st.Newest.IsZero() {
		fmt.Fprintf(tw, "last write\t%s\n", st.Newest.Format(time.RFC3339))
	}
	fmt.Fprintf(tw, "stale\t%d note(s) changed or new, %d gone\n", st.Stale, st.Missing)
	if st.Python == "" {
		fmt.Fprintf(tw, "helper\tnot available — %s\n", helperHowTo)
	} else {
		fmt.Fprintf(tw, "helper\t%s\n", st.Python)
	}
	return tw.Flush()
}

// memorySearchCmd is `bermuda memory search`.
func memorySearchCmd(argv []string) error {
	fs := flag.NewFlagSet("memory search", flag.ContinueOnError)
	n := fs.Int("n", 8, "how many results")
	section := fs.String("section", "", "restrict to one folder of the vault, e.g. memory")
	typ := fs.String("type", "", "restrict to a note type: user, feedback, project, reference")
	asJSON := fs.Bool("json", false, "machine-readable output")
	full := fs.Bool("full", false, "print the whole matching paragraph rather than a snippet")
	words, err := parseAround(fs, argv)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(words, " "))
	if query == "" {
		return errors.New("usage: bermuda memory search <question> [-n 8] [--section memory]")
	}
	hits, err := indexClient().Search(context.Background(), query,
		index.SearchOpts{N: *n, Section: *section, Type: *typ})
	if err != nil {
		return helperAdvice(err)
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(hits)
	}
	printHits(os.Stdout, hits, *full)
	return nil
}

// parseAround parses flags that appear anywhere, not only before the first
// word of the query.
//
// Go's flag package stops at the first non-flag argument, so
// `memory search why was it replaced -n 3` silently searched for the string
// "why was it replaced -n 3" and returned eight results. Nothing errored, the
// results looked plausible, and the only tell was the count. Agents write the
// question first because that is how every other search command on earth
// works, so the parser has to cope rather than the caller.
func parseAround(fs *flag.FlagSet, argv []string) ([]string, error) {
	var words []string
	for len(argv) > 0 {
		if err := fs.Parse(argv); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		words = append(words, fs.Arg(0))
		argv = fs.Args()[1:]
	}
	return words, nil
}

// printHits renders results as the note they came from, the heading they sat
// under, and the text itself — the three things needed to decide whether to
// open the file.
func printHits(w io.Writer, hits []index.Hit, full bool) {
	if len(hits) == 0 {
		fmt.Fprintln(w, "nothing matched; `bermuda memory index --status` says whether anything is indexed")
		return
	}
	for i, h := range hits {
		if i > 0 {
			fmt.Fprintln(w)
		}
		where := h.Path
		if h.Heading != "" {
			where += "  ·  " + h.Heading
		}
		fmt.Fprintf(w, "%.2f  %s\n", h.Score, where)
		fmt.Fprintf(w, "      %s\n", indent(body(h.Text), "      ", full))
	}
}

// body strips the title and heading prefix the chunk was embedded with: it is
// already printed on the line above, and repeating it costs the reader the
// first two lines of every result.
func body(text string) string {
	if _, after, ok := strings.Cut(text, "\n\n"); ok {
		return after
	}
	return text
}

// indent re-indents a paragraph under its heading line, trimming it to a
// couple of lines unless the reader asked for all of it.
func indent(text, pad string, full bool) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if !full && len(lines) > 4 {
		lines = append(lines[:4], "…")
	}
	return strings.Join(lines, "\n"+pad)
}

// helperHowTo is the one sentence that turns "not configured" into a fix.
const helperHowTo = "install uv (https://docs.astral.sh/uv/), or set $BERMUDA_INDEX_PYTHON to a python with chromadb"

// helperAdvice turns the one failure a fresh install actually hits into
// instructions instead of an error nobody can act on.
func helperAdvice(err error) error {
	if errors.Is(err, index.ErrNoPython) {
		return fmt.Errorf("the search index needs Chroma, which runs in Python: %s", helperHowTo)
	}
	return err
}
