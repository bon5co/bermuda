package store

import (
	"context"
	"testing"
	"time"
)

// UsageByJob is what `bermuda usage` reads, and it is the one report whose
// wrongness is invisible: a total is plausible whatever it says. Nobody
// recomputes a token sum by hand, so a dropped run or a window off by one
// would be believed indefinitely.

// The four token counts are billed differently and are kept apart for that
// reason. Summing them into the wrong column would misreport cost without
// changing the total, which is the error nobody would spot.
func TestUsageAddsUpEachTokenKindSeparately(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	for i, r := range []Run{
		{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 1000, CacheCreationTokens: 50},
		{InputTokens: 200, OutputTokens: 20, CacheReadTokens: 2000, CacheCreationTokens: 60},
	} {
		r.ID = "run" + string(rune('a'+i))
		r.JobID = "briefing"
		r.StartedAt = base.Add(time.Duration(i) * time.Hour)
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	usage, err := s.UsageByJob(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("got %d jobs, want the one that ran", len(usage))
	}
	u := usage[0]
	if u.JobID != "briefing" {
		t.Errorf("job id %q, want briefing", u.JobID)
	}
	if u.Runs != 2 {
		t.Errorf("counted %d runs, want 2", u.Runs)
	}
	for _, c := range []struct {
		kind string
		got  int64
		want int64
	}{
		{"input", u.InputTokens, 300},
		{"output", u.OutputTokens, 30},
		{"cache read", u.CacheReadTokens, 3000},
		{"cache creation", u.CacheCreationTokens, 110},
	} {
		if c.got != c.want {
			t.Errorf("%s tokens = %d, want %d", c.kind, c.got, c.want)
		}
	}
}

// The window is the argument, and a run outside it must not be counted. A
// leaking window would make yesterday's spend appear in today's report and
// every number would drift upward forever.
func TestUsageCountsOnlyRunsInsideTheWindow(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	cutoff := base
	runs := []struct {
		id      string
		started time.Time
		tokens  int64
	}{
		{"old", cutoff.Add(-time.Hour), 999},
		{"exactly-at-cutoff", cutoff, 5},
		{"inside", cutoff.Add(time.Hour), 7},
	}
	for _, r := range runs {
		if err := s.PutRun(ctx, Run{ID: r.id, JobID: "briefing",
			StartedAt: r.started, InputTokens: r.tokens}); err != nil {
			t.Fatal(err)
		}
	}

	usage, err := s.UsageByJob(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("got %d jobs, want 1", len(usage))
	}
	// The run at the cutoff instant counts: the window is "at or after since",
	// and excluding its boundary would drop a run every time the report is
	// asked for exactly the period it covers.
	if got := usage[0].InputTokens; got != 12 {
		t.Errorf("input tokens = %d, want 12 (the two runs at or after the cutoff)", got)
	}
	if usage[0].Runs != 2 {
		t.Errorf("counted %d runs, want 2", usage[0].Runs)
	}
}

// The report exists to answer which job is expensive, so jobs must not be
// merged and the busiest-most-recently must lead.
func TestUsageGroupsByJobMostRecentFirst(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	for _, r := range []Run{
		{ID: "a1", JobID: "alpha", StartedAt: base, InputTokens: 1},
		{ID: "b1", JobID: "beta", StartedAt: base.Add(2 * time.Hour), InputTokens: 2},
		{ID: "a2", JobID: "alpha", StartedAt: base.Add(time.Hour), InputTokens: 4},
	} {
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	usage, err := s.UsageByJob(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 {
		t.Fatalf("got %d jobs, want alpha and beta kept apart", len(usage))
	}
	if usage[0].JobID != "beta" {
		t.Errorf("first job is %q, want beta — it ran most recently", usage[0].JobID)
	}
	if usage[1].JobID != "alpha" || usage[1].InputTokens != 5 {
		t.Errorf("alpha = %d tokens over %d runs, want 5 over 2",
			usage[1].InputTokens, usage[1].Runs)
	}
	if !usage[1].LastRun.Equal(base.Add(time.Hour)) {
		t.Errorf("alpha last ran %v, want its newest run at %v",
			usage[1].LastRun, base.Add(time.Hour))
	}
}

// Cost per token depends on the model, so a job whose model changed mid-window
// reports the newest — what the next run will cost. Reporting the oldest would
// price the job on a model it has stopped using.
func TestUsageReportsTheModelTheJobRunsOnNow(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	for _, r := range []Run{
		{ID: "old", JobID: "briefing", StartedAt: base, Model: "sonnet"},
		{ID: "new", JobID: "briefing", StartedAt: base.Add(time.Hour), Model: "opus"},
		// A run that never had its usage attributed records no model. It must
		// not blank out the answer: all-zero means unattributed, not free.
		{ID: "unattributed", JobID: "briefing", StartedAt: base.Add(2 * time.Hour)},
	} {
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	usage, err := s.UsageByJob(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("got %d jobs, want 1", len(usage))
	}
	if usage[0].Model != "opus" {
		t.Errorf("model = %q, want opus — the newest run that named one", usage[0].Model)
	}
}

// An empty window is a real answer and must not be an error: a machine that
// ran nothing yesterday still asks what it spent.
func TestUsageOverAWindowWithNoRuns(t *testing.T) {
	s := newThread(t)
	ctx := context.Background()

	if err := s.PutRun(ctx, Run{ID: "old", JobID: "briefing",
		StartedAt: base.Add(-24 * time.Hour), InputTokens: 500}); err != nil {
		t.Fatal(err)
	}

	usage, err := s.UsageByJob(ctx, base)
	if err != nil {
		t.Fatalf("an empty window errored: %v", err)
	}
	if len(usage) != 0 {
		t.Errorf("got %d jobs, want none", len(usage))
	}
}
