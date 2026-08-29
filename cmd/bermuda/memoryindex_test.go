package main

import (
	"bytes"
	"context"
	"flag"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/index"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// The most common outcome by far, and the one a person reads at a glance: the
// vault has not changed since the last sweep.
func TestPrintSweepSaysNothingChangedInOneLine(t *testing.T) {
	var b bytes.Buffer
	printSweep(&b, index.Report{Root: "/vault", Unchanged: 580, Took: 40 * time.Millisecond})
	out := b.String()
	if !strings.Contains(out, "up to date") || !strings.Contains(out, "580") {
		t.Errorf("output = %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("output is %d lines, want one: %q", strings.Count(out, "\n"), out)
	}
}

func TestPrintSweepBreaksOutNewChangedAndRemoved(t *testing.T) {
	var b bytes.Buffer
	printSweep(&b, index.Report{Indexed: 3, New: 2, Changed: 1, Removed: 4, Chunks: 22, Unchanged: 100})
	out := b.String()
	for _, want := range []string{"3 note(s)", "2 new", "1 changed", "22 chunk(s)", "removed 4", "100 unchanged"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q is missing %q", out, want)
		}
	}
}

// A note too big to index has to say so: "not indexed" and "too big to index"
// are different answers to "why did search not find it".
func TestPrintSweepNamesEverySkippedNote(t *testing.T) {
	var b bytes.Buffer
	printSweep(&b, index.Report{Indexed: 1, Chunks: 1, Skipped: []index.File{
		{Path: "transcripts/dump.md", Skipped: "larger than 2 MiB"},
	}})
	if !strings.Contains(b.String(), "transcripts/dump.md: larger than 2 MiB") {
		t.Errorf("output = %q", b.String())
	}
}

func TestPrintHitsShowsWhereEachAnswerCameFrom(t *testing.T) {
	var b bytes.Buffer
	printHits(&b, []index.Hit{{
		Path: "memory/goals.md", Heading: "Money", Score: 0.82,
		Text: "goals\nMoney\n\nFinancial independence is the standing objective.",
	}}, false)
	out := b.String()
	if !strings.Contains(out, "memory/goals.md") || !strings.Contains(out, "Money") {
		t.Errorf("output %q does not say where the answer came from", out)
	}
	if !strings.Contains(out, "0.82") {
		t.Errorf("output %q has no score", out)
	}
}

// The title and heading are already on the line above; repeating them costs
// the reader the first two lines of every result.
func TestPrintHitsDropsTheEmbeddedPrefixFromTheBody(t *testing.T) {
	var b bytes.Buffer
	printHits(&b, []index.Hit{{
		Path: "a.md", Heading: "H", Text: "a title\nH\n\nthe paragraph itself",
	}}, false)
	if strings.Count(b.String(), "a title") != 0 {
		t.Errorf("the embedded prefix was printed as body: %q", b.String())
	}
	if !strings.Contains(b.String(), "the paragraph itself") {
		t.Errorf("output = %q", b.String())
	}
}

// Nothing matching is a real answer, and the next question is always whether
// anything is indexed at all.
func TestPrintHitsSaysWhereToLookWhenNothingMatched(t *testing.T) {
	var b bytes.Buffer
	printHits(&b, nil, false)
	if !strings.Contains(b.String(), "--status") {
		t.Errorf("output = %q", b.String())
	}
}

func TestIndentTrimsALongParagraphUnlessAskedForAllOfIt(t *testing.T) {
	text := "one\ntwo\nthree\nfour\nfive\nsix"
	if got := indent(text, "", false); !strings.Contains(got, "…") {
		t.Errorf("a six-line paragraph was not trimmed: %q", got)
	}
	if got := indent(text, "", true); strings.Contains(got, "…") {
		t.Errorf("--full still trimmed: %q", got)
	}
}

// A fresh install has no Python, and that is the one failure this feature
// actually hits. It has to arrive as instructions, not as an error nobody can
// act on.
func TestHelperAdviceTurnsAMissingPythonIntoAFix(t *testing.T) {
	err := helperAdvice(index.ErrNoPython)
	if err == nil || !strings.Contains(err.Error(), "uv") {
		t.Errorf("err = %v, want the install instruction", err)
	}
}

func TestMemoryCmdDispatchesIndexAndSearch(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("BERMUDA_INDEX_ROOT", t.TempDir())
	t.Setenv("BERMUDA_INDEX_PYTHON", "")
	t.Setenv("PATH", t.TempDir())
	// --status embeds nothing, so it works with no helper at all.
	if err := memoryCmd([]string{"index", "--status"}); err != nil {
		t.Errorf("memory index --status: %v", err)
	}
	// Search with no query is a usage error, which proves the dispatch without
	// needing a collection.
	if err := memoryCmd([]string{"search"}); err == nil {
		t.Error("memory search with no query was accepted")
	}
	if err := memoryCmd([]string{"nonsense"}); err == nil {
		t.Error("an unknown memory subcommand was accepted")
	}
}

// The daemon is what makes "stale files are reindexed automatically" true, so
// the ticker has to fire on its own.
func TestDaemonSweepsTheVaultIndexWhileRunning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var mu sync.Mutex
	done := make(chan struct{})
	calls := 0
	d := &daemon{
		store:          s,
		tick:           time.Hour,
		slots:          make(chan struct{}, 1),
		reconcileEvery: time.Hour,
		indexEvery:     5 * time.Millisecond,
		indexVault: func(context.Context) (index.Report, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 2 {
				close(done)
			}
			return index.Report{}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.run(ctx)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the daemon never swept the vault index")
	}
}

// A machine with no Python cannot search its notes. That is a smaller problem
// than a scheduler that stops running jobs, and repeating the same sentence
// every five minutes forever would be the loudest thing in the log.
func TestDaemonStopsSweepingOnceTheHelperIsKnownMissing(t *testing.T) {
	d := &daemon{indexVault: func(context.Context) (index.Report, error) {
		return index.Report{}, index.ErrNoPython
	}}
	d.sweepIndex(context.Background())
	if !d.indexOff {
		t.Fatal("a missing helper did not turn the sweep off")
	}
	d.indexVault = func(context.Context) (index.Report, error) {
		t.Fatal("the sweep ran again after the helper was known missing")
		return index.Report{}, nil
	}
	d.sweepIndex(context.Background())
}

// Any other failure is transient -- a locked database, a helper killed by the
// OOM killer -- and must be retried on the next tick.
func TestDaemonKeepsSweepingAfterAnOrdinaryFailure(t *testing.T) {
	d := &daemon{indexVault: func(context.Context) (index.Report, error) {
		return index.Report{}, context.DeadlineExceeded
	}}
	d.sweepIndex(context.Background())
	if d.indexOff {
		t.Error("a transient failure turned the sweep off for good")
	}
}

func TestDaemonIndexIntervalIsMinutesNotSeconds(t *testing.T) {
	if defaultIndexEvery < time.Minute {
		t.Fatalf("defaultIndexEvery is %s, want at least a minute", defaultIndexEvery)
	}
}

// The question comes first, because that is how every other search command
// works. Go's flag package stops at the first non-flag word, so without this
// `memory search why was it replaced -n 3` searched for the flags as text and
// returned eight results -- plausible output, wrong query, nothing logged.
func TestSearchFlagsAreReadWhereverTheyAppear(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	n := fs.Int("n", 8, "")
	section := fs.String("section", "", "")
	full := fs.Bool("full", false, "")
	words, err := parseAround(fs, []string{"why", "was", "it", "replaced", "-n", "3", "--section", "memory", "--full"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(words, " "); got != "why was it replaced" {
		t.Errorf("query = %q, want the flags removed from it", got)
	}
	if *n != 3 || *section != "memory" || !*full {
		t.Errorf("flags after the query were not read: n=%d section=%q full=%v", *n, *section, *full)
	}
}

func TestSearchFlagsStillWorkBeforeTheQuery(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	n := fs.Int("n", 8, "")
	words, err := parseAround(fs, []string{"-n", "2", "why", "was", "it", "replaced"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(words, " ") != "why was it replaced" || *n != 2 {
		t.Errorf("words = %v, n = %d", words, *n)
	}
}

// What a sweep costs is the number that decides whether running it on the
// daemon's timer was a good idea, so it belongs on screen rather than in a
// benchmark nobody reruns.
func TestIndexStatusPrintsWhatTheLastSweepCost(t *testing.T) {
	dir := t.TempDir()
	m, err := index.OpenManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Record(index.File{Path: "a.md", Digest: "1"}, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordSweep(time.Now(), 24*time.Millisecond, 568, 2, 7); err != nil {
		t.Fatal(err)
	}
	m.Close()

	t.Setenv("BERMUDA_INDEX_PYTHON", "")
	t.Setenv("PATH", t.TempDir())
	var b bytes.Buffer
	if err := reportIndexStatus(&b, index.New(dir, t.TempDir()), context.Background(), false); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"24ms", "568 note(s) hashed", "2 note(s), 7 chunk(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("status %q is missing %q", out, want)
		}
	}
	// And with no helper it still has to say how to get one.
	if !strings.Contains(out, "uv") {
		t.Errorf("status does not say how to make search work:\n%s", out)
	}
}
