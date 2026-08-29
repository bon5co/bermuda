package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/checklist"
	"github.com/bon5co/bermuda/v3/internal/flow"
	"github.com/bon5co/bermuda/v3/internal/runner"
	"github.com/bon5co/bermuda/v3/internal/store"
)

// The check command, driven the way it is typed.
//
// The thing worth testing here is not the file format — that is the package's
// own business — but the command line, and specifically the one piece of it
// that looks ambiguous: <list> is positional and optional, so `check tick 3`
// and `check tick ship-640 3` have to mean different things without either
// being a guess.

// checkEnv points a test at its own vault.
func checkEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	t.Setenv("BERMUDA_MEMORY_DIR", "")
	t.Setenv(checklist.Env, "")
}

func TestCheckCmdRefusesAnUnknownSubcommand(t *testing.T) {
	if err := checkCmd(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("checkCmd with no arguments returned %v, want the usage line", err)
	}
	if err := checkCmd([]string{"tik"}); err == nil || !strings.Contains(err.Error(), "tik") {
		t.Errorf("checkCmd(tik) returned %v, want an error naming the typo", err)
	}
}

// The whole cycle, in the order somebody actually types it.
func TestCheckNewAddTickAndList(t *testing.T) {
	checkEnv(t)
	out, err := captureStdout(t, func() error {
		return checkNew([]string{"ship 640 fix", "--about", "timezone fix, webapp"})
	})
	if err != nil {
		t.Fatalf("check new: %v", err)
	}
	path := strings.SplitN(out, "\n", 2)[0]
	if filepath.Dir(path) != checkDir() {
		t.Fatalf("check new wrote to %q, want a page under %q", path, checkDir())
	}

	for _, argv := range [][]string{
		{"open PR into dev", "--ref", "https://example.test/pull/474"},
		{"merge release-42", "--blocked-on", "operator", "--why", "auto-deploys to QA"},
	} {
		if _, err := captureStdout(t, func() error { return checkAdd(argv) }); err != nil {
			t.Fatalf("check add %v: %v", argv, err)
		}
	}
	// No list named, and it still landed on the only page there is.
	if _, err := captureStdout(t, func() error { return checkSet([]string{"open PR"}, true) }); err != nil {
		t.Fatalf("check tick: %v", err)
	}

	out, err = captureStdout(t, func() error { return checkList(nil) })
	if err != nil {
		t.Fatalf("check ls: %v", err)
	}
	if !strings.Contains(out, "1/2 done, 1 blocked on operator") {
		t.Errorf("check ls said:\n%s\nwant the counts on one line", out)
	}
	if !strings.Contains(out, "ship-640-fix") {
		t.Errorf("check ls did not name the page:\n%s", out)
	}
}

// A page with nothing left open is not what "what is outstanding" means, so it
// leaves the listing — and the listing says so rather than looking empty.
func TestCheckListHidesFinishedWorkUnlessAsked(t *testing.T) {
	checkEnv(t)
	if _, err := captureStdout(t, func() error { return checkNew([]string{"done work"}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return checkAdd([]string{"the only thing"}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return checkSet([]string{"1"}, true) }); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error { return checkList(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "done-work") || !strings.Contains(out, "nothing open") {
		t.Errorf("check ls said:\n%s\nwant the finished page hidden and said so", out)
	}
	out, err = captureStdout(t, func() error { return checkList([]string{"--all"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "done-work") {
		t.Errorf("check ls --all said:\n%s\nwant the finished page listed", out)
	}
}

// --why with nobody to hold it is a stop sign with nothing written on it.
func TestCheckAddRefusesAReasonWithNoBlocker(t *testing.T) {
	checkEnv(t)
	if _, err := captureStdout(t, func() error { return checkNew([]string{"work"}) }); err != nil {
		t.Fatal(err)
	}
	err := checkAdd([]string{"a thing", "--why", "because"})
	if err == nil || !strings.Contains(err.Error(), "--blocked-on") {
		t.Errorf("--why alone returned %v, want a refusal naming --blocked-on", err)
	}
}

// The arity rule, stated as a test because it is the only part of this command
// line that could be read two ways.
func TestSplitListArgReadsTheOptionalListFromTheArity(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		want     int
		wantList string
		wantRest []string
	}{
		{"item only", []string{"3"}, 2, "", []string{"3"}},
		{"list and item", []string{"ship-640", "3"}, 2, "ship-640", []string{"3"}},
		{"item and flags", []string{"open PR", "--ref", "x"}, 2, "", []string{"open PR", "--ref", "x"}},
		{"list item and flags", []string{"ship", "open PR", "--ref", "x"}, 2, "ship", []string{"open PR", "--ref", "x"}},
		{"show with nothing", nil, 1, "", nil},
		{"show with a list", []string{"ship"}, 1, "ship", []string{}},
		{"show with a flag", []string{"--raw"}, 1, "", []string{"--raw"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list, rest := splitListArg(c.argv, c.want)
			if list != c.wantList {
				t.Errorf("list = %q, want %q", list, c.wantList)
			}
			if strings.Join(rest, "|") != strings.Join(c.wantRest, "|") {
				t.Errorf("rest = %v, want %v", rest, c.wantRest)
			}
		})
	}
}

// Naming an item that is not there has to point at both ways it could have been
// meant, because the likeliest cause is a list id typed where an item goes.
func TestCheckTickOnAMissingItemSaysHowToNameAList(t *testing.T) {
	checkEnv(t)
	if _, err := captureStdout(t, func() error { return checkNew([]string{"work"}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return checkAdd([]string{"a thing"}) }); err != nil {
		t.Fatal(err)
	}
	err := checkSet([]string{"nothing like it"}, true)
	if err == nil || !strings.Contains(err.Error(), "check tick <list>") {
		t.Errorf("ticking a missing item returned %v, want the two-argument form spelled out", err)
	}
}

// The flow half: a step that names an item gets one before the run starts and
// has it ticked when it reports ok — so the declared sequence and the page a
// human is reading are one object rather than two that drift.
func TestFlowStepsSeedAndTickTheirChecklistItems(t *testing.T) {
	checkEnv(t)
	l, err := checklist.Resolve(checkDir(), "")
	if err == nil {
		t.Fatalf("an empty vault resolved to %q", l.Name)
	}
	out, err := captureStdout(t, func() error { return checkNew([]string{"ship the fix"}) })
	if err != nil {
		t.Fatal(err)
	}
	path := strings.SplitN(out, "\n", 2)[0]

	def := flow.Flow{ID: "ship", Steps: []store.Step{
		{ID: "survey", Agent: "look", Check: "survey the repo"},
		{ID: "build", Agent: "change", Check: "make the change"},
		{ID: "quiet", Run: "true"},
	}}
	seedChecklist(def, path)

	seeded, err := checklist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded.Items) != 2 {
		t.Fatalf("seeded %d items, want one per step that names one", len(seeded.Items))
	}
	if seeded.Counts().Line() != "0/2 done" {
		t.Errorf("a run that has not started reads as %q", seeded.Counts().Line())
	}

	// A step that has only started ticks nothing: a page that ran ahead of the
	// work is worse than one that lags it.
	tickStep(def, path, runner.StepRun{ID: "survey", Outcome: runner.OutcomeRunning})
	if after, _ := checklist.Load(path); after.Counts().Done != 0 {
		t.Error("a running step ticked its item")
	}
	tickStep(def, path, runner.StepRun{ID: "survey", Outcome: runner.OutcomeDone})
	tickStep(def, path, runner.StepRun{ID: "build", Outcome: runner.OutcomeParked})
	after, err := checklist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Counts().Line(); got != "1/2 done" {
		t.Errorf("after one ok and one park the page reads %q, want 1/2 done", got)
	}

	// Seeding again is what a resume does, and it must not write a second copy
	// of every step.
	seedChecklist(def, path)
	if again, _ := checklist.Load(path); len(again.Items) != 2 {
		t.Errorf("a resume seeded %d items, want the 2 already there", len(again.Items))
	}
}

// A vault that cannot be written must not take a flow down with it: the
// checklist is the human's view of the run, not the run.
func TestSeedChecklistSurvivesAnUnwritablePage(t *testing.T) {
	checkEnv(t)
	missing := filepath.Join(t.TempDir(), "nope", "gone.md")
	def := flow.Flow{ID: "ship", Steps: []store.Step{{ID: "a", Run: "true", Check: "a thing"}}}
	// Neither of these returns an error, by design; the test is that neither
	// panics or writes a file where there is no directory.
	seedChecklist(def, missing)
	tickStep(def, missing, runner.StepRun{ID: "a", Outcome: runner.OutcomeDone})
	if _, err := os.Stat(missing); err == nil {
		t.Error("a page was created under a directory that does not exist")
	}
	// And a run bound to nothing is the ordinary case: it must do nothing at all.
	seedChecklist(def, "")
	tickStep(def, "", runner.StepRun{ID: "a", Outcome: runner.OutcomeDone})
}
