package flow

import (
	"strings"
	"testing"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

// Every flow has an overwatch. A file that declares none is not a flow nobody
// is watching -- it is a flow watched on the defaults, and the two have to
// resolve to the same thing.
func TestAFlowThatDeclaresNoOverwatchGetsTheStandardOne(t *testing.T) {
	var none *Overwatch
	got := none.Resolve()
	if got.Watch != WatchOnTrouble {
		t.Errorf("Watch = %q, want %q", got.Watch, WatchOnTrouble)
	}
	if got.Budget != defaultBudget {
		t.Errorf("Budget = %d, want %d", got.Budget, defaultBudget)
	}
	if got.Wait() != DefaultTimeout {
		t.Errorf("Wait = %s, want %s", got.Wait(), DefaultTimeout)
	}
}

func TestResolveFillsOnlyWhatTheFlowLeftOut(t *testing.T) {
	o := &Overwatch{Model: "opus", Watch: WatchEveryStep}
	got := o.Resolve()
	if got.Model != "opus" || got.Watch != WatchEveryStep {
		t.Errorf("Resolve overwrote what the flow declared: %+v", got)
	}
	if got.Budget != defaultBudget || len(got.Allow) != len(defaultAllow) {
		t.Errorf("Resolve did not fill the rest: %+v", got)
	}
}

// Waving a failed step through is the one decision that breaks what a flow is
// for, so it is never in the default set.
func TestSkipIsNotAllowedByDefault(t *testing.T) {
	std := (*Overwatch)(nil).Resolve()
	if std.Permits(DecideSkip) {
		t.Error("the default overwatch may wave a failed step through")
	}
	for _, d := range []string{DecideRetry, DecideGoto, DecidePark, DecideAbort} {
		if !std.Permits(d) {
			t.Errorf("the default overwatch may not %s", d)
		}
	}
}

// A flow cannot configure its way into having no legal answer: park is what
// the harness would have done anyway, and continue is what "nothing is wrong"
// is called.
func TestParkAndContinueAreAlwaysPermitted(t *testing.T) {
	narrow := (&Overwatch{Allow: []string{DecideRetry}}).Resolve()
	if !narrow.Permits(DecidePark) || !narrow.Permits(DecideContinue) {
		t.Error("a flow narrowed its overwatch out of every legal answer")
	}
	if narrow.Permits(DecideAbort) {
		t.Error("a decision the flow left out was permitted anyway")
	}
}

func TestOverwatchValidationRefusesWhatCannotMeanAnything(t *testing.T) {
	cases := map[string]struct {
		o    Overwatch
		want string
	}{
		"a cadence that does not exist":  {Overwatch{Watch: "always"}, "not a cadence"},
		"a decision that does not exist": {Overwatch{Allow: []string{"improvise"}}, "not a decision"},
		"a budget past the ceiling":      {Overwatch{Budget: maxBudget + 1}, "ceiling"},
		"a negative budget":              {Overwatch{Budget: -1}, "negative"},
		"a timeout that is not one":      {Overwatch{Timeout: "soon"}, "not a duration"},
		"a timeout of nothing":           {Overwatch{Timeout: "0s"}, "not a wait"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.o.validate()
			if err == nil {
				t.Fatalf("%+v was accepted", c.o)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

func TestWaitParsesWhatTheFlowDeclared(t *testing.T) {
	if got := (Overwatch{Timeout: "2m"}).Wait(); got != 2*time.Minute {
		t.Errorf("Wait = %s, want 2m", got)
	}
	// A duration that stopped parsing means the file changed under a running
	// flow; the default is the safe reading of that.
	if got := (Overwatch{Timeout: "nonsense"}).Wait(); got != DefaultTimeout {
		t.Errorf("Wait = %s, want the default", got)
	}
}

// A flow of shell steps has exit codes rather than prose, and giving it an
// agent it never asked for makes a deterministic flow depend on one.
func TestAShellOnlyFlowIsOverseenOnlyIfItSaysSo(t *testing.T) {
	shell := Flow{Steps: []store.Step{{ID: "a", Run: "true"}, {ID: "b", Run: "false"}}}
	if shell.Applies() {
		t.Error("a shell-only flow was given an overwatch it never asked for")
	}
	shell.Overwatch = &Overwatch{}
	if !shell.Applies() {
		t.Error("a shell flow that declared an overwatch was not given one")
	}
}

// Anything that runs an agent is overseen, declared or not. That is what
// mandatory means.
func TestAFlowWithAnAgentStepIsAlwaysOverseen(t *testing.T) {
	mixed := Flow{Steps: []store.Step{{ID: "a", Run: "true"}, {ID: "b", Agent: "think"}}}
	if !mixed.Applies() {
		t.Error("a flow with an agent step was not overseen")
	}
}

// A step sharing the overwatch's name would share its run id, which shows up
// as one agent reading another's result -- confusing enough to refuse at read
// time rather than debug at run time.
func TestAStepCannotBeCalledOverwatch(t *testing.T) {
	_, err := decode([]byte("steps:\n  - id: overwatch\n    agent: think\n"), "wf", "wf.yml")
	if err == nil || !strings.Contains(err.Error(), "overwatch") {
		t.Fatalf("err = %v, want a refusal naming the clash", err)
	}
}

func TestAnOverwatchBlockIsReadFromTheFile(t *testing.T) {
	f, err := decode([]byte(`
overwatch:
  model: opus
  watch: every_step
  budget: 5
  timeout: 3m
  allow: [retry, park]
  brief: never retry the deploy step
steps:
  - id: build
    agent: build it
`), "wf", "wf.yml")
	if err != nil {
		t.Fatal(err)
	}
	w := f.Watcher()
	if w.Model != "opus" || w.Watch != WatchEveryStep || w.Budget != 5 {
		t.Errorf("overwatch = %+v", w)
	}
	if w.Wait() != 3*time.Minute {
		t.Errorf("Wait = %s, want 3m", w.Wait())
	}
	if w.Permits(DecideAbort) {
		t.Error("a decision the file left out of allow was permitted")
	}
	if !strings.Contains(w.Brief, "never retry the deploy") {
		t.Errorf("the flow's own brief was dropped: %q", w.Brief)
	}
}

// The file is edited by people and by agents, neither of which gets a schema
// in front of them, so a bad block has to be refused at read time.
func TestABadOverwatchBlockFailsTheFileNotTheRun(t *testing.T) {
	_, err := decode([]byte("overwatch:\n  watch: sometimes\nsteps:\n  - id: a\n    agent: go\n"), "wf", "wf.yml")
	if err == nil || !strings.Contains(err.Error(), "not a cadence") {
		t.Fatalf("err = %v, want the file refused", err)
	}
}
