package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// What `thread log` says about its own window, and how it renders a message.
//
// The failure these guard against is the quiet one: a read that returns fewer
// messages than the thread holds and says nothing about it. An agent reads the
// last fifty lines, concludes it has the whole picture, and acts on a thread
// whose load-bearing message was number fifty-one. Nothing errors — the answer
// is simply wrong, and looks complete.

// The notice has to distinguish "your limit bit" from "your age bound bit",
// because they are widened with different flags. A reader told only "there is
// more" cannot tell which of --limit or --since would get it.
func TestLogWindowNoticeNamesWhichBoundCutTheReadShort(t *testing.T) {
	window := func(limit int, age time.Duration) store.ThreadWindow {
		return store.ThreadWindow{Limit: limit, Age: age}
	}
	cases := []struct {
		name                     string
		shown, inWindow, older   int
		w                        store.ThreadWindow
		wantEmpty                bool
		wantContains, wantAbsent []string
	}{{
		name: "nothing was left out",
		// The whole window fits and nothing sits behind it, so there is nothing
		// to say. A notice on every read is a line everyone learns to skip.
		shown: 12, inWindow: 12, older: 0, w: window(50, 24*time.Hour),
		wantEmpty: true,
	}, {
		name:  "the limit bit",
		shown: 50, inWindow: 130, older: 0, w: window(50, 24*time.Hour),
		wantContains: []string{"showing the last 50 of 130 messages in the last 24h", "--since/--limit to widen"},
		wantAbsent:   []string{"older than that"},
	}, {
		name:  "only the age bound bit",
		shown: 8, inWindow: 8, older: 40, w: window(50, time.Hour),
		wantContains: []string{"showing all 8 in the last 1h", "40 older than that"},
		wantAbsent:   []string{"showing the last"},
	}, {
		name:  "both bounds bit",
		shown: 50, inWindow: 90, older: 300, w: window(50, 24*time.Hour),
		wantContains: []string{"showing the last 50 of 90", "300 older than that"},
	}, {
		name: "at the ceiling there is nothing left to widen",
		// Advising --limit to a caller already at the ceiling is advice that
		// does nothing, and following it and seeing no change reads as a bug.
		shown: store.ThreadMaxLimit, inWindow: 900, older: 4000,
		w:            window(store.ThreadMaxLimit, store.ThreadMaxAge),
		wantContains: []string{"that is the ceiling (200 messages / 7d)"},
		wantAbsent:   []string{"to widen"},
	}, {
		name: "asking past the ceiling still reports the ceiling",
		// The window is clamped before it gets here, so a caller who asked for
		// more than it can have is told the same thing as one who asked for
		// exactly the ceiling, rather than being advised to ask for more again.
		shown: store.ThreadMaxLimit, inWindow: 900, older: 0,
		w:            window(store.ThreadMaxLimit, store.ThreadMaxAge),
		wantContains: []string{"that is the ceiling"},
		wantAbsent:   []string{"to widen"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logWindowNotice(c.shown, c.inWindow, c.older, c.w)
			if c.wantEmpty {
				if got != "" {
					t.Fatalf("a complete read announced %q, which trains readers to ignore the notice", got)
				}
				return
			}
			if got == "" {
				t.Fatal("a truncated read said nothing, so it looks complete")
			}
			for _, want := range c.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("notice %q does not say %q", got, want)
				}
			}
			for _, absent := range c.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("notice %q says %q, which is not true of this window", got, absent)
				}
			}
		})
	}
}

// The reach is quoted back the way the flag that sets it is written. A ceiling
// reported as 168h is one nobody can pass to --since without doing arithmetic
// first.
func TestAgeLabelReadsLikeTheFlagThatSetsIt(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"the ceiling", 7 * 24 * time.Hour, "7d"},
		{"two days", 48 * time.Hour, "2d"},
		{"one day stays in hours", 24 * time.Hour, "24h"},
		{"a day and a half is not a whole number of days", 36 * time.Hour, "36h"},
		{"three days and an hour is not a whole number of days", 73 * time.Hour, "73h"},
		{"an hour", time.Hour, "1h"},
		{"minutes", 90 * time.Minute, "1h30m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ageLabel(c.d); got != c.want {
				t.Errorf("ageLabel(%s) = %q, want %q", c.d, got, c.want)
			}
		})
	}
}

// A claim line has to answer "for how long" where it is read. The lease lives
// in a column the log does not print, so folding it into the body is the only
// place the answer appears — and a claim with no expiry has to say so out loud,
// because that is the one that holds the browser forever.
func TestThreadBodyFoldsAClaimsLeaseIntoItsLine(t *testing.T) {
	created := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	expires := func(d time.Duration) *time.Time {
		t := created.Add(d)
		return &t
	}
	cases := []struct {
		name string
		m    store.ThreadMessage
		want string
	}{{
		name: "a leased claim",
		m: store.ThreadMessage{Kind: store.KindClaim, Body: "scraping listings",
			CreatedAt: created, ExpiresAt: expires(20 * time.Minute)},
		want: "scraping listings (ttl 20m)",
	}, {
		name: "a claim taken forever",
		m: store.ThreadMessage{Kind: store.KindClaim, Body: "scraping listings",
			CreatedAt: created},
		want: "scraping listings (no expiry)",
	}, {
		name: "a leased claim with no reason given",
		m: store.ThreadMessage{Kind: store.KindClaim, CreatedAt: created,
			ExpiresAt: expires(2 * time.Hour)},
		want: "ttl 2h",
	}, {
		name: "an unleased claim with no reason given",
		m:    store.ThreadMessage{Kind: store.KindClaim, CreatedAt: created},
		want: "no expiry",
	}, {
		name: "an already-lapsed lease is still reported as what was asked for",
		// TTL is expiry minus creation, not minus now: the log records what the
		// claim asked to hold for, and reading an old line must not turn a
		// twenty-minute lease into a negative one.
		m: store.ThreadMessage{Kind: store.KindClaim, Body: "held the browser",
			CreatedAt: created, ExpiresAt: expires(20 * time.Minute)},
		want: "held the browser (ttl 20m)",
	}, {
		name: "a note carries no lease",
		m: store.ThreadMessage{Kind: store.KindNote, Body: "gog configured",
			CreatedAt: created},
		want: "gog configured",
	}, {
		name: "a release carries no lease even when the row has an expiry",
		m: store.ThreadMessage{Kind: store.KindRelease, Body: "browser",
			CreatedAt: created, ExpiresAt: expires(20 * time.Minute)},
		want: "browser",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := threadBody(c.m); got != c.want {
				t.Errorf("threadBody = %q, want %q", got, c.want)
			}
		})
	}
}

// The two counts the notice is built from come from the store, and they have to
// be counts of the whole table rather than of the page that was printed.
//
// This is the half that a pure test of logWindowNotice cannot reach: if the
// unbounded count inherited the window's limit, "40 older than that" would come
// back as some number no larger than the page size, and a thread with a thousand
// messages behind the window would report a handful.
func TestReportLogWindowCountsEverythingBehindTheWindow(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	now := time.Now()

	post := func(body string, at time.Time) {
		t.Helper()
		if _, err := s.ThreadPost(ctx, store.ThreadMessage{
			Kind: store.KindNote, By: store.Identity{Name: "tester"},
			Body: body, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	const (
		inside = 6
		older  = 9
	)
	for i := 0; i < inside; i++ {
		post("recent", now.Add(-time.Duration(i)*time.Minute))
	}
	for i := 0; i < older; i++ {
		post("ancient", now.Add(-72*time.Hour).Add(-time.Duration(i)*time.Minute))
	}

	// A window narrower than what is inside it, so both bounds have something
	// to report: two of six shown, nine sitting behind the age bound.
	w := store.ThreadReadWindow(2, 24*time.Hour, now)
	f := w.Apply(store.ThreadFilter{})
	msgs, err := s.ThreadLog(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("the read returned %d messages, want the window's 2", len(msgs))
	}

	notice := captureStderr(t, func() {
		if err := reportLogWindow(ctx, s, f, w, len(msgs)); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"showing the last 2 of 6", "9 older than that"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice %q does not say %q", strings.TrimSpace(notice), want)
		}
	}
}

// A kind filter has to narrow both counts. The question the reader is asking is
// "how many claims am I not seeing", and answering it with a count of every
// message in the thread is a wrong number that looks authoritative.
func TestReportLogWindowCountsOnlyTheKindsBeingRead(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < 4; i++ {
		if _, err := s.ThreadPost(ctx, store.ThreadMessage{
			Kind: store.KindNote, By: store.Identity{Name: "tester"},
			Body: "note", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := s.ThreadPost(ctx, store.ThreadMessage{
			Kind: store.KindEvent, By: store.Identity{Name: "tester"},
			Body: "event", CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	w := store.ThreadReadWindow(1, 24*time.Hour, now)
	f := w.Apply(store.ThreadFilter{Kinds: []store.ThreadKind{store.KindEvent}})
	notice := captureStderr(t, func() {
		if err := reportLogWindow(ctx, s, f, w, 1); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(notice, "showing the last 1 of 3") {
		t.Errorf("the notice %q counts messages the kind filter excluded", strings.TrimSpace(notice))
	}
}

// A complete read must print nothing at all, or `thread log` gains a line of
// stderr on every invocation and the notice stops being read when it matters.
func TestReportLogWindowIsSilentWhenTheReadIsComplete(t *testing.T) {
	s := flowStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := s.ThreadPost(ctx, store.ThreadMessage{
		Kind: store.KindNote, By: store.Identity{Name: "tester"},
		Body: "the only message", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w := store.ThreadReadWindow(0, 0, now)
	f := w.Apply(store.ThreadFilter{})
	notice := captureStderr(t, func() {
		if err := reportLogWindow(ctx, s, f, w, 1); err != nil {
			t.Fatal(err)
		}
	})
	if notice != "" {
		t.Errorf("a complete read printed %q", notice)
	}
}
