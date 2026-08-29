package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests are about the four claim properties that decide whether the
// thread is a coordination primitive or a decoration: contention is resolved,
// leases lapse when read rather than when swept, releasing what you do not
// hold fails, and re-claiming extends instead of deadlocking.
//
// Time is injected everywhere it can be, so expiry is proven at the instant it
// matters rather than approximated by sleeping and hoping.

func newThread(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var (
	alice = Identity{Name: "alice"}
	bob   = Identity{Name: "bob"}
)

// base is a fixed instant, so a test's arithmetic reads as intent rather than as
// whatever the clock happened to say.
var base = time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)

func TestClaimTakesAFreeResource(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	c, err := s.ThreadClaim(ctx, ClaimRequest{
		Resource: "browser", By: alice, Why: "tiktok signup",
		TTL: 20 * time.Minute, Now: base})
	if err != nil {
		t.Fatal(err)
	}
	if c.ExpiresAt == nil || !c.ExpiresAt.Equal(base.Add(20*time.Minute)) {
		t.Errorf("lease ends at %v, want %v", c.ExpiresAt, base.Add(20*time.Minute))
	}

	claims, err := s.ThreadClaims(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Resource != "browser" {
		t.Fatalf("claims = %+v, want one on browser", claims)
	}
	if claims[0].Why != "tiktok signup" {
		t.Errorf("why is %q: a claim nobody can explain is a claim nobody can resolve", claims[0].Why)
	}
}

// Contention is the whole point: two agents wanting the browser is the case
// that took the host down.
func TestClaimRefusesAHeldResourceAndNamesTheHolder(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		Why: "tiktok signup", TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}

	_, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: bob,
		TTL: 5 * time.Minute, Now: base.Add(time.Minute)})
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second claim returned %v, want a HeldError", err)
	}
	if held.Claim.Holder.Name != "alice" {
		t.Errorf("error names %q as the holder, want alice", held.Claim.Holder.Name)
	}
	// The message has to be enough to act on without another command.
	for _, want := range []string{"browser", "alice", "in 19m", "tiktok signup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// The refusal must not have written a claim.
	claims, _ := s.ThreadClaims(ctx, base.Add(time.Minute))
	if len(claims) != 1 || claims[0].Holder.Name != "alice" {
		t.Errorf("claims after a refused attempt = %+v, want alice's alone", claims)
	}
}

// A killed agent must not hold the browser forever, and nothing is running to
// notice that it died. Expiry therefore has to happen in the read.
func TestLeasesExpireAtReadTimeWithNothingRunning(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}

	// One second before the lease ends it is still held.
	if claims, _ := s.ThreadClaims(ctx, base.Add(20*time.Minute-time.Second)); len(claims) != 1 {
		t.Fatalf("lease lapsed early: %+v", claims)
	}
	// At the moment it ends, it is gone — no sweeper involved.
	claims, err := s.ThreadClaims(ctx, base.Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("expired lease still held: %+v", claims)
	}
	// And somebody else can take it.
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: bob,
		TTL: time.Minute, Now: base.Add(21 * time.Minute)}); err != nil {
		t.Fatalf("expired lease blocked the next agent: %v", err)
	}
}

// A claim with no TTL is legitimate for a deliberate long hold, and must not be
// silently treated as expired.
func TestAClaimWithNoTTLNeverLapses(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "main-checkout", By: alice, Now: base}); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ThreadClaims(ctx, base.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("an unbounded lease lapsed after a month: %+v", claims)
	}
	if got := claims[0].ExpiryLabel(base); got != "no expiry" {
		t.Errorf("expiry label is %q, want no expiry", got)
	}
}

// Re-claiming what you already hold is how a job that needs longer says so. It
// must extend, not deadlock against itself.
func TestReclaimExtendsInsteadOfDeadlocking(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		Why: "tiktok signup", TTL: 10 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	at := base.Add(9 * time.Minute)
	c, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: 10 * time.Minute, Now: at})
	if err != nil {
		t.Fatalf("re-claiming my own lease failed: %v", err)
	}
	if !c.ExpiresAt.Equal(at.Add(10 * time.Minute)) {
		t.Errorf("extended lease ends at %v, want %v", c.ExpiresAt, at.Add(10*time.Minute))
	}
	// An extension with no new reason keeps the old one.
	if c.Why != "tiktok signup" {
		t.Errorf("extension lost the reason: %q", c.Why)
	}
	// The hold is one hold, not two, and it dates from when it started.
	claims, _ := s.ThreadClaims(ctx, at)
	if len(claims) != 1 {
		t.Fatalf("extension produced %d claims, want 1", len(claims))
	}
	if !claims[0].Since.Equal(base) {
		t.Errorf("held since %v after an extension, want the original %v: "+
			"a renewed lease must not look new", claims[0].Since, base)
	}
	// The lease really did move.
	if claims, _ := s.ThreadClaims(ctx, base.Add(11*time.Minute)); len(claims) != 1 {
		t.Error("the extension did not outlive the original lease")
	}
}

// Taking a resource back after your own lease lapsed is a new hold, not the old
// one continuing. Reporting it as one hold would overstate it by however long
// nobody had the resource at all.
func TestALapseBreaksTheHoldEvenForTheSameAgent(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: 10 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	// An hour later — long lapsed — alice takes it again.
	retaken := base.Add(time.Hour)
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: 10 * time.Minute, Now: retaken}); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ThreadClaims(ctx, retaken)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims = %+v, want one", claims)
	}
	if !claims[0].Since.Equal(retaken) {
		t.Errorf("held since %v, want %v: the lease lapsed in between, so this is a new hold",
			claims[0].Since, retaken)
	}
}

// A silent no-op that reports success is the bug class this repo has been
// fixing; releasing what nobody holds must say so.
func TestReleaseWithoutAHoldFails(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	_, err := s.ThreadRelease(ctx, "browser", alice, base)
	var notHeld *NotHeldError
	if !errors.As(err, &notHeld) {
		t.Fatalf("releasing an unheld resource returned %v, want a NotHeldError", err)
	}
	if notHeld.Current != nil {
		t.Errorf("nobody holds browser, but the error names %v", notHeld.Current)
	}
}

func TestReleaseCannotStealSomebodyElsesLease(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ThreadRelease(ctx, "browser", bob, base.Add(time.Minute))
	var notHeld *NotHeldError
	if !errors.As(err, &notHeld) {
		t.Fatalf("bob released alice's lease: %v", err)
	}
	if notHeld.Current == nil || notHeld.Current.Holder.Name != "alice" {
		t.Errorf("error should name alice as the holder, got %+v", notHeld.Current)
	}
	// Alice still has it.
	claims, _ := s.ThreadClaims(ctx, base.Add(time.Minute))
	if len(claims) != 1 || claims[0].Holder.Name != "alice" {
		t.Errorf("claims = %+v, want alice still holding", claims)
	}
}

// An expired lease is not held, so releasing it fails — which is how a job
// learns it outlived its own claim rather than assuming it did not.
func TestReleasingAnExpiredLeaseFails(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ThreadRelease(ctx, "browser", alice, base.Add(2*time.Minute))
	var notHeld *NotHeldError
	if !errors.As(err, &notHeld) {
		t.Fatalf("releasing a lapsed lease returned %v, want a NotHeldError", err)
	}
	if notHeld.Current != nil {
		t.Errorf("a lapsed lease should leave nobody holding it, got %+v", notHeld.Current)
	}
}

func TestReleaseFreesTheResourceForTheNextAgent(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadRelease(ctx, "browser", alice, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if claims, _ := s.ThreadClaims(ctx, base.Add(time.Minute)); len(claims) != 0 {
		t.Fatalf("release left %+v held", claims)
	}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: bob,
		TTL: time.Minute, Now: base.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("bob could not take a released resource: %v", err)
	}
	// And the hold dates from bob's claim, not from alice's.
	claims, _ := s.ThreadClaims(ctx, base.Add(2*time.Minute))
	if len(claims) != 1 || !claims[0].Since.Equal(base.Add(2*time.Minute)) {
		t.Errorf("claims = %+v, want bob holding since %v", claims, base.Add(2*time.Minute))
	}
}

// Two runs of one job are two holders. If they were not, one could release the
// other's lease and neither would ever know.
func TestRunsOfTheSameJobAreDifferentHolders(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	first := Identity{Name: "scrape-daily", JobID: "scrape-daily", RunID: "run-1"}
	second := Identity{Name: "scrape-daily", JobID: "scrape-daily", RunID: "run-2"}

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: first,
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: second,
		TTL: 20 * time.Minute, Now: base}); err == nil {
		t.Error("a second run of the same job took a lease its sibling holds")
	}
	if _, err := s.ThreadRelease(ctx, "browser", second, base); err == nil {
		t.Error("a second run of the same job released its sibling's lease")
	}
}

// The incident this field exists for. Two interactive agents both called
// `ada` had nothing else to tell them apart, so one released the other's
// live 45-minute lease and was told it had succeeded. Neither was told anything
// was wrong.
func TestInteractiveAgentsOfTheSameNameAreDifferentHolders(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	first := Identity{Name: "ada", PID: "5095"}
	second := Identity{Name: "ada", PID: "7421"}

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: first,
		Why: "jlpt scrape", TTL: 45 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: second,
		TTL: 45 * time.Minute, Now: base}); err == nil {
		t.Error("a second interactive ada took a lease the first one holds")
	}
	_, err := s.ThreadRelease(ctx, "browser", second, base)
	if err == nil {
		t.Fatal("a second interactive ada released the first one's lease")
	}
	// The refusal has to name which ada holds it, or the agent that just
	// lost has no way to work out who to go and ask.
	if !strings.Contains(err.Error(), "ada#5095") {
		t.Errorf("the refusal says %q, and should name the holder's pid", err)
	}
	// And the real holder is unaffected: a refused release must not have
	// written anything.
	if _, err := s.ThreadRelease(ctx, "browser", first, base); err != nil {
		t.Errorf("the agent that took the lease could not release it: %v", err)
	}
}

// The other half of the same rule. One agent runs many commands, and every one
// of them has to be recognised as the same holder — a claim its own release
// could not match would be a lease nothing could ever give back.
func TestOneAgentIsOneHolderAcrossCommands(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	me := Identity{Name: "ada", PID: "5095"}
	again := Identity{Name: "ada", PID: "5095"}

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: me,
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	// Re-claiming extends rather than deadlocking, which only works if the two
	// identities compare equal.
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: again,
		TTL: 20 * time.Minute, Now: base.Add(time.Minute)}); err != nil {
		t.Errorf("the same agent could not extend its own lease: %v", err)
	}
	if _, err := s.ThreadRelease(ctx, "browser", again, base.Add(2*time.Minute)); err != nil {
		t.Errorf("the same agent could not release its own lease: %v", err)
	}
}

// Every claim and message in the live database predates this field. If a
// pid-less identity matched nothing, those claims would be unreleasable
// forever — a resource held by a row nobody can hand back.
func TestAnIdentityWithNoPIDStillMatchesByName(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	legacy := Identity{Name: "ada"}
	now := Identity{Name: "ada", PID: "5095"}

	// A claim taken before pids existed, released by an agent that has one.
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: legacy,
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadRelease(ctx, "browser", now, base); err != nil {
		t.Errorf("a pid-carrying agent could not release a pre-pid claim: %v", err)
	}
	// And the other direction: an old script with no pid, releasing a claim
	// taken by an agent that had one.
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "gpu", By: now,
		TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadRelease(ctx, "gpu", legacy, base); err != nil {
		t.Errorf("a pid-less caller could not release a claim carrying a pid: %v", err)
	}
}

// Concurrency is the case a single-threaded test cannot reach: without an
// IMMEDIATE transaction both racers read a free resource and both write a claim.
func TestConcurrentClaimsProduceExactlyOneWinner(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := s.ThreadClaim(ctx, ClaimRequest{
				Resource: "browser",
				By:       Identity{Name: "agent", RunID: string(rune('a' + n))},
				TTL:      time.Minute,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
				return
			}
			var held *HeldError
			if !errors.As(err, &held) {
				t.Errorf("racer %d failed for the wrong reason: %v", n, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if won != 1 {
		t.Fatalf("%d of %d racers took the browser, want exactly 1", won, racers)
	}
	claims, err := s.ThreadClaims(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("the thread records %d live claims on browser, want 1", len(claims))
	}
}

// Waiting is what an agent does instead of giving up, and it has to actually
// acquire once the holder lets go.
func TestClaimWaitAcquiresOnceTheHolderReleases(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice, TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		if _, err := s.ThreadRelease(ctx, "browser", alice, time.Time{}); err != nil {
			t.Error(err)
		}
	}()

	c, err := s.ThreadClaimWait(ctx, ClaimRequest{Resource: "browser", By: bob,
		TTL: time.Minute}, 5*time.Second)
	if err != nil {
		t.Fatalf("waiting claim never acquired: %v", err)
	}
	if c.Holder.Name != "bob" {
		t.Errorf("claim went to %s, want bob", c.Holder.Name)
	}
}

func TestClaimWaitGivesUpAndStillNamesTheHolder(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice, TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ThreadClaimWait(ctx, ClaimRequest{Resource: "browser", By: bob,
		TTL: time.Minute}, 300*time.Millisecond)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("a timed-out wait returned %v, want a HeldError naming alice", err)
	}
}

// Bermuda routinely has three processes on one database — the daemon, the
// board, and whichever agent is running a thread command — so opening the store
// must never disturb a read already in flight. An earlier version rebuilt a SQL
// view on every open, and a concurrent claim failed with "no such table" while
// the window was open.
func TestOpeningTheStoreDoesNotDisturbALiveReader(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	ctx := context.Background()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice, TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Another process arriving, over and over, while the reader works.
		for {
			select {
			case <-stop:
				return
			default:
			}
			other, err := Open(dir)
			if err != nil {
				t.Error(err)
				return
			}
			other.Close()
		}
	}()

	for i := 0; i < 200; i++ {
		if _, err := s.ThreadClaims(ctx, time.Time{}); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("a concurrent open broke a read of the claims: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The log is append-only: a released claim is still in it, because the history
// of who held what is the point.
func TestTheLogKeepsEverythingAndReadsOldestFirst(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindEvent, By: alice,
		Body: "removed camoufox", CreatedAt: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ThreadRelease(ctx, "browser", alice, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	log, err := s.ThreadLog(ctx, ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []ThreadKind{KindClaim, KindEvent, KindRelease}
	if len(log) != len(want) {
		t.Fatalf("log has %d messages, want %d", len(log), len(want))
	}
	for i, k := range want {
		if log[i].Kind != k {
			t.Errorf("log[%d] is %s, want %s (a thread reads oldest-first)", i, log[i].Kind, k)
		}
	}
}

func TestLogFiltersBySinceAndKind(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	for i, body := range []string{"old note", "new note"} {
		if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindNote, By: alice,
			Body: body, CreatedAt: base.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindEvent, By: alice,
		Body: "installed gog v0.34.1", CreatedAt: base.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	recent, err := s.ThreadLog(ctx, ThreadFilter{Since: base.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("--since kept %d messages, want 2", len(recent))
	}
	events, err := s.ThreadLog(ctx, ThreadFilter{Kinds: []ThreadKind{KindEvent}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != KindEvent {
		t.Fatalf("--kind event returned %+v", events)
	}
}

// The limit takes the newest N, not the oldest: an append-only table grows
// forever, and the first page of it stops being interesting immediately.
func TestLogLimitKeepsTheNewest(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindNote, By: alice,
			Body: string(rune('a' + i)), CreatedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	log, err := s.ThreadLog(ctx, ThreadFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0].Body != "d" || log[1].Body != "e" {
		t.Fatalf("limit returned %+v, want the last two in order", log)
	}
}

// The read window.
//
// Reading the thread costs an agent context, and most of an append-only log is
// settled history by the time anybody reads it. These tests pin the two bounds
// that decide how much gets spent: the default a caller gets for asking for
// nothing, and the ceiling it gets however much it asks for.

// fillLog posts n notes a minute apart, the last of them at newest.
func fillLog(t *testing.T, s *Store, n int, newest time.Time) {
	t.Helper()
	ctx := context.Background()
	for i := n - 1; i >= 0; i-- {
		if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindNote, By: alice,
			Body:      fmt.Sprintf("note %d", i),
			CreatedAt: newest.Add(-time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheDefaultWindowIsFiftyMessagesOfOneDay(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	fillLog(t, s, 60, base)                     // the last hour
	fillLog(t, s, 5, base.Add(-3*24*time.Hour)) // and last week's

	w := ThreadReadWindow(0, 0, base)
	if w.Limit != ThreadDefaultLimit || w.Age != ThreadDefaultAge {
		t.Fatalf("default window is %d/%s, want %d/%s",
			w.Limit, w.Age, ThreadDefaultLimit, ThreadDefaultAge)
	}
	if w.LimitClamped || w.AgeClamped {
		t.Errorf("asking for nothing clamped something: %+v", w)
	}
	if !w.Since.Equal(base.Add(-ThreadDefaultAge)) {
		t.Errorf("window reaches back to %v, want %v", w.Since, base.Add(-ThreadDefaultAge))
	}

	log, err := s.ThreadLog(ctx, w.Apply(ThreadFilter{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != ThreadDefaultLimit {
		t.Fatalf("default read returned %d messages, want %d", len(log), ThreadDefaultLimit)
	}
	for _, m := range log {
		if m.CreatedAt.Before(w.Since) {
			t.Fatalf("message from %v is older than the window's %v", m.CreatedAt, w.Since)
		}
	}
	// Newest last, so the eye finishes on the thing that just happened.
	if log[len(log)-1].Body != "note 0" {
		t.Errorf("log ends on %q, want the newest message", log[len(log)-1].Body)
	}
}

// The age bound on its own: few enough messages that the limit never comes into
// it, and the old ones still stay out.
func TestTheAgeBoundBitesOnItsOwn(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	fillLog(t, s, 6, base)
	fillLog(t, s, 4, base.Add(-30*time.Hour))

	f := ThreadReadWindow(0, 0, base).Apply(ThreadFilter{})
	log, err := s.ThreadLog(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 6 {
		t.Fatalf("the day's window returned %d messages, want the 6 inside it", len(log))
	}
	// What the notice is built from: everything the window kept out is still
	// there to be counted, so a truncated log can say how much it is not showing.
	in, err := s.ThreadCount(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.ThreadCount(ctx, ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if in != 6 || all != 10 {
		t.Fatalf("counted %d in the window of %d total, want 6 of 10", in, all)
	}
}

// The limit on its own: everything is minutes old, so only the count bites.
func TestTheLimitBitesOnItsOwn(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	fillLog(t, s, 60, base)

	f := ThreadReadWindow(0, 0, base).Apply(ThreadFilter{})
	log, err := s.ThreadLog(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != ThreadDefaultLimit {
		t.Fatalf("returned %d messages, want the newest %d", len(log), ThreadDefaultLimit)
	}
	in, err := s.ThreadCount(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if in != 60 {
		t.Fatalf("counted %d messages in the window, want all 60: the count ignores the limit", in)
	}
}

// Asking for more than the ceiling is not an error — a script passing a generous
// --limit is not doing anything wrong — it just cannot have it.
func TestTheCeilingClampsRatherThanFails(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	fillLog(t, s, 260, base)

	w := ThreadReadWindow(1000, 30*24*time.Hour, base)
	if w.Limit != ThreadMaxLimit || !w.LimitClamped {
		t.Errorf("--limit 1000 resolved to %d (clamped=%v), want %d clamped",
			w.Limit, w.LimitClamped, ThreadMaxLimit)
	}
	if w.Age != ThreadMaxAge || !w.AgeClamped {
		t.Errorf("--since 30d resolved to %s (clamped=%v), want %s clamped",
			w.Age, w.AgeClamped, ThreadMaxAge)
	}
	log, err := s.ThreadLog(ctx, w.Apply(ThreadFilter{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != ThreadMaxLimit {
		t.Fatalf("a clamped read returned %d messages, want the ceiling %d", len(log), ThreadMaxLimit)
	}
	// The ceiling is the store's, not the flag parser's: going round the CLI
	// with an enormous filter must not buy an unbounded read either.
	direct, err := s.ThreadLog(ctx, ThreadFilter{Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(direct) != ThreadMaxLimit {
		t.Fatalf("ThreadLog honoured a limit of 100000 with %d messages, want %d",
			len(direct), ThreadMaxLimit)
	}
}

// A caller that wants less than the default gets less. The window is a bound on
// how much can be read, not an instruction to read that much.
func TestANarrowerRequestIsHonoured(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	fillLog(t, s, 60, base)

	w := ThreadReadWindow(5, 0, base)
	if w.Limit != 5 || w.LimitClamped {
		t.Fatalf("--limit 5 resolved to %d (clamped=%v), want 5 unclamped", w.Limit, w.LimitClamped)
	}
	log, err := s.ThreadLog(ctx, w.Apply(ThreadFilter{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 5 || log[4].Body != "note 0" {
		t.Fatalf("--limit 5 returned %d messages ending on %+v, want the newest 5", len(log), log)
	}
	if narrow := ThreadReadWindow(0, 10*time.Minute, base); narrow.Age != 10*time.Minute ||
		narrow.AgeClamped {
		t.Errorf("--since 10m resolved to %s (clamped=%v), want it left alone",
			narrow.Age, narrow.AgeClamped)
	}
}

func TestPostRejectsWhatTheThreadIsNotFor(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	// Claims carry semantics, so they cannot be written as plain messages.
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindClaim, By: alice,
		Resource: "browser", Body: "mine"}); err == nil {
		t.Error("a claim was written without checking whether the resource was free")
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: "chat", By: alice, Body: "hi"}); err == nil {
		t.Error("an unknown kind was accepted: the thread is not a chat channel")
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindNote, Body: "anonymous"}); err == nil {
		t.Error("a message with no author was accepted")
	}
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindNote, By: alice, Body: "  "}); err == nil {
		t.Error("an empty note was accepted")
	}
}

func TestClaimRejectsWhatCannotBeAttributed(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", TTL: time.Minute}); err == nil {
		t.Error("an anonymous claim was accepted: nobody could be asked to release it")
	}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{By: alice, TTL: time.Minute}); err == nil {
		t.Error("a claim on nothing was accepted")
	}
}

// The error text is the whole interface for an agent that just lost a race, so
// it has to say what happened and who to go and find without another command.
func TestRefusalMessagesAreActionable(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	holder := Identity{Name: "scrape-daily", JobID: "scrape-daily", RunID: "run-1"}
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: holder,
		Why: "deal scrape", TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}

	_, err := s.ThreadRelease(ctx, "browser", bob, base)
	for _, want := range []string{"browser", "bob", "scrape-daily", "run-1", "in 20m"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("release refusal %q does not mention %q", err, want)
		}
	}

	_, err = s.ThreadRelease(ctx, "gpu", bob, base)
	if !strings.Contains(err.Error(), "expired lease") {
		t.Errorf("releasing an unheld resource says %q, and should name the usual cause", err)
	}
}

func TestIdentityNamesTheRunWhenItHasOne(t *testing.T) {
	for _, tc := range []struct {
		id   Identity
		want string
	}{
		{Identity{Name: "alice"}, "alice"},
		{Identity{Name: "alice", JobID: "alice"}, "alice"},
		{Identity{Name: "brief", JobID: "daily-brief"}, "brief (daily-brief)"},
		{Identity{Name: "brief", JobID: "daily-brief", RunID: "r7"}, "brief (r7)"},
		// The pid is the only thing distinguishing two interactive agents, so
		// it is rendered wherever they are named.
		{Identity{Name: "ada", PID: "5095"}, "ada#5095"},
	} {
		if got := tc.id.String(); got != tc.want {
			t.Errorf("%+v renders as %q, want %q", tc.id, got, tc.want)
		}
	}
}

// Short is what the board's narrow HOLDS column prints. It must still carry the
// pid: the block exists to tell a blocked reader which agent to go and ask, and
// a bare `ada` does not answer that when three are running.
func TestShortCarriesThePIDAndNotTheRun(t *testing.T) {
	for _, tc := range []struct {
		id   Identity
		want string
	}{
		{Identity{Name: "ada"}, "ada"},
		{Identity{Name: "ada", PID: "5095"}, "ada#5095"},
		{Identity{Name: "brief", JobID: "daily-brief", RunID: "r7"}, "brief"},
	} {
		if got := tc.id.Short(); got != tc.want {
			t.Errorf("%+v renders short as %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestShortDurationReadsTheWayALeaseIsAskedAbout(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{time.Second, "1s"},
		// Expiries are stored to the second, so a fresh lease is always a shade
		// short of what was asked for and must not report as one unit less.
		{20*time.Minute - time.Millisecond, "20m"},
		{59 * time.Second, "59s"},
		{90 * time.Second, "2m"},
		{59*time.Minute + 45*time.Second, "1h"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
	} {
		if got := ShortDuration(tc.in); got != tc.want {
			t.Errorf("ShortDuration(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseThreadKind(t *testing.T) {
	for _, k := range ThreadKinds {
		if got, err := ParseThreadKind(string(k)); err != nil || got != k {
			t.Errorf("ParseThreadKind(%q) = %q, %v", k, got, err)
		}
	}
	if _, err := ParseThreadKind("gossip"); err == nil {
		t.Error("ParseThreadKind accepted a kind the thread does not have")
	}
}

func TestThreadClaimOfReportsOneResource(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		TTL: 10 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}
	c, err := s.ThreadClaimOf(ctx, "browser", base)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.Holder.Name != "alice" {
		t.Fatalf("claim on browser = %+v, want alice", c)
	}
	free, err := s.ThreadClaimOf(ctx, "gpu", base)
	if err != nil {
		t.Fatal(err)
	}
	if free != nil {
		t.Errorf("gpu reports %+v, want nobody", free)
	}
}

// The rename from "room" to "thread" ran against a live database with real
// messages in it, so the only thing that matters about the migration is that
// nothing was lost. A rename that dropped and recreated the table would pass
// every other test in this file and still silently destroy the record the whole
// feature exists to keep.
func TestTheRenameCarriesEveryOldMessageAcross(t *testing.T) {
	dir := t.TempDir()
	seedRoomSchema(t, dir)

	s := open(t, dir)
	log, err := s.ThreadLog(context.Background(), ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 3 {
		t.Fatalf("the migrated thread holds %d messages, want the 3 that were in room_messages", len(log))
	}
	// Every column, not just the count: a migration that copied rows through a
	// column list in the wrong order would still be the right length.
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
	}
	// The claim was live when the rename happened, and a lease that lapsed
	// because its expiry did not survive is a resource silently handed to
	// whoever asks next.
	claims, err := s.ThreadClaims(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Holder.Name != "tiktok-agent" {
		t.Fatalf("the migrated database reports claims %+v, want browser still held by tiktok-agent", claims)
	}
	if claims[0].ExpiresAt == nil || !claims[0].ExpiresAt.Equal(base.Add(20*time.Minute)) {
		t.Errorf("the surviving claim expires at %v, want the lease it was written with", claims[0].ExpiresAt)
	}
}

// Bermuda opens this database from the daemon, the board, and whichever agent
// is running a thread command, so every open re-runs the migration. The second
// one must find nothing to do rather than failing on a table that is already
// gone, or the board would refuse to start whenever the daemon opened first.
func TestTheRenameIsSafeToRunTwice(t *testing.T) {
	dir := t.TempDir()
	seedRoomSchema(t, dir)

	first := open(t, dir)
	second := open(t, dir)
	for _, s := range []*Store{first, second} {
		log, err := s.ThreadLog(context.Background(), ThreadFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(log) != 3 {
			t.Fatalf("a re-opened store holds %d messages, want 3", len(log))
		}
	}
	// The old table must be gone rather than left beside the new one. Two
	// tables means two answers to "what happened", and the next reader has no
	// way to tell which one is being appended to.
	var leftovers int
	if err := second.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'room_messages'`).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Error("room_messages is still there after the migration")
	}
	// Both index names would be maintained on every insert if the rename left
	// the old ones behind, on a table that is only ever appended to.
	var indexes int
	if err := second.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'room_messages%'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Errorf("%d pre-rename indexes survived, and every append still pays for them", indexes)
	}
}

// A database that never had a room_messages table is the common case — every
// machine that installs bermuda after the rename — and it must not be treated
// as a half-migrated one.
func TestAFreshDatabaseNeedsNoRename(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()
	if _, err := s.ThreadPost(ctx, ThreadMessage{Kind: KindNote, By: alice, Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	log, err := s.ThreadLog(ctx, ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 {
		t.Fatalf("a fresh store holds %d messages, want the 1 that was posted", len(log))
	}
}

// seedRoomSchema writes a database in the shape bermuda shipped before the
// rename: a room_messages table, its two indexes, and the kind of traffic a
// month of agents leaves in one.
func seedRoomSchema(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "bermuda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE room_messages (
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
CREATE INDEX room_messages_created ON room_messages(created_at);
CREATE INDEX room_messages_resource ON room_messages(resource, seq);
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
			INSERT INTO room_messages (kind, agent, job_id, run_id, resource, body, expires_at, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			r.kind, r.agent, r.job, r.run, r.resource, r.body, r.expires, base.Unix()); err != nil {
			t.Fatal(err)
		}
	}
}

// TTL is what the thread log and the board print beside a claim, and both gate
// on it being positive. A forever-hold must therefore come back as exactly 0:
// anything else would print a lease on the one claim that has none, which is
// precisely the hold a human needs to notice.
func TestTTLIsWhatTheClaimAskedForAndZeroWhenItAskedForever(t *testing.T) {
	cases := []struct {
		name string
		msg  ThreadMessage
		want time.Duration
	}{
		{"a lease", ThreadMessage{CreatedAt: base,
			ExpiresAt: ptr(base.Add(20 * time.Minute))}, 20 * time.Minute},
		{"no expiry at all", ThreadMessage{CreatedAt: base}, 0},
		// A lease that has already lapsed still reports what it asked for.
		// TTL is the length of the hold requested, not the time remaining, and
		// reading it as remaining would make an expired claim look unbounded.
		{"a lease already lapsed", ThreadMessage{CreatedAt: base,
			ExpiresAt: ptr(base.Add(-time.Minute))}, -time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.msg.TTL(); got != c.want {
				t.Errorf("TTL = %v, want %v", got, c.want)
			}
		})
	}
}

// The claim is written and read back through SQLite, where both instants are
// stored to the second. The TTL a reader prints has to be the one the claim was
// taken with — a round trip that shifted either instant would misreport every
// lease in the log.
func TestTTLSurvivesTheRoundTripThroughTheLog(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if _, err := s.ThreadClaim(ctx, ClaimRequest{Resource: "browser", By: alice,
		Why: "tiktok signup", TTL: 20 * time.Minute, Now: base}); err != nil {
		t.Fatal(err)
	}

	log, err := s.ThreadLog(ctx, ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var claim *ThreadMessage
	for i := range log {
		if log[i].Kind == KindClaim {
			claim = &log[i]
		}
	}
	if claim == nil {
		t.Fatal("the claim is not in the log")
	}
	if got := claim.TTL(); got != 20*time.Minute {
		t.Errorf("TTL read back as %v, want the 20m it was claimed for", got)
	}
}

func ptr(t time.Time) *time.Time { return &t }
