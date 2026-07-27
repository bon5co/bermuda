package board

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bon5co/bermuda/internal/herdrcli"
	"github.com/bon5co/bermuda/internal/store"
)

// The FLOWS tab, driven the way a person drives it: write flow files, send
// keys, assert what the board did with them.
//
// Nothing here reaches the real thing. The state directory is a temporary one
// and the two flow dependencies are fakes that only record — a test that used
// the real ones would launch agents on whichever machine ran it.

// The three flows every test here works with: one that needs an input, one that
// does not, and one that does not parse.
const (
	flowWithInput = `about: triage a ticket
input: the ticket to look at
steps:
  - id: assess
    agent: |
      Look at {{input}} and say whether it matters.
  - id: record
    run: echo "$BERMUDA_PREVIOUS"
`
	flowWithoutInput = `about: sweep the workspace
steps:
  - id: sweep
    run: echo swept
`
	// `agnet` is the failure the flow package refuses files for: a dropped key
	// is a step that never runs, in a flow that reports success.
	flowBroken = `about: this one does not parse
steps:
  - id: nope
    agnet: a typo where agent should be
`
)

// startedFlow is one call the board made to the command layer.
type startedFlow struct {
	id    string
	input string
}

// flowBoard is a board over a private state directory with flows on disk, plus
// the record of what it asked the command layer to do.
type flowBoard struct {
	*Model
	started []startedFlow
	resumed []string
	dir     string
}

func newFlowBoard(t *testing.T, files map[string]string) *flowBoard {
	t.Helper()
	// A state directory of this test's own. The real one holds a live store and
	// the flows this machine actually runs.
	state := t.TempDir()
	t.Setenv("BERMUDA_STATE_DIR", state)

	dir := filepath.Join(state, "flows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	fb := &flowBoard{dir: dir}
	fb.Model = New(s, herdrcli.New(), Deps{
		Run: func(store.Job, string) error { return nil },
		RunFlow: func(id, input string) error {
			fb.started = append(fb.started, startedFlow{id: id, input: input})
			return nil
		},
		ResumeFlow: func(runID string) error {
			fb.resumed = append(fb.resumed, runID)
			return nil
		},
		FlowDir:       dir,
		DaemonRunning: func() bool { return true },
		EnsureDaemon:  func() error { return nil },
	})
	fb.width, fb.height = 160, 40
	fb.daemonUp = true
	fb.apply(t, fb.load()())
	fb.selectTab(focusFlows)
	return fb
}

// putRun records a run of a flow, the way the command layer would.
func (fb *flowBoard) putRun(t *testing.T, id, flowID, outcome string, at time.Time) {
	t.Helper()
	err := fb.store.PutRun(context.Background(), store.Run{
		ID: id, JobID: flowID, Flow: flowID, Trigger: "manual",
		Outcome: outcome, StartedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	fb.apply(t, fb.load()())
	fb.selectTab(focusFlows)
}

// selectFlow puts the cursor on a flow by id, so a test never depends on where
// in the list it happened to land.
func (fb *flowBoard) selectFlow(t *testing.T, id string) {
	t.Helper()
	for i, r := range fb.visibleFlows() {
		if r.err == nil && r.flow.ID == id {
			fb.cursor = i
			return
		}
	}
	t.Fatalf("no flow %q on the tab", id)
}

// A flow that will not parse is invisible everywhere else, which reads as "I
// never wrote that one". It is the flow most in need of being seen.
func TestTheFlowsTabListsFlowsIncludingTheBrokenOnes(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml": flowWithInput,
		"sweep.yml":  flowWithoutInput,
		"broken.yml": flowBroken,
	})

	if len(fb.flows) != 2 {
		t.Fatalf("loaded %d flows, want the 2 that parse", len(fb.flows))
	}
	if len(fb.flowErrs) != 1 {
		t.Fatalf("kept %d parse errors, want 1", len(fb.flowErrs))
	}
	if got := len(fb.visibleFlows()); got != 3 {
		t.Fatalf("the tab shows %d rows, want all 3 flow files", got)
	}

	out := fb.renderFlows(0, len(fb.visibleFlows()))
	for _, want := range []string{"triage", "sweep", "broken.yml"} {
		if !strings.Contains(out, want) {
			t.Errorf("the flows table does not mention %q:\n%s", want, out)
		}
	}
	// The declared input belongs on screen: it is what the reader has to supply
	// and the only place it is written down is the file.
	if !strings.Contains(out, "the ticket to look at") {
		t.Error("a flow's declared input is not shown, so nobody can tell what to type")
	}
}

// A flow nobody has called must not read as one that ran at the epoch.
func TestAFlowThatHasNeverRunSaysSo(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"sweep.yml": flowWithoutInput})
	out := fb.renderFlows(0, len(fb.visibleFlows()))
	if !strings.Contains(out, "never") {
		t.Errorf("an uncalled flow should read as never:\n%s", out)
	}
	if strings.Contains(out, "1970") {
		t.Errorf("an uncalled flow rendered a zero timestamp:\n%s", out)
	}
}

// The latest run of the flow, found by flow id rather than by job id: a flow
// called directly has no job to be keyed under.
func TestTheTabShowsTheLatestRunOfEachFlow(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"sweep.yml": flowWithoutInput})
	fb.putRun(t, "old", "sweep", "done", time.Now().Add(-2*time.Hour))
	fb.putRun(t, "new", "sweep", "parked", time.Now().Add(-time.Minute))

	if got := fb.lastFlow["sweep"].ID; got != "new" {
		t.Fatalf("the tab is showing run %q, want the most recent one", got)
	}
	if !strings.Contains(fb.renderFlows(0, len(fb.visibleFlows())), "parked") {
		t.Error("the flow's state is not on its row")
	}
}

// A flow that declares an input must not start with a blank one: every prompt
// saying {{input}} would get a hole where its subject should be.
func TestAFlowThatNeedsAnInputCannotBeLaunchedBlank(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"triage.yml": flowWithInput})
	fb.selectFlow(t, "triage")

	fb.pressSpecial(t, tea.KeyEnter)
	if fb.flowInput == nil {
		t.Fatal("enter on a flow that takes an input should ask for it")
	}
	if len(fb.started) != 0 {
		t.Fatalf("the flow was started before the input was given: %v", fb.started)
	}

	// Committing an empty box must refuse rather than close: closing silently
	// looks exactly like launching.
	fb.pressSpecial(t, tea.KeyEnter)
	if fb.flowInput == nil {
		t.Fatal("an empty input closed the box instead of refusing")
	}
	if fb.flowInput.err == "" {
		t.Error("the refusal says nothing, so nothing on screen explains it")
	}
	if len(fb.started) != 0 {
		t.Fatalf("a blank input started the flow anyway: %v", fb.started)
	}

	fb.typeText(t, "  ticket 41  ")
	fb.apply(t, tea.KeyMsg{Type: tea.KeyEnter})
	if fb.flowInput != nil {
		t.Error("a filled input should close the box")
	}
	if len(fb.started) != 1 {
		t.Fatalf("started %d flows, want 1", len(fb.started))
	}
	if fb.started[0] != (startedFlow{id: "triage", input: "ticket 41"}) {
		t.Errorf("started %+v, want triage with the typed input, trimmed", fb.started[0])
	}
}

// A flow that declares no input needs no box in the way.
func TestAFlowWithNoInputRunsStraightAway(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"sweep.yml": flowWithoutInput})
	fb.selectFlow(t, "sweep")
	fb.apply(t, tea.KeyMsg{Type: tea.KeyEnter})

	if fb.flowInput != nil {
		t.Fatal("a flow that takes no input should not be asked for one")
	}
	if len(fb.started) != 1 || fb.started[0].id != "sweep" {
		t.Fatalf("started %+v, want one call for sweep", fb.started)
	}
}

// The box owns the keyboard while it is open, or a `q` typed into an input
// would quit the board mid-sentence.
func TestTheInputBoxOwnsTheKeyboard(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"triage.yml": flowWithInput})
	fb.selectFlow(t, "triage")
	fb.pressSpecial(t, tea.KeyEnter)
	before := fb.cursor

	fb.typeText(t, "quit j k 3")
	if fb.flowInput == nil {
		t.Fatal("typing closed the box")
	}
	if got := fb.flowInput.input.Value(); got != "quit j k 3" {
		t.Errorf("the box holds %q, want the text that was typed", got)
	}
	if fb.cursor != before {
		t.Error("a key pressed in the box moved the list behind it")
	}
}

// There is nothing to run: the file did not parse, so it has no steps.
func TestABrokenFlowCannotBeLaunched(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"broken.yml": flowBroken})
	fb.cursor = 0
	if r, ok := fb.selectedFlow(); !ok || r.err == nil {
		t.Fatal("the broken flow should be the row under the cursor")
	}
	fb.apply(t, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fb.started) != 0 {
		t.Fatalf("a flow that does not parse was launched: %v", fb.started)
	}
	if fb.err == nil {
		t.Error("enter on a broken flow said nothing, which reads as a broken board")
	}
}

// Only a parked run can be resumed. Reaching this key on a finished one would
// re-run steps that already cost money.
func TestUnparkDoesNothingToARunThatIsNotParked(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"sweep.yml": flowWithoutInput})
	fb.putRun(t, "r-done", "sweep", "done", time.Now().Add(-time.Hour))
	fb.selectFlow(t, "sweep")

	fb.press(t, "u")
	if len(fb.resumed) != 0 {
		t.Fatalf("u resumed a run that was not parked: %v", fb.resumed)
	}
	if !strings.Contains(fb.status, "not parked") {
		t.Errorf("status is %q, want it to say why nothing happened", fb.status)
	}
}

// The gap this tab exists to close: a parked flow was visible on the board and
// could only be resumed by leaving it and typing a run id nobody memorises.
func TestUnparkResumesTheLatestParkedRun(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"sweep.yml": flowWithoutInput})
	fb.putRun(t, "r-parked", "sweep", "parked", time.Now().Add(-time.Minute))
	fb.selectFlow(t, "sweep")

	fb.press(t, "u")
	if len(fb.resumed) != 1 || fb.resumed[0] != "r-parked" {
		t.Fatalf("resumed %v, want the parked run r-parked", fb.resumed)
	}
	// Resume goes at the run, never at the flow: starting the flow again by name
	// would redo the steps the parked run already paid for.
	if len(fb.started) != 0 {
		t.Errorf("u started a fresh run as well: %v", fb.started)
	}
}

// A flow the reader cannot see must not be the one a key acts on.
func TestFlowKeysRespectTheFilter(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml": flowWithInput,
		"sweep.yml":  flowWithoutInput,
	})
	fb.query = "sweep"
	fb.cursor = 0
	r, ok := fb.selectedFlow()
	if !ok || r.flow.ID != "sweep" {
		t.Fatalf("selected %q (%v), want sweep", r.flow.ID, ok)
	}
}

// One row is one line, including a broken one — a YAML error arrives with the
// offending line under a heading, and a row that is secretly two breaks the
// arithmetic the pane is sized by.
func TestEveryFlowRowIsExactlyOneLine(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml": flowWithInput,
		"sweep.yml":  flowWithoutInput,
		"broken.yml": flowBroken,
	})
	rows := len(fb.visibleFlows())
	out := strings.TrimRight(fb.renderFlows(0, rows), "\n")
	// One line per row, plus the column titles.
	if got := strings.Count(out, "\n") + 1; got != rows+1 {
		t.Errorf("rendered %d lines for %d rows:\n%s", got, rows, out)
	}
}

// The open input box is chrome, and every row it takes has to come out of the
// list rather than off the bottom of the pane: a view one row taller than the
// pane makes the terminal scroll and smears the whole board upwards.
func TestTheFlowsTabFitsThePaneWithTheInputBoxOpen(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml": flowWithInput,
		"sweep.yml":  flowWithoutInput,
		"broken.yml": flowBroken,
	})
	for _, width := range []int{60, 100, 160} {
		for height := 6; height <= 30; height++ {
			for _, prompting := range []bool{false, true} {
				fb.width, fb.height = width, height
				fb.flowInput = nil
				if prompting {
					fb.selectFlow(t, "triage")
					fb.pressSpecial(t, tea.KeyEnter)
				}
				if rows := strings.Count(fb.View(), "\n") + 1; rows > height {
					t.Fatalf("%dx%d, box open %v: the view renders %d rows",
						width, height, prompting, rows)
				}
			}
		}
	}
}

// Width is measured in display columns: a styled line carries escape sequences
// that are not printed, so counting runes would flag lines that fit.
func TestNoFlowsTableLineExceedsThePane(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml": flowWithInput,
		"sweep.yml":  flowWithoutInput,
		"broken.yml": flowBroken,
	})
	for _, width := range []int{60, 100, 160} {
		fb.width = width
		table := fb.renderFlows(0, len(fb.visibleFlows()))
		for _, line := range strings.Split(table, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("at %d columns a row is %d wide: %q", width, w, line)
			}
		}
	}
}

// A board wired with no flow directory would otherwise read as an installation
// with no flows, which is a wrong answer rather than an empty one.
func TestABoardWithNoFlowDirectorySaysSoRatherThanShowingNone(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	m := New(s, herdrcli.New(), Deps{})
	flows, errs := m.readFlows()
	if len(flows) != 0 {
		t.Fatalf("read %d flows from nowhere", len(flows))
	}
	if len(errs) != 1 {
		t.Fatalf("reported %d problems, want 1 naming the missing directory", len(errs))
	}
}
