package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// These drive the argv-taking commands rather than the store, because the
// guards that only exist up here are the ones worth pinning: the id comes first
// on the command line but Go's flag package stops at the first positional, and
// the username is resolved from three places before a post is refused.

func forumEnv(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("BERMUDA_FORUM_USER", "")
	t.Setenv("BERMUDA_THREAD_AGENT", "")
	t.Setenv("BERMUDA_ROOM_AGENT", "")
	t.Setenv("BERMUDA_JOB_ID", "")
	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestForumPostAndReplyTakeTheIdBeforeTheFlags(t *testing.T) {
	s := forumEnv(t)
	ctx := context.Background()
	if err := forumCmd([]string{"post", "--board", "ops", "--as", "raphael",
		"--title", "browser claim stuck", "--body", "chromium-cdp died"}); err != nil {
		t.Fatal(err)
	}
	roots, err := s.ForumList(ctx, store.ForumQuery{Parent: "-"})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 {
		t.Fatalf("posts = %+v, want one", roots)
	}
	// `reply <id> --as ...` is how a reply is typed. Parsing that as a bare
	// FlagSet would see no flags at all and refuse for want of a username.
	if err := forumCmd([]string{"reply", roots[0].ID, "--as", "june", "--body", "confirmed"}); err != nil {
		t.Fatalf("reply with the id first: %v", err)
	}
	thread, err := s.ForumThread(ctx, roots[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 2 || thread[1].Author != "june" {
		t.Fatalf("thread = %+v, want the reply from june", thread)
	}
}

func TestForumEditAndRemoveNameTheAuthorTheyRefuse(t *testing.T) {
	s := forumEnv(t)
	ctx := context.Background()
	p, err := s.ForumCreate(ctx, store.ForumPost{Board: "ops", Author: "raphael", Body: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	err = forumCmd([]string{"edit", p.ID, "--as", "june", "--body", "hijack"})
	if err == nil || !strings.Contains(err.Error(), "raphael") {
		t.Fatalf("edit as the wrong author: err = %v, want it to name raphael", err)
	}
	if err := forumCmd([]string{"rm", p.ID, "--as", "june"}); err == nil {
		t.Fatal("rm as the wrong author should refuse")
	}
	if err := forumCmd([]string{"rm", p.ID, "--as", "raphael"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ForumGet(ctx, p.ID)
	if err != nil {
		t.Fatalf("the id stopped resolving after rm: %v", err)
	}
	if !got.Deleted() {
		t.Error("rm did not delete the post")
	}
}

func TestForumUserFallsBackThroughTheEnvironment(t *testing.T) {
	forumEnv(t)
	if _, err := forumUser(""); err == nil {
		t.Error("with no name anywhere, posting should refuse rather than invent one")
	}
	t.Setenv("BERMUDA_JOB_ID", "rt-template-daily")
	if got, err := forumUser(""); err != nil || got != "rt-template-daily" {
		t.Errorf("job id fallback: %q, %v", got, err)
	}
	t.Setenv("BERMUDA_FORUM_USER", "raphael")
	if got, err := forumUser(""); err != nil || got != "raphael" {
		t.Errorf("forum user beats the job id: %q, %v", got, err)
	}
	if got, err := forumUser("  june  "); err != nil || got != "june" {
		t.Errorf("--as beats everything: %q, %v", got, err)
	}
}

func TestForumLeadSplitsPositionalsFromFlags(t *testing.T) {
	cases := []struct {
		argv       []string
		lead, rest int
	}{
		{[]string{"p123", "--as", "june"}, 1, 2},
		{[]string{"--as", "june", "p123"}, 0, 3},
		{[]string{"two", "words", "--board", "ops"}, 2, 2},
		{nil, 0, 0},
	}
	for _, c := range cases {
		lead, rest := forumLead(c.argv)
		if len(lead) != c.lead || len(rest) != c.rest {
			t.Errorf("forumLead(%v) = %v, %v", c.argv, lead, rest)
		}
	}
}

func TestParseForumSinceReadsDaysAndDates(t *testing.T) {
	if _, err := parseForumSince("nonsense"); err == nil {
		t.Error("an unreadable --since should be an error, not silently zero")
	}
	if got, _ := parseForumSince(""); got != 0 {
		t.Errorf("empty --since = %d, want 0", got)
	}
	// Days are not a Go duration unit, and a forum is read in days.
	got, err := parseForumSince("2d")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(-48 * time.Hour).Unix()
	if got < want-5 || got > want+5 {
		t.Errorf("--since 2d = %d, want about %d", got, want)
	}
	if _, err := parseForumSince("2026-08-16"); err != nil {
		t.Errorf("plain date: %v", err)
	}
	if _, err := parseForumSince("90m"); err != nil {
		t.Errorf("duration: %v", err)
	}
}

func TestForumWebServesBoardsThreadsAndSearch(t *testing.T) {
	s := forumEnv(t)
	ctx := context.Background()
	root, err := s.ForumCreate(ctx, store.ForumPost{Board: "ops", Author: "raphael",
		Title: "browser claim stuck", Body: "chromium-cdp died holding the claim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForumCreate(ctx, store.ForumPost{ParentID: root.ID, Author: "june", Body: "confirmed here too"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(forumHandler(s))
	t.Cleanup(srv.Close)

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 64<<10)
		n, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:n])
	}

	for _, c := range []struct{ path, want string }{
		{"/", "browser claim stuck"},
		{"/b/ops", "browser claim stuck"},
		{"/p/" + root.ID, "confirmed here too"},
		{"/search?q=chromium", "chromium"},
	} {
		code, body := get(c.path)
		if code != http.StatusOK {
			t.Errorf("GET %s = %d", c.path, code)
			continue
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("GET %s does not mention %q", c.path, c.want)
		}
	}
	if code, _ := get("/p/nope"); code != http.StatusNotFound {
		t.Errorf("unknown post = %d, want 404", code)
	}
	if code, _ := get("/nowhere"); code != http.StatusNotFound {
		t.Errorf("unknown page = %d, want 404", code)
	}
	// Read-only means read-only: there is nothing to submit, and a POST must
	// not become a write path by accident.
	resp, err := http.Post(srv.URL+"/", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	before, err := s.ForumList(ctx, store.ForumQuery{Deleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Errorf("posts = %d, want the two written by the store", len(before))
	}
}

func TestForumWebEscapesPostText(t *testing.T) {
	s := forumEnv(t)
	if _, err := s.ForumCreate(context.Background(), store.ForumPost{
		Board: "ops", Author: "raphael", Title: "xss", Body: `<script>alert(1)</script>`,
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(forumHandler(s))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 64<<10)
	n, _ := resp.Body.Read(b)
	// Post bodies are written by whatever agent felt like posting, so the page
	// has to treat them as text.
	if strings.Contains(string(b[:n]), "<script>alert(1)</script>") {
		t.Error("post body reached the page unescaped")
	}
}
