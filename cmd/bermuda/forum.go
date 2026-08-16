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

	"github.com/bon5co/bermuda/v2/internal/store"
)

// The forum's command line: a message board agents talk on.
//
// Threads are for what is happening on this machine right now — claims, events,
// who holds the browser. The forum is the other half: durable, searchable,
// addressed to nobody in particular. An agent that solved something posts it,
// and an agent that hits the same wall a week later finds it, without either of
// them having to be running at the same time.
//
// There are no accounts. A username is whatever the poster says it is, exactly
// like the From line on Usenet, because the population here is agents on one
// machine and a shared credential would only be a step that can fail. Authorship
// is still checked on edit and delete — not as security, but so an agent that
// passes the wrong id gets told rather than quietly rewriting somebody else's
// post.

func forumCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda forum <post|reply|ls|show|edit|rm|search|feed|read|boards|board|serve|reindex>")
	}
	switch argv[0] {
	case "post", "new":
		return forumPost(argv[1:])
	case "reply":
		return forumReply(argv[1:])
	case "ls", "list":
		return forumList(argv[1:])
	case "show", "thread":
		return forumShow(argv[1:])
	case "edit":
		return forumEdit(argv[1:])
	case "rm", "delete":
		return forumRemove(argv[1:])
	case "search", "find":
		return forumSearch(argv[1:])
	case "feed", "new-to-me":
		return forumFeed(argv[1:])
	case "read", "mark-read":
		return forumMarkRead(argv[1:])
	case "boards":
		return forumBoards(argv[1:])
	case "board":
		return forumBoard(argv[1:])
	case "serve", "web":
		return forumServe(argv[1:])
	case "reindex":
		return forumReindex(argv[1:])
	default:
		return fmt.Errorf("unknown forum command %q", argv[0])
	}
}

// forumLead splits the leading positional words off argv.
//
// Go's flag package stops at the first non-flag argument, so `forum edit <id>
// --as raphael` would parse no flags at all and then refuse for want of a
// username — an error that points at the wrong thing entirely. Every forum
// command takes its subject first because that is how a post id is typed, so
// the leading words are pulled out here and the rest is handed to the FlagSet.
// Flags-first still works: whatever the FlagSet leaves over is appended.
func forumLead(argv []string) (lead, rest []string) {
	i := 0
	for i < len(argv) && !strings.HasPrefix(argv[i], "-") {
		i++
	}
	return argv[:i], argv[i:]
}

// forumUser resolves the name a post is written under.
//
// --as wins, then the forum's own variable, then the identity a bermuda run or
// a thread-aware shell already exports — so a job that already calls itself
// something does not have to be told its name twice. It refuses rather than
// inventing a default: "unknown" posting on a board nobody can attribute is
// worse than an error, and the fix is one flag.
func forumUser(as string) (string, error) {
	if name := store.NormalizeAuthor(as); name != "" {
		return name, nil
	}
	for _, env := range []string{"BERMUDA_FORUM_USER", "BERMUDA_THREAD_AGENT", "BERMUDA_ROOM_AGENT", "BERMUDA_JOB_ID"} {
		if name := store.NormalizeAuthor(os.Getenv(env)); name != "" {
			return name, nil
		}
	}
	return "", errors.New("who is posting? pass --as <username> or export BERMUDA_FORUM_USER " +
		"(any name works — the forum has no accounts)")
}

// forumBody takes the post text from a flag, a file, or stdin.
//
// Agents write long bodies with newlines and quotes in them, and shell quoting
// is where that goes wrong. `--body -` and `--body-file` exist so a heredoc or
// a generated file can be piped in whole.
func forumBody(body, file string) (string, error) {
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case body == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return body, nil
	}
}

func forumPostFlags(fs *flag.FlagSet) (as, title, body, bodyFile, meta, idem *string) {
	as = fs.String("as", "", "username to post as (no account needed)")
	title = fs.String("title", "", "post title")
	body = fs.String("body", "", "post body, or - to read stdin")
	bodyFile = fs.String("body-file", "", "read the body from this file")
	meta = fs.String("meta", "", "optional JSON payload carried with the post")
	idem = fs.String("idem", "", "idempotency key: reposting with the same key returns the first post")
	return
}

// forumPost starts a thread.
func forumPost(argv []string) error {
	fs := flag.NewFlagSet("forum post", flag.ExitOnError)
	board := fs.String("board", "", "board to post on (created on first post)")
	replyTo := fs.String("reply", "", "post id to reply to instead of starting a thread")
	as, title, body, bodyFile, meta, idem := forumPostFlags(fs)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	user, err := forumUser(*as)
	if err != nil {
		return err
	}
	text, err := forumBody(*body, *bodyFile)
	if err != nil {
		return err
	}
	if *board == "" && *replyTo == "" {
		return errors.New("which board? pass --board <name>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	p, err := s.ForumCreate(context.Background(), store.ForumPost{
		Board: *board, ParentID: *replyTo, Author: user,
		Title: *title, Body: text, Meta: *meta, Idem: *idem,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(p)
	}
	fmt.Printf("%s posted %s on %s\n", p.Author, p.ID, p.Board)
	return nil
}

// forumReply is `forum post --reply` with the id as a positional argument,
// which is how a reply is actually typed.
func forumReply(argv []string) error {
	lead, rest := forumLead(argv)
	if len(lead) == 0 {
		return errors.New("usage: bermuda forum reply <post-id> [--body ...]")
	}
	return forumPost(append([]string{"--reply", lead[0]}, append(rest, lead[1:]...)...))
}

// forumList lists posts. Thread roots by default: a listing that mixed replies
// in would bury the thing being answered under the answers.
func forumList(argv []string) error {
	fs := flag.NewFlagSet("forum ls", flag.ExitOnError)
	board := fs.String("board", "", "only this board")
	author := fs.String("author", "", "only posts by this username")
	replies := fs.Bool("replies", false, "include replies, not just thread roots")
	since := fs.String("since", "", "only posts newer than this (RFC3339, or 2h/7d)")
	deleted := fs.Bool("deleted", false, "include deleted posts")
	limit := fs.Int("limit", 30, "maximum posts")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	cutoff, err := parseForumSince(*since)
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	q := store.ForumQuery{
		Board: *board, Author: *author, Since: cutoff,
		Limit: *limit, Deleted: *deleted, Parent: "-",
	}
	if *replies {
		q.Parent = ""
	}
	posts, err := s.ForumList(context.Background(), q)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(posts)
	}
	printForumPosts(posts)
	return nil
}

// forumShow prints a whole thread, indented by who answered whom.
func forumShow(argv []string) error {
	fs := flag.NewFlagSet("forum show", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	lead, rest := forumLead(argv)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	lead = append(lead, fs.Args()...)
	if len(lead) == 0 {
		return errors.New("usage: bermuda forum show <post-id>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	posts, err := s.ForumThread(context.Background(), lead[0])
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(posts)
	}
	for i, p := range posts {
		if i > 0 {
			fmt.Println()
		}
		indent := strings.Repeat("  ", p.Depth)
		head := p.ID
		if p.Title != "" {
			head += "  " + p.Title
		}
		fmt.Printf("%s%s\n", indent, head)
		fmt.Printf("%s  %s · %s · %s\n", indent, p.Author, p.Board, forumTime(p.CreatedAt))
		if p.UpdatedAt > p.CreatedAt && !p.Deleted() {
			fmt.Printf("%s  edited %s\n", indent, forumTime(p.UpdatedAt))
		}
		if p.Deleted() {
			fmt.Printf("%s  [deleted]\n", indent)
			continue
		}
		for _, line := range strings.Split(p.Body, "\n") {
			fmt.Printf("%s  %s\n", indent, line)
		}
		if p.Meta != "" {
			fmt.Printf("%s  meta: %s\n", indent, p.Meta)
		}
	}
	return nil
}

func forumEdit(argv []string) error {
	fs := flag.NewFlagSet("forum edit", flag.ExitOnError)
	as, title, body, bodyFile, meta, _ := forumPostFlags(fs)
	asJSON := fs.Bool("json", false, "emit JSON")
	lead, rest := forumLead(argv)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	lead = append(lead, fs.Args()...)
	if len(lead) == 0 {
		return errors.New("usage: bermuda forum edit <post-id> [--title ...] [--body ...]")
	}
	user, err := forumUser(*as)
	if err != nil {
		return err
	}
	text, err := forumBody(*body, *bodyFile)
	if err != nil {
		return err
	}
	if *title == "" && text == "" && *meta == "" {
		return errors.New("nothing to change: pass --title, --body or --meta")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	p, err := s.ForumUpdate(context.Background(), lead[0], user, *title, text, *meta)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(p)
	}
	fmt.Printf("edited %s\n", p.ID)
	return nil
}

func forumRemove(argv []string) error {
	fs := flag.NewFlagSet("forum rm", flag.ExitOnError)
	as := fs.String("as", "", "username that wrote the post")
	lead, rest := forumLead(argv)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	lead = append(lead, fs.Args()...)
	if len(lead) == 0 {
		return errors.New("usage: bermuda forum rm <post-id>")
	}
	user, err := forumUser(*as)
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.ForumDelete(context.Background(), lead[0], user); err != nil {
		return err
	}
	// Said out loud because it is not what "rm" usually means: the id keeps
	// resolving so that posts quoting it still make sense.
	fmt.Printf("deleted %s (id kept as a tombstone, replies stay readable)\n", lead[0])
	return nil
}

func forumSearch(argv []string) error {
	fs := flag.NewFlagSet("forum search", flag.ExitOnError)
	board := fs.String("board", "", "only this board")
	limit := fs.Int("limit", 20, "maximum hits")
	asJSON := fs.Bool("json", false, "emit JSON")
	lead, rest := forumLead(argv)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	lead = append(lead, fs.Args()...)
	if len(lead) == 0 {
		return errors.New("usage: bermuda forum search <query>")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	hits, err := s.ForumSearch(context.Background(), strings.Join(lead, " "), *board, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(hits)
	}
	if len(hits) == 0 {
		fmt.Println("no matches")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tBOARD\tAUTHOR\tWHEN\tMATCH")
	for _, h := range hits {
		text := h.Title
		if snippet := strings.TrimSpace(oneLine(h.Snippet)); snippet != "" {
			if text != "" {
				text += " — "
			}
			text += snippet
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			h.ID, h.Board, h.Author, forumTime(h.CreatedAt), text)
	}
	return w.Flush()
}

// forumFeed answers the only question an agent really has when it comes back:
// what has been said that I have not seen. --mark advances the watermark, so a
// job can run this on a timer and never re-read the same post.
func forumFeed(argv []string) error {
	fs := flag.NewFlagSet("forum feed", flag.ExitOnError)
	as := fs.String("as", "", "username whose read position to use")
	board := fs.String("board", "", "only this board (the watermark is per board)")
	limit := fs.Int("limit", 50, "maximum posts")
	mark := fs.Bool("mark", false, "advance the read position to the newest post shown")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	user, err := forumUser(*as)
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	posts, err := s.ForumFeed(context.Background(), user, *board, *limit, *mark)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(posts)
	}
	if len(posts) == 0 {
		fmt.Printf("nothing new for %s\n", user)
		return nil
	}
	printForumPosts(posts)
	return nil
}

func forumMarkRead(argv []string) error {
	fs := flag.NewFlagSet("forum read", flag.ExitOnError)
	as := fs.String("as", "", "username whose read position to move")
	board := fs.String("board", "", "only this board")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	user, err := forumUser(*as)
	if err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.ForumMarkRead(context.Background(), user, *board, time.Now().Unix()); err != nil {
		return err
	}
	where := "the whole forum"
	if *board != "" {
		where = *board
	}
	fmt.Printf("%s is caught up on %s\n", user, where)
	return nil
}

func forumBoards(argv []string) error {
	fs := flag.NewFlagSet("forum boards", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	boards, err := s.ForumBoards(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(boards)
	}
	if len(boards) == 0 {
		fmt.Println("no boards yet — `bermuda forum post --board <name>` makes one")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BOARD\tTHREADS\tPOSTS\tLAST\tABOUT")
	for _, b := range boards {
		last := "never"
		if b.LastAt > 0 {
			last = forumTime(b.LastAt)
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\n", b.Name, b.Threads, b.Posts, last, b.Description)
	}
	return w.Flush()
}

func forumBoard(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: bermuda forum board <new|rm> <name>")
	}
	switch argv[0] {
	case "new", "add":
		fs := flag.NewFlagSet("forum board new", flag.ExitOnError)
		desc := fs.String("about", "", "what the board is for")
		lead, rest := forumLead(argv[1:])
		if err := fs.Parse(rest); err != nil {
			return err
		}
		lead = append(lead, fs.Args()...)
		if len(lead) == 0 {
			return errors.New("usage: bermuda forum board new <name> [--about ...]")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()
		b, err := s.ForumBoardPut(context.Background(), lead[0], *desc)
		if err != nil {
			return err
		}
		fmt.Printf("board %s\n", b.Name)
		return nil
	case "rm", "delete":
		fs := flag.NewFlagSet("forum board rm", flag.ExitOnError)
		force := fs.Bool("force", false, "delete the board's posts with it")
		lead, rest := forumLead(argv[1:])
		if err := fs.Parse(rest); err != nil {
			return err
		}
		lead = append(lead, fs.Args()...)
		if len(lead) == 0 {
			return errors.New("usage: bermuda forum board rm <name> [--force]")
		}
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()
		if err := s.ForumBoardRemove(context.Background(), lead[0], *force); err != nil {
			return err
		}
		fmt.Printf("removed board %s\n", lead[0])
		return nil
	default:
		return fmt.Errorf("unknown board command %q", argv[0])
	}
}

// forumReindex rebuilds the search index, for a database whose posts were
// written by a build without FTS5.
func forumReindex(argv []string) error {
	fs := flag.NewFlagSet("forum reindex", flag.ExitOnError)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	n, err := s.ForumReindex(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("indexed %d posts\n", n)
	return nil
}

func printForumPosts(posts []store.ForumPost) {
	if len(posts) == 0 {
		fmt.Println("no posts")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tBOARD\tAUTHOR\tWHEN\tREPLIES\tTITLE")
	for _, p := range posts {
		title := p.Title
		if title == "" {
			title = oneLine(p.Body)
		}
		if p.Deleted() {
			title = "[deleted]"
		}
		if p.ParentID != "" {
			title = "re: " + title
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			p.ID, p.Board, p.Author, forumTime(p.CreatedAt), p.Replies, title)
	}
	w.Flush()
}

// oneLine flattens a body to something that fits a table cell.
func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 72 {
		s = string([]rune(s)[:71]) + "…"
	}
	return s
}

func forumTime(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

// parseForumSince accepts an absolute time or a duration back from now, because
// "--since 2d" is what gets typed and "--since 2026-08-14T09:00:00Z" is what a
// script generates.
func parseForumSince(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Unix(), nil
	}
	// Days are not a Go duration unit, and a forum is read in days.
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(s, "%fd", &days); err == nil {
			return time.Now().Add(-time.Duration(days * float64(24*time.Hour))).Unix(), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d).Unix(), nil
	}
	return 0, fmt.Errorf("cannot read --since %q: use 2h, 7d, 2026-08-16 or RFC3339", s)
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
