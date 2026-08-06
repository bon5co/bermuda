package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A run id is a directory name, a sort key and a label all at once: it names
// the run directory the agent writes into, it is what `run list` orders by, and
// it is the anchor the usage reader keys off (filepath.Base of the run dir).
// Nothing checks it at the point of use, so whatever it produces is what the
// rest of the system believes.

var runIDRE = regexp.MustCompile(`^\d{8}T\d{6}Z-`)

// The timestamp is UTC, not local time. Bermuda runs in Asia/Tokyo, where a
// local timestamp would still sort correctly — until the machine's zone changed
// or a zone with DST was used, at which point an hour of run ids would sort
// before the hour that preceded them and `run list` would interleave them with
// no error anywhere. The stamp is compared against the wall clock in UTC, which
// is what makes this fail on a machine that is not on UTC if the offset is ever
// dropped.
func TestRunIDStampsUTC(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	id := newRunID("nightly")
	after := time.Now().UTC().Add(time.Second)

	if !runIDRE.MatchString(id) {
		t.Fatalf("run id %q does not start with a Zulu timestamp", id)
	}
	stamp := strings.SplitN(id, "-", 2)[0]
	at, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		t.Fatalf("timestamp %q does not parse: %v", stamp, err)
	}
	if at.Before(before.Truncate(time.Second)) || at.After(after) {
		t.Errorf("timestamp %s is outside [%s, %s]; it is not UTC",
			stamp, before.Format(time.RFC3339), after.Format(time.RFC3339))
	}
}

// Lexicographic order has to be chronological order. `run list` and the board
// both present runs newest first by id, so an id that sorted differently from
// the clock would put a run in the wrong place in its own job's history.
func TestRunIDsSortChronologically(t *testing.T) {
	first := newRunID("nightly")
	// The format has one-second resolution, so a later id needs a later second
	// to be distinguishable at all.
	time.Sleep(1100 * time.Millisecond)
	second := newRunID("nightly")

	if first == second {
		t.Fatalf("two run ids a second apart are identical: %q", first)
	}
	if !(first < second) {
		t.Errorf("%q sorts after %q, but was made first", first, second)
	}
}

// The job id is part of the run id, which is what makes a run directory
// readable on its own — `ls ~/.bermuda/runs` is meant to say which job each
// one belonged to.
func TestRunIDCarriesTheJobID(t *testing.T) {
	for _, job := range []string{"nightly", "bermuda-unit-tests", "adhoc"} {
		id := newRunID(job)
		if !strings.HasSuffix(id, "-"+job) {
			t.Errorf("run id %q does not end in the job id %q", id, job)
		}
	}
}

// Two jobs firing in the same second must not collide. They would share a run
// directory, so the second agent would read the first one's result.json and the
// store would upsert one row over the other.
func TestRunIDsForDifferentJobsInTheSameSecondDiffer(t *testing.T) {
	if a, b := newRunID("alpha"), newRunID("beta"); a == b {
		t.Errorf("two jobs got the same run id %q", a)
	}
}

// The run directory lives under the state directory, so a test — or a second
// installation — that sets BERMUDA_STATE_DIR gets its own runs. It is also the
// path `run show` prints for the prompt, transcript and result, so it has to be
// the same one the runner wrote to.
func TestRunDirIsUnderTheStateDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", dir)

	id := newRunID("nightly")
	got := runDirFor(id)
	if want := filepath.Join(dir, "runs", id); got != want {
		t.Errorf("run dir = %q, want %q", got, want)
	}
	// Usage attribution recovers the run id from the directory name; a run dir
	// whose base is not the run id silently costs the run its token counts.
	if filepath.Base(got) != id {
		t.Errorf("base of %q is not the run id %q", got, id)
	}
}
