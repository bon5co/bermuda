package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests are about the two properties that make many threads safe to have
// at all: a message goes into exactly one conversation and stays there, and a
// claim does not, because there is still only one browser.
//
// The third is the migration. Every message written before threads existed has
// to still be readable, in a thread that the default command reads.

// Every unqualified write lands in global, so global existing cannot depend on
// anybody having created it. A fresh install whose first command is `thread
// post` would otherwise be refused for naming a thread it never named.
func TestGlobalIsThereBeforeAnybodyCreatesIt(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	threads, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != GlobalThread {
		t.Fatalf("a fresh store lists %+v, want global alone", threads)
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindNote, By: alice,
		Body: "hello"}); err != nil {
		t.Fatalf("posting without naming a thread failed: %v", err)
	}
	log, err := s.ThreadLog(ctx, ThreadFilter{Thread: GlobalThread})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 || log[0].Thread != GlobalThread {
		t.Errorf("an unqualified message landed as %+v, want it in global", log)
	}
}

// Deleting global would leave every unqualified command — and every message
// written before threads existed — writing into a thread that is not there.
func TestGlobalCannotBeDeleted(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	_, err := s.RemoveThread(ctx, GlobalThread, true)
	if err == nil {
		t.Fatal("global was deleted; every unqualified write now has nowhere to go")
	}
	// Force must not be a way round it either: --force exists to confirm losing
	// messages, not to confirm losing the default.
	if !strings.Contains(err.Error(), "global") {
		t.Errorf("the refusal reads %q, and does not say which thread it is about", err)
	}
	threads, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != GlobalThread {
		t.Errorf("threads after the refused delete = %+v, want global still there", threads)
	}
}

// A thread is the record of what is currently true. Losing one to a mistyped id
// is the deletion nobody notices until they go looking for something that used
// to be written down.
func TestDeletingAThreadWithMessagesNeedsForce(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.NewThread(ctx, "webapp", "the django saas"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{Thread: "webapp",
		Kind: KindNote, By: alice, Body: "stripe keys rotated"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.RemoveThread(ctx, "webapp", false); err == nil {
		t.Fatal("a thread holding messages was deleted without --force")
	}
	if log, _ := s.ThreadLog(ctx, ThreadFilter{Thread: "webapp"}); len(log) != 1 {
		t.Fatalf("the refused delete left %d messages, want the 1 that was posted", len(log))
	}

	n, err := s.RemoveThread(ctx, "webapp", true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the delete reported %d messages removed, want 1", n)
	}
	// The messages go with the thread. A row left behind belongs to a thread
	// nothing lists, so nothing would ever show it again.
	if log, _ := s.ThreadLog(ctx, ThreadFilter{}); len(log) != 0 {
		t.Errorf("%d messages outlived the thread they were in", len(log))
	}
	if exists, _ := s.ThreadExists(ctx, "webapp"); exists {
		t.Error("the thread is still listed after being removed")
	}
}

// An empty thread is a typo being cleaned up, and demanding --force for one
// trains people to pass it without reading.
func TestDeletingAnEmptyThreadNeedsNoForce(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.NewThread(ctx, "typo-thread", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveThread(ctx, "typo-thread", false); err != nil {
		t.Fatalf("removing an empty thread needed force: %v", err)
	}
}

// The whole point: two agents on different projects must not have to read each
// other's traffic to find their own.
func TestAThreadOnlyShowsItsOwnMessages(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	for _, id := range []string{"webapp", "tiktok-deals"} {
		if _, err := s.NewThread(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	posts := []ThreadMessage{
		{Thread: "webapp", Kind: KindNote, By: alice, Body: "stripe keys rotated"},
		{Thread: "tiktok-deals", Kind: KindEvent, By: bob, Body: "account created"},
		{Thread: GlobalThread, Kind: KindNote, By: alice, Body: "machine rebooted"},
	}
	for _, p := range posts {
		if _, err := s.ThreadPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		thread string
		want   string
	}{
		{"webapp", "stripe keys rotated"},
		{"tiktok-deals", "account created"},
		{GlobalThread, "machine rebooted"},
	} {
		log, err := s.ThreadLog(ctx, ThreadFilter{Thread: c.thread})
		if err != nil {
			t.Fatal(err)
		}
		if len(log) != 1 || log[0].Body != c.want {
			t.Errorf("%s holds %+v, want only %q", c.thread, log, c.want)
		}
	}
	// An unfiltered read is still every thread at once, which is what `thread
	// log --all` is for.
	all, err := s.ThreadLog(ctx, ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("an unfiltered log holds %d messages, want all 3", len(all))
	}
}

// Claims deliberately do not divide by thread. A per-thread lease would tell
// two agents in two conversations that the same browser was free, which is the
// two-browsers-at-once failure that took the host down.
func TestAClaimIsHeldInEveryThread(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	for _, id := range []string{"webapp", "tiktok-deals"} {
		if _, err := s.NewThread(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Thread: "webapp",
		Resource: "browser", By: alice, Why: "stripe dashboard",
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}

	// An agent working in a different conversation must be refused, and told
	// who has it — even though it can see nothing alice said.
	_, err := s.ThreadClaim(ctx, ClaimRequest{Thread: "tiktok-deals",
		Resource: "browser", By: bob, TTL: time.Minute, Now: base})
	if err == nil {
		t.Fatal("the browser was claimed twice from two threads")
	}
	if !strings.Contains(err.Error(), "alice") {
		t.Errorf("the refusal reads %q and does not name the holder", err)
	}
	// And the holds block is the same wherever it is read from: ThreadClaims
	// takes no thread at all, which is what makes that true by construction.
	claims, err := s.ThreadClaims(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Holder.Name != "alice" {
		t.Fatalf("claims = %+v, want the one alice took in webapp", claims)
	}
	// The message recording it is in one conversation, though: the claim is
	// everyone's business, the discussion around it is not.
	log, _ := s.ThreadLog(ctx, ThreadFilter{Thread: "tiktok-deals"})
	if len(log) != 0 {
		t.Errorf("tiktok-deals holds %+v, want alice's claim message left in her own thread", log)
	}
}

// A claim in one thread and its release in another leaves a claim that is never
// given back where anybody is reading, and a release for nothing where they are
// not. The pair has to stay together.
func TestAReleaseLandsBesideItsClaim(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.NewThread(ctx, "webapp", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Thread: "webapp",
		Resource: "browser", By: alice, TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadRelease(ctx, "browser", alice, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	log, err := s.ThreadLog(ctx, ThreadFilter{Thread: "webapp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[1].Kind != KindRelease {
		t.Fatalf("webapp holds %+v, want the claim and the release that answers it", log)
	}
	if global, _ := s.ThreadLog(ctx, ThreadFilter{Thread: GlobalThread}); len(global) != 0 {
		t.Errorf("global holds %+v, want the release to have stayed with its claim", global)
	}
}

// Auto-creating on a typo is the quiet failure: `--thread betterlingu` would
// succeed, and the agent reading webapp would never see the message or
// have any reason to think one existed.
func TestWritingIntoAThreadNobodyCreatedIsRefused(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	_, err := s.ThreadPost(ctx, ThreadMessage{Thread: "betterlingu",
		Kind: KindNote, By: alice, Body: "stripe keys rotated"})
	if err == nil {
		t.Fatal("a note was posted into a thread nobody created")
	}
	if !strings.Contains(err.Error(), "thread new") {
		t.Errorf("the refusal reads %q and does not say how to fix it", err)
	}
	// A claim is worse: the lease would be taken for real, and the only record
	// of it would be in a conversation nobody reads.
	_, err = s.ThreadClaim(ctx, ClaimRequest{Thread: "betterlingu",
		Resource: "browser", By: alice, TTL: time.Minute, Now: base})
	if err == nil {
		t.Fatal("a claim was recorded in a thread nobody created")
	}
	if claims, _ := s.ThreadClaims(ctx, base); len(claims) != 0 {
		t.Errorf("the refused claim still took the browser: %+v", claims)
	}
}

// Thread ids are typed by one agent and read by another. Slugging `Browser
// Work` into `browser-work` silently would put two agents one keystroke apart
// in two conversations, each seeing half of it.
func TestThreadIDsAreShapedLikeJobIDs(t *testing.T) {
	for _, ok := range []string{"global", "webapp", "tiktok-deals", "n3"} {
		if _, err := ParseThreadID(ok); err != nil {
			t.Errorf("ParseThreadID(%q) refused a legal id: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", "Betterlingo", "tiktok deals",
		"-leading", "trailing-", "double--hyphen", "under_score", "emoji🙂"} {
		if _, err := ParseThreadID(bad); err == nil {
			t.Errorf("ParseThreadID(%q) accepted it; two agents can now mean two things", bad)
		}
	}
	// Surrounding space is a copy-paste artefact, not a different thread.
	if got, err := ParseThreadID("  webapp\n"); err != nil || got != "webapp" {
		t.Errorf("ParseThreadID trimmed to %q, %v", got, err)
	}
}

// The picker chooses between conversations by how live they are, so the count
// and the last message have to be right — a thread nobody has written in must
// read as never, not as 1970.
func TestThreadListReportsSizeAndLastActivity(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.NewThread(ctx, "webapp", "the django saas"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewThread(ctx, "quiet", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.ThreadPost(ctx, ThreadMessage{Thread: "webapp",
			Kind: KindNote, By: alice, Body: "note", CreatedAt: base}); err != nil {
			t.Fatal(err)
		}
	}

	threads, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Global leads whatever it is called alphabetically: it is where an
	// unqualified command writes, so it is the one being looked for.
	if threads[0].ID != GlobalThread {
		t.Errorf("the list starts with %q, want global", threads[0].ID)
	}
	byID := map[string]Thread{}
	for _, th := range threads {
		byID[th.ID] = th
	}
	if got := byID["webapp"]; got.Messages != 2 || !got.LastAt.Equal(base) {
		t.Errorf("webapp reads as %d messages last at %v, want 2 at %v",
			got.Messages, got.LastAt, base)
	}
	if got := byID["webapp"].About; got != "the django saas" {
		t.Errorf("the description came back as %q", got)
	}
	if got := byID["quiet"]; got.Messages != 0 || !got.LastAt.IsZero() {
		t.Errorf("an unused thread reads as %d messages last at %v, want 0 and never",
			got.Messages, got.LastAt)
	}
}

// Starting a conversation that already exists usually means somebody wanted a
// fresh one. Handing them an ongoing one instead, silently, is how two projects
// end up sharing a thread again.
func TestCreatingTheSameThreadTwiceIsRefused(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.NewThread(ctx, "webapp", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewThread(ctx, "webapp", "second"); err == nil {
		t.Fatal("the same thread was created twice")
	}
	// And the description of the existing one is untouched: a refused create
	// must not half-apply.
	threads, _ := s.Threads(ctx)
	for _, th := range threads {
		if th.ID == "webapp" && th.About != "first" {
			t.Errorf("the description is now %q, want the original", th.About)
		}
	}
}

// The migration that matters. The live database has real messages in it,
// written before threads existed, and they have to survive into a thread that
// the default command reads — otherwise the record the whole feature keeps is
// there but invisible.
func TestMessagesFromBeforeThreadsLandInGlobal(t *testing.T) {
	dir := t.TempDir()
	seedPreThreadSchema(t, dir)

	s := open(t, dir)
	ctx := context.Background()

	log, err := s.ThreadLog(ctx, ThreadFilter{Thread: GlobalThread})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 {
		t.Fatalf("global holds %d messages, want the 3 that predate threads", len(log))
	}
	want := []ThreadMessage{
		{Seq: 1, Kind: KindEvent, By: Identity{Name: "setup-agent"}, Body: "removed camoufox"},
		{Seq: 2, Kind: KindClaim, By: Identity{Name: "tiktok-agent", JobID: "tiktok", RunID: "r1"},
			Resource: "browser", Body: "tiktok signup"},
		{Seq: 3, Kind: KindNote, By: Identity{Name: "handler"}, Body: "gog is gmail-read only"},
	}
	for i, w := range want {
		got := log[i]
		if got.Seq != w.Seq || got.Kind != w.Kind || got.By != w.By ||
			got.Resource != w.Resource || got.Body != w.Body {
			t.Errorf("message %d came across as %+v, want %+v", i, got, w)
		}
		if got.Thread != GlobalThread {
			t.Errorf("message %d landed in %q, want global", i, got.Thread)
		}
	}
	// The live claim has to still be a live claim: a lease that vanished in a
	// migration is a resource handed silently to whoever asks next.
	claims, err := s.ThreadClaims(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Holder.Name != "tiktok-agent" {
		t.Fatalf("claims after the migration = %+v, want browser still held", claims)
	}
	// And global is a thread anyone can list, not just a default string that
	// happens to be on every row.
	threads, err := s.Threads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != GlobalThread || threads[0].Messages != 3 {
		t.Errorf("the migrated database lists %+v, want global holding 3", threads)
	}
}

// The live database was written before identities carried a pid, so every row
// in it has none. Those claims still have to be releasable by the name that
// took them: a lease nothing can hand back holds the browser forever.
func TestClaimsFromBeforePIDsAreStillReleasable(t *testing.T) {
	dir := t.TempDir()
	seedPreThreadSchema(t, dir)

	s := open(t, dir)
	ctx := context.Background()

	// The same identity the pre-pid row was written under, plus the pid this
	// build stamps on everything. It has to match the row that has none.
	holder := Identity{Name: "tiktok-agent", JobID: "tiktok", RunID: "r1", PID: "5095"}
	if _, err := s.ThreadRelease(ctx, "browser", holder, base); err != nil {
		t.Fatalf("a pre-pid claim could not be released by the agent that took it: %v", err)
	}
	claims, err := s.ThreadClaims(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Errorf("claims after the release = %+v, want the browser free", claims)
	}
}

// Bermuda opens this database from the daemon, the board, and whichever agent
// is running a thread command, so every open re-runs the migration. A second
// open must find the column already there rather than failing on a duplicate —
// otherwise the board refuses to start whenever the daemon opened first.
func TestTheThreadColumnIsOnlyAddedOnce(t *testing.T) {
	dir := t.TempDir()
	seedPreThreadSchema(t, dir)

	first := open(t, dir)
	second := open(t, dir)
	for _, s := range []*Store{first, second} {
		log, err := s.ThreadLog(context.Background(), ThreadFilter{Thread: GlobalThread})
		if err != nil {
			t.Fatal(err)
		}
		if len(log) != 3 {
			t.Fatalf("a re-opened store holds %d messages in global, want 3", len(log))
		}
	}
}

// seedPreThreadSchema writes a database in the shape bermuda shipped between
// the room rename and threads: thread_messages with no thread column, and no
// registry at all.
func seedPreThreadSchema(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "bermuda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE thread_messages (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT NOT NULL,
  agent       TEXT NOT NULL,
  job_id      TEXT NOT NULL DEFAULT '',
  run_id      TEXT NOT NULL DEFAULT '',
  resource    TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  expires_at  INTEGER,
  created_at  INTEGER NOT NULL
);
CREATE INDEX thread_messages_created ON thread_messages(created_at);
CREATE INDEX thread_messages_resource ON thread_messages(resource, seq);
`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		kind, agent, job, run, resource, body string
		expires                               any
	}{
		{"event", "setup-agent", "", "", "", "removed camoufox", nil},
		{"claim", "tiktok-agent", "tiktok", "r1", "browser", "tiktok signup",
			base.Add(20 * time.Minute).Unix()},
		{"note", "handler", "", "", "", "gog is gmail-read only", nil},
	}
	for _, r := range rows {
		if _, err := db.Exec(`
			INSERT INTO thread_messages (kind, agent, job_id, run_id, resource, body, expires_at, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			r.kind, r.agent, r.job, r.run, r.resource, r.body, r.expires, base.Unix()); err != nil {
			t.Fatal(err)
		}
	}
}

// A claim is a message, and deleting a thread deletes its messages — so
// deleting a thread somebody took the browser from would delete the claim and
// the fold would read the browser as free. Two agents would then be driving one
// browser, which on this machine can take the host down, and nothing anywhere
// would have said a word. --force means "yes, delete the messages"; it does not
// mean "yes, break the lock".
func TestAThreadCannotBeDeletedWhileALeaseIsHeldFromIt(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.NewThread(ctx, "tiktok-deals", ""); err != nil {
		t.Fatal(err)
	}
	// Against the real clock rather than base: the delete asks whether the lease
	// is live *now*, which is the same read-time expiry every other claim query
	// applies.
	now := time.Now()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		Thread: "tiktok-deals", Why: "signup", TTL: time.Hour, Now: now}); err != nil {
		t.Fatal(err)
	}

	err := mustFail(t, s, "tiktok-deals", true)
	// The refusal has to name both, or the reader cannot act on it: which
	// resource to release, and who to go and ask.
	if !strings.Contains(err.Error(), "browser") || !strings.Contains(err.Error(), "alice") {
		t.Errorf("the refusal says %q, want the resource and the holder in it", err)
	}
	held, err := s.ThreadClaimOf(ctx, "browser", now)
	if err != nil || held == nil {
		t.Fatalf("the lease is %v (%v) after a refused delete, want it still held", held, err)
	}

	// Released, the thread deletes normally: the refusal is about the lock, not
	// about the thread ever having had one.
	if _, err := s.ThreadRelease(ctx, "browser", alice, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveThread(ctx, "tiktok-deals", true); err != nil {
		t.Fatalf("deleting a thread whose lease was given back failed: %v", err)
	}
}

// A lease that lapsed holds nothing — expiry is evaluated at read time and
// nothing swept it — so it must not block a delete forever. Otherwise one
// killed agent would make its thread permanently undeletable.
func TestALapsedLeaseDoesNotBlockTheDelete(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.NewThread(ctx, "dead-project", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "gpu", By: bob,
		Thread: "dead-project", TTL: 10 * time.Minute,
		Now: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveThread(ctx, "dead-project", true); err != nil {
		t.Fatalf("a thread whose only lease lapsed an hour ago refused to delete: %v", err)
	}
}

// A lease taken from another conversation is not this thread's to protect. The
// claim message lives where it was written, so deleting an unrelated thread
// leaves it exactly where it was.
func TestALeaseHeldFromElsewhereDoesNotBlockTheDelete(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	for _, id := range []string{"webapp", "tiktok-deals"} {
		if _, err := s.NewThread(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		Thread: "webapp", TTL: time.Hour, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveThread(ctx, "tiktok-deals", true); err != nil {
		t.Fatalf("a lease held from another thread blocked this one: %v", err)
	}
	held, err := s.ThreadClaimOf(ctx, "browser", now)
	if err != nil || held == nil {
		t.Fatalf("the lease is %v (%v), want it untouched", held, err)
	}
}

// mustFail deletes a thread expecting a refusal, and returns it.
func mustFail(t *testing.T, s *Store, id string, force bool) error {
	t.Helper()
	n, err := s.RemoveThread(context.Background(), id, force)
	if err == nil {
		t.Fatalf("removing %s deleted %d messages instead of refusing", id, n)
	}
	return err
}
