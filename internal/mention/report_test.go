package mention

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// What a caller of Deliver does next is decided almost entirely by the Result:
// the CLI prints Report to stderr, the board prints Status in the one line it
// has, and a caller that wants to know whether anybody was actually interrupted
// asks Reached. All three read the same struct, and a wrong answer from any of
// them is silent — an author told "told review-agent" stops waiting and starts
// expecting a reply that is never coming.

// Describe is the name a person sees in every delivery report. It has to come
// from whatever the agent actually carries, because most agents carry only some
// of it: one started outside bermuda has no Name, one started without a pane
// label has no Label, and the fallback chain is the only thing standing between
// a readable report and a wall of pane ids.
func TestDescribeNamesAnAgentByTheFirstNameItHas(t *testing.T) {
	cases := []struct {
		name  string
		agent Agent
		want  string
	}{
		{
			name:  "a registered name wins over everything else",
			agent: Agent{Target: "w1:p3", Name: "review-agent", Label: "reviewing", Dir: "/home/dev/bermuda"},
			want:  "review-agent (w1:p3)",
		},
		{
			name:  "the pane label stands in when herdr has no name",
			agent: Agent{Target: "w1:p3", Label: "reviewing", Dir: "/home/dev/bermuda"},
			want:  "reviewing (w1:p3)",
		},
		{
			name:  "the working directory's basename is the last name",
			agent: Agent{Target: "w1:p3", Dir: "/home/dev/bermuda"},
			want:  "bermuda (w1:p3)",
		},
		{
			name:  "a trailing separator does not turn the directory into an empty name",
			agent: Agent{Target: "w1:p3", Dir: "/home/dev/bermuda/"},
			want:  "bermuda (w1:p3)",
		},
		{
			// Nothing but a pane. The target is shown bare rather than as
			// "(w1:p3)" with an empty name in front of it.
			name:  "an agent with no names at all is its target",
			agent: Agent{Target: "w1:p3"},
			want:  "w1:p3",
		},
		{
			// Whitespace is not a name. Reporting " (w1:p3)" would read as an
			// agent whose name failed to print.
			name:  "a blank name is not a name",
			agent: Agent{Target: "w1:p3", Name: "   ", Label: "\t"},
			want:  "w1:p3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.agent.Describe(); got != tc.want {
				t.Errorf("Describe() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Reached answers one question: was anybody interrupted? Every other outcome —
// a mention that matched nobody, a delivery that failed, an `@all` refused for
// want of a workspace — leaves the message sitting in the thread to be read
// later. A caller that treated any of those as "reached" would report a push
// that never happened.
func TestReachedIsTrueOnlyWhenSomebodyWasTold(t *testing.T) {
	agent := Agent{Target: "w1:p1", Name: "one"}
	cases := []struct {
		name   string
		result Result
		want   bool
	}{
		{"nothing happened", Result{}, false},
		{"somebody was told", Result{Delivered: []Delivery{{Agent: agent}}}, true},
		{"a delivery failed", Result{Failed: []Delivery{{Agent: agent, Err: errors.New("gone")}}}, false},
		{"the mention matched nobody", Result{Missed: []string{"ghost"}}, false},
		{"the only mention was the speaker", Result{Mine: []string{"one"}}, false},
		{"an @all was refused", Result{Refused: []string{"all"}}, false},
		{
			"a failure alongside a delivery is still a delivery",
			Result{
				Delivered: []Delivery{{Agent: agent}},
				Failed:    []Delivery{{Agent: Agent{Target: "w1:p2"}, Err: errors.New("gone")}},
			},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.Reached(); got != tc.want {
				t.Errorf("Reached() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Empty is what decides whether anything is printed at all, so every field has
// to count. A field left out of it would make its outcome invisible: the post
// would succeed, the report would say nothing, and nobody would learn that the
// mention went nowhere.
func TestEveryOutcomeMakesTheResultNonEmpty(t *testing.T) {
	agent := Agent{Target: "w1:p1", Name: "one"}
	cases := []struct {
		name   string
		result Result
	}{
		{"delivered", Result{Delivered: []Delivery{{Agent: agent}}}},
		{"failed", Result{Failed: []Delivery{{Agent: agent, Err: errors.New("gone")}}}},
		{"missed", Result{Missed: []string{"ghost"}}},
		{"mine", Result{Mine: []string{"one"}}},
		{"refused", Result{Refused: []string{"all"}}},
	}
	if !(Result{}).Empty() {
		t.Error("a zero Result is not Empty")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.result.Empty() {
				t.Errorf("a Result carrying %s reports Empty", tc.name)
			}
		})
	}
}

// The board has one line for this, and an empty line is how it says "nothing to
// report". Anything that prints an empty summary for a result that did have an
// outcome loses that outcome entirely.
func TestStatusIsEmptyOnlyWhenThereIsNothingToSay(t *testing.T) {
	if got := Status(Result{}); got != "" {
		t.Errorf("Status of a zero Result = %q, want the empty string", got)
	}
}

func TestStatusNamesWhatHappenedToEachMention(t *testing.T) {
	told := Agent{Target: "w1:p1", Name: "review-agent"}
	gone := Agent{Target: "w1:p2", Name: "sonnet-scraper"}

	cases := []struct {
		name    string
		result  Result
		want    []string
		notWant []string
	}{
		{
			name:   "a delivery names the agent that was interrupted",
			result: Result{Delivered: []Delivery{{Agent: told}}},
			want:   []string{"told", "review-agent", "w1:p1"},
		},
		{
			name: "two deliveries name both",
			result: Result{Delivered: []Delivery{
				{Agent: told}, {Agent: gone},
			}},
			want: []string{"review-agent", "sonnet-scraper"},
		},
		{
			// The count is what matters on one line; Report carries the errors.
			// What must not happen is a failure reading as a delivery.
			name:    "a failure is counted and not reported as told",
			result:  Result{Failed: []Delivery{{Agent: gone, Err: errors.New("agent_not_found")}}},
			want:    []string{"1", "could not be told"},
			notWant: []string{"told sonnet-scraper"},
		},
		{
			name:   "a mention nobody answers to is named",
			result: Result{Missed: []string{"ghost", "retired"}},
			want:   []string{"@ghost", "@retired"},
		},
		{
			// Refused is the one outcome an author is most likely to
			// misread as delivery, so the line has to say the message
			// waits rather than pushes.
			name:   "a refused @all says it will be read at leisure",
			result: Result{Refused: []string{"all"}},
			want:   []string{"@all", "leisure"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Status(tc.result)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("Status() = %q, want it to mention %q", got, want)
				}
			}
			for _, unwanted := range tc.notWant {
				if strings.Contains(got, unwanted) {
					t.Errorf("Status() = %q, want it not to say %q", got, unwanted)
				}
			}
		})
	}
}

// One post can end in several outcomes at once — some agents told, one gone,
// one name nobody answers to. All of them belong on the line; a summary that
// reported only the first would hide the rest behind a success.
func TestStatusCarriesEveryOutcomeOfOnePost(t *testing.T) {
	r := Result{
		Delivered: []Delivery{{Agent: Agent{Target: "w1:p1", Name: "review-agent"}}},
		Failed:    []Delivery{{Agent: Agent{Target: "w1:p2", Name: "scraper"}, Err: errors.New("agent_not_found")}},
		Missed:    []string{"ghost"},
		Refused:   []string{"all"},
	}
	got := Status(r)
	for _, want := range []string{"review-agent", "could not be told", "@ghost", "@all"} {
		if !strings.Contains(got, want) {
			t.Errorf("Status() = %q, want it to mention %q", got, want)
		}
	}
	// One line, whatever happened: the board has no room for a second.
	if strings.Contains(got, "\n") {
		t.Errorf("Status() = %q, want a single line", got)
	}
}

// A mention of yourself is not an outcome anybody needs told about — it is what
// `@all` does to the agent that sent it, on every send. Reporting it would put
// a line on the board for every broadcast saying nothing happened.
func TestStatusSaysNothingWhenTheOnlyMentionWasTheSpeaker(t *testing.T) {
	r := Result{Mine: []string{"review-agent"}}
	if r.Empty() {
		t.Fatal("a self-mention should still be a non-empty Result: Report says something about it")
	}
	if got := Status(r); got != "" {
		t.Errorf("Status() = %q, want the empty string for a self-mention", got)
	}
}

// Report and Status are the same outcome at two lengths, and they are read by
// the same person in the same session. An agent that was actually interrupted
// has to be named in both: that is the name the author will look for when they
// wonder who is answering.
//
// A failure is the one thing the short form only counts. Report has the room to
// name the agent and quote the error; the board has one line, and the number is
// what fits. That difference is deliberate, so it is pinned here rather than
// left to be "fixed" into a longer line later.
func TestReportAndStatusAgreeOnWhoWasTold(t *testing.T) {
	r := Result{
		Delivered: []Delivery{{Agent: Agent{Target: "w1:p1", Name: "review-agent"}}},
		Failed:    []Delivery{{Agent: Agent{Target: "w1:p2", Label: "scraping"}, Err: errors.New("agent_not_found")}},
		Missed:    []string{"ghost"},
	}
	var buf bytes.Buffer
	Report(&buf, r)
	long, short := buf.String(), Status(r)

	for _, who := range []string{"review-agent", "ghost"} {
		if !strings.Contains(long, who) {
			t.Errorf("Report() = %q, want it to mention %q", long, who)
		}
		if !strings.Contains(short, who) {
			t.Errorf("Status() = %q, want it to mention %q", short, who)
		}
	}
	if !strings.Contains(long, "scraping") {
		t.Errorf("Report() = %q, want it to name the agent it could not tell", long)
	}
	if !strings.Contains(short, "1 could not be told") {
		t.Errorf("Status() = %q, want it to count the failure", short)
	}
}

// Report is what a person sees on stderr after a post, and each outcome has to
// be distinguishable: a failure that read like a delivery would leave the
// author waiting, and a refused @all that read like a failure would send them
// looking for a broken herdr.
func TestReportSaysSomethingDistinctForEachOutcome(t *testing.T) {
	cases := []struct {
		name   string
		result Result
		want   []string
	}{
		{
			name:   "delivered",
			result: Result{Delivered: []Delivery{{Agent: Agent{Target: "w1:p1", Name: "review-agent"}}}},
			want:   []string{"told", "review-agent"},
		},
		{
			name:   "failed carries the error and says the message survived",
			result: Result{Failed: []Delivery{{Agent: Agent{Target: "w1:p2", Name: "scraper"}, Err: errors.New("agent_not_found")}}},
			want:   []string{"could not tell", "scraper", "agent_not_found", "still in the thread"},
		},
		{
			name:   "missed says it may simply have finished",
			result: Result{Missed: []string{"ghost"}},
			want:   []string{"@ghost", "finished"},
		},
		{
			name:   "a self-mention is explained rather than dropped",
			result: Result{Mine: []string{"review-agent"}},
			want:   []string{"@review-agent", "is you"},
		},
		{
			name:   "a refused @all warns about the change in timing",
			result: Result{Refused: []string{"all"}},
			want:   []string{"warning", "@all", "leisure"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			Report(&buf, tc.result)
			got := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("Report() = %q, want it to mention %q", got, want)
				}
			}
		})
	}
}

// Nothing happened, nothing is printed. Report goes to stderr at every call
// site, so a blank line on every post would be noise in every session.
func TestReportIsSilentForAnEmptyResult(t *testing.T) {
	var buf bytes.Buffer
	Report(&buf, Result{})
	if buf.Len() != 0 {
		t.Errorf("Report() wrote %q for an empty Result, want nothing", buf.String())
	}
}
