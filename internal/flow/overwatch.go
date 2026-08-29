package flow

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Overwatch is the agent that oversees a whole run.
//
// Every step of a flow is a fresh agent that has read nothing: it knows its own
// prompt, the input, and the note the step before it published. That is
// deliberate — it is what stops step four inheriting step one's confusion — but
// it leaves nobody holding the shape of the run. When a step fails, the harness
// takes the only decision it can from outside: park, and wait for a person.
//
// Overwatch is the reader that has the whole thing. It is handed the flow's
// definition, every step's outcome and note, and the artifacts of whatever just
// went wrong, and it answers one question: what should the run do now. It
// exists because "park and wait for a human" is right when nobody knows why a
// step failed and wasteful when the answer is legible from two steps up.
//
// Every flow has one. A flow that declares none gets the defaults below, which
// is why this is a pointer with a resolver rather than a struct with zero
// values: unset and "explicitly the default" have to read the same, and the
// only thing a flow may configure is how much overwatch costs and what it is
// allowed to do — not whether anyone is watching.
type Overwatch struct {
	// Model and Effort override the job's for the overwatch agent alone. The
	// decision it makes is a judgement over a whole run, which is not the same
	// difficulty as the steps it is judging -- usually harder, occasionally
	// much cheaper.
	Model  string `yaml:"model,omitempty"`
	Effort string `yaml:"effort,omitempty"`
	// Kind is the herdr agent kind, as on a step.
	Kind string `yaml:"kind,omitempty"`

	// Brief is appended to the standard brief, and is where a flow says what
	// this particular sequence's overwatch needs to know: which step is
	// expensive, which failure is always transient, what must never be retried.
	Brief string `yaml:"brief,omitempty"`

	// Watch is when the overwatch is consulted.
	//
	//   on_trouble  a step failed or parked, or a declared edge ran out (default)
	//   every_step  after every step, successful or not
	//
	// The default costs nothing on a run where nothing goes wrong, which is
	// most runs. every_step buys continuous oversight and costs one agent call
	// per step, so it is opt-in rather than the default -- a flow that wants a
	// reader watching every result is declaring that, not discovering it in a
	// token bill.
	Watch string `yaml:"watch,omitempty"`

	// Allow is the set of decisions this flow will honour. Unset means the
	// four safe ones: retry, goto, park, abort.
	//
	// `skip` is deliberately not in the default set. Waving a failed step
	// through is the one decision that breaks what a flow is for -- B must not
	// run on A's unverified claim -- so a flow that wants an agent able to take
	// it has to say so in the file, where a reviewer sees it.
	Allow []string `yaml:"allow,omitempty"`

	// Timeout bounds one consult, as a duration string ("90s", "5m"). Unset is
	// ten minutes.
	//
	// A supervisor that can hang the run it supervises is worse than no
	// supervisor: before this had a deadline, a flow that used to park in
	// milliseconds sat for ten minutes waiting on an agent that was never
	// going to answer. The bound is the overwatch's own, not the job's,
	// because the job's is the budget for doing the work and this is the
	// budget for deciding about it.
	Timeout string `yaml:"timeout,omitempty"`

	// Budget bounds how many decisions the overwatch may take in one run.
	// Unset is three. It survives a resume for the same reason a loop budget
	// does: the thing that resumes a parked run is not always a human.
	Budget int `yaml:"budget,omitempty"`
}

// OverwatchStepID is the id the overwatch agent runs under. It is not a step
// and never appears in a flow file; it names the agent so a run's artifacts say
// which of them the overwatch wrote.
const OverwatchStepID = "overwatch"

// Applies reports whether this flow is overseen.
//
// Every flow that runs agents has an overwatch, declared or not -- that is what
// "mandatory" means, and it is the case the feature exists for: agents write
// prose about work nobody else saw, and somebody has to read it.
//
// A flow of nothing but `run:` steps is the one exception, and only while it
// declares no overwatch of its own. Its failures are exit codes, and summoning
// a model to interpret `test -f gate` spends an agent on a boolean while making
// a deliberately deterministic flow depend on one. A shell-only flow that wants
// one says so in the file, and then it has one.
func (f Flow) Applies() bool {
	if f.Overwatch != nil {
		return true
	}
	for _, s := range f.Steps {
		if s.IsAgent() {
			return true
		}
	}
	return false
}

// Watch cadences.
const (
	// WatchOnTrouble consults the overwatch only where the run would otherwise
	// park.
	WatchOnTrouble = "on_trouble"
	// WatchEveryStep consults it after every step.
	WatchEveryStep = "every_step"
)

// The decisions an overwatch may return.
const (
	// DecideRetry runs the step that just failed again, unchanged.
	DecideRetry = "retry"
	// DecideGoto sends the run back to an earlier step, as a declared on_fail
	// edge would, but chosen with the whole run in view.
	DecideGoto = "goto"
	// DecidePark stops the run where it is, resumable. This is what the harness
	// does on its own, and it is the answer the overwatch is expected to give
	// most often.
	DecidePark = "park"
	// DecideAbort stops the run and says it is not worth resuming.
	DecideAbort = "abort"
	// DecideSkip accepts the failed step and carries on to the next one. Never
	// available unless the flow's `allow` names it.
	DecideSkip = "skip"
	// DecideContinue is the no-op an every_step overwatch returns when it has
	// nothing to say. It is not configurable and not refusable: it is what
	// "carry on" is called.
	DecideContinue = "continue"
)

// defaultAllow is what a flow that says nothing will honour.
var defaultAllow = []string{DecideRetry, DecideGoto, DecidePark, DecideAbort}

// DefaultTimeout bounds one consult when the flow names none.
const DefaultTimeout = 10 * time.Minute

// defaultBudget is how many decisions an overwatch gets per run when the flow
// names none. Three is enough for "retry, then go back, then give up" and small
// enough that a confused overwatch costs three agent calls rather than a night.
const defaultBudget = 3

// maxBudget is the ceiling a flow may declare. The failure this bounds is an
// overwatch that keeps choosing retry on something that will never pass, which
// is the same failure max_loops bounds one step at a time.
const maxBudget = 12

// Resolve fills in every default, so callers never handle a nil overwatch and
// never re-derive what "unset" meant.
//
// A nil receiver resolves to the standard overwatch, which is the whole of what
// "every flow has one" means in code.
func (o *Overwatch) Resolve() Overwatch {
	out := Overwatch{Watch: WatchOnTrouble, Budget: defaultBudget, Allow: defaultAllow}
	if o == nil {
		return out
	}
	res := *o
	if strings.TrimSpace(res.Watch) == "" {
		res.Watch = WatchOnTrouble
	}
	if res.Budget <= 0 {
		res.Budget = defaultBudget
	}
	if len(res.Allow) == 0 {
		res.Allow = defaultAllow
	}
	return res
}

// Wait is how long one consult may take.
//
// Parsing is not repeated here: validate has already refused a duration that
// does not parse, so an unparseable one at this point means the file changed
// under the run, and the default is the safe reading of it.
func (o Overwatch) Wait() time.Duration {
	if strings.TrimSpace(o.Timeout) == "" {
		return DefaultTimeout
	}
	d, err := time.ParseDuration(o.Timeout)
	if err != nil || d <= 0 {
		return DefaultTimeout
	}
	return d
}

// Permits reports whether this overwatch may take a decision.
//
// park and continue are always permitted whatever the flow said: park is what
// the harness would have done anyway, and continue is the only thing an
// every_step consult can answer when nothing is wrong. A flow cannot configure
// its way into having no legal answer.
func (o Overwatch) Permits(decision string) bool {
	if decision == DecidePark || decision == DecideContinue {
		return true
	}
	for _, a := range o.Allow {
		if a == decision {
			return true
		}
	}
	return false
}

// validDecisions is every decision a flow may name in `allow`.
var validDecisions = map[string]bool{
	DecideRetry: true, DecideGoto: true, DecidePark: true,
	DecideAbort: true, DecideSkip: true,
}

// validate rejects an overwatch block that cannot mean what it says.
func (o *Overwatch) validate() error {
	if o == nil {
		return nil
	}
	switch strings.TrimSpace(o.Watch) {
	case "", WatchOnTrouble, WatchEveryStep:
	default:
		return fmt.Errorf("overwatch watch: %q is not a cadence (%s or %s)",
			o.Watch, WatchOnTrouble, WatchEveryStep)
	}
	if o.Budget > maxBudget {
		return fmt.Errorf("overwatch budget: %d is more than the ceiling of %d — an overwatch "+
			"that has not resolved a run in %d decisions is not going to on the next one",
			o.Budget, maxBudget, maxBudget)
	}
	if o.Budget < 0 {
		return fmt.Errorf("overwatch budget: %d is negative", o.Budget)
	}
	if t := strings.TrimSpace(o.Timeout); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return fmt.Errorf("overwatch timeout: %q is not a duration (90s, 5m)", o.Timeout)
		}
		if d <= 0 {
			return fmt.Errorf("overwatch timeout: %s is not a wait", o.Timeout)
		}
	}
	var unknown []string
	for _, a := range o.Allow {
		if !validDecisions[strings.TrimSpace(a)] {
			unknown = append(unknown, a)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("overwatch allow: %s is not a decision (%s)",
			strings.Join(unknown, ", "), strings.Join(sortedDecisions(), ", "))
	}
	return nil
}

// sortedDecisions names every legal decision, for an error a reader can act on.
func sortedDecisions() []string {
	out := make([]string, 0, len(validDecisions))
	for d := range validDecisions {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
