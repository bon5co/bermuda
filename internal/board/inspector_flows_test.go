package board

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The panel beside the flows table, driven the way the tab is: flow files in a
// temporary state directory, a cursor on a row, and assertions about what the
// panel said. Nothing here reaches the real store or the real flow directory.

// flowNoBypass is the surprising case: the permission bypass is on by default
// because nobody is sitting in a flow step's pane, so a flow that turns it off
// parks where its author expected it to run.
const flowNoBypass = `about: touches production
skip_permissions: false
steps:
  - id: check
    agent: |
      Ask before doing anything.
`

// flowWithSteps is a sequence longer than the panel will draw, which is the
// case the cap exists for.
func flowWithSteps(n int) string {
	var b strings.Builder
	b.WriteString("about: a long sequence\nsteps:\n")
	for i := 1; i <= n; i++ {
		b.WriteString("  - id: step" + itoa(i) + "\n    run: echo " + itoa(i) + "\n")
	}
	return b.String()
}

// The panel answers what the row cannot: where the file is, what it takes, and
// what it actually does — in order.
func TestTheFlowsInspectorSummarisesTheSelectedFlow(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"triage.yml": flowWithInput})
	fb.putRun(t, "r1", "triage", "parked", time.Now().Add(-time.Hour))
	fb.selectFlow(t, "triage")

	panel := fb.renderFlowInspector(64)
	for _, want := range []string{
		"triage",                // which flow this is
		"triage a ticket",       // its about
		"the ticket to look at", // what it has to be called with
		"assess", "agent",       // the sequence, in order
		"record", "run",
		"parked", // how it went last time
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("the flows inspector does not mention %q:\n%s", want, panel)
		}
	}
	// The path is what somebody who wants to change the flow needs, and the
	// panel is the only place on the tab it appears. It scrolls when it does not
	// fit, so only its beginning is guaranteed on screen at frame 0.
	if head := firstRunes(fb.flows[0].Path, 20); !strings.Contains(panel, head) {
		t.Errorf("the flow's path is not on the panel:\n%s", panel)
	}
	// A step with no model of its own runs on the default, and "agent" alone
	// does not tell a reader what the step will cost.
	if !strings.Contains(panel, "sonnet") {
		t.Errorf("an agent step's model is not shown:\n%s", panel)
	}
	// And it is really on the board, not merely renderable. The path is the
	// marker, because it is the one thing here the table never prints — STEPS is
	// a column title as well as a heading on the panel.
	if !strings.Contains(fb.View(), firstRunes(fb.flows[0].Path, 20)) {
		t.Errorf("the panel does not appear beside the flows table:\n%s", fb.View())
	}
}

// A flow nobody has called must not read as one that ran at the epoch.
func TestTheFlowsInspectorSaysWhenAFlowHasNeverRun(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"sweep.yml": flowWithoutInput})
	fb.selectFlow(t, "sweep")

	panel := fb.renderFlowInspector(64)
	if !strings.Contains(panel, "never run") {
		t.Errorf("an uncalled flow should say so:\n%s", panel)
	}
	// A zero timestamp can only reach the panel through the LAST RUN block, so
	// the absence of that block is the check. Searching the whole panel for
	// "1970" is not: the flow's directory is printed there too, and a
	// t.TempDir() name carries a random number that sometimes begins with those
	// four digits, which failed this test for a reason that had nothing to do
	// with what it is guarding.
	if strings.Contains(panel, "LAST RUN") {
		t.Errorf("an uncalled flow rendered a last run:\n%s", panel)
	}
	// A flow that takes no input runs on enter, and a blank row is
	// indistinguishable from one that failed to render.
	if !strings.Contains(panel, "none") {
		t.Errorf("a flow that takes no input does not say so:\n%s", panel)
	}
}

// The file that will not parse is the one a reader most needs told about, and
// an empty panel beside its row reads as a board that gave up.
func TestTheFlowsInspectorShowsTheParseError(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"broken.yml": flowBroken})
	fb.cursor = 0
	if r, ok := fb.selectedFlow(); !ok || r.err == nil {
		t.Fatal("the broken flow should be the row under the cursor")
	}

	panel := fb.renderFlowInspector(64)
	if strings.TrimSpace(stripStyle(panel)) == "" {
		t.Fatal("a broken flow got an empty panel, which is the case that needs one most")
	}
	if !strings.Contains(panel, "does not parse") {
		t.Errorf("the panel does not say what is wrong with the row:\n%s", panel)
	}
	// The parser's own complaint, naming the key it choked on: without it the
	// reader has to open the file and guess.
	if !strings.Contains(panel, "agnet") {
		t.Errorf("the panel does not carry the parse error:\n%s", panel)
	}
	if head := firstRunes(fb.dir, 20); !strings.Contains(panel, head) {
		t.Errorf("the panel does not say which file failed:\n%s", panel)
	}
}

// A YAML error arrives as a heading with the offending line under it. The path
// is pulled off the front so it can scroll on a line of its own rather than
// wrapping wherever the panel's width happens to fall.
func TestAFlowParseErrorIsSplitIntoFileAndComplaint(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"broken.yml": flowBroken})
	if len(fb.flowErrs) != 1 {
		t.Fatalf("kept %d parse errors, want 1", len(fb.flowErrs))
	}
	path, complaint := flowErrorParts(fb.flowErrs[0])
	if !strings.HasSuffix(path, "broken.yml") {
		t.Errorf("path = %q, want the file that would not parse", path)
	}
	if strings.Contains(complaint, path) {
		t.Errorf("the complaint repeats the path: %q", complaint)
	}
	if strings.ContainsAny(complaint, "\n") {
		t.Errorf("the complaint is more than one line, which the panel counts as one: %q", complaint)
	}
}

// Silent truncation reads as "that is the whole flow", which for a harness
// whose entire point is the sequence is the worst thing the panel could say.
func TestTheFlowsInspectorCountsTheStepsItDidNotShow(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"long.yml": flowWithSteps(flowStepsShown + 4)})
	fb.selectFlow(t, "long")

	panel := fb.renderFlowInspector(64)
	if !strings.Contains(panel, "step1") {
		t.Errorf("the sequence does not start where it starts:\n%s", panel)
	}
	last := "step" + itoa(flowStepsShown+4)
	if strings.Contains(panel, last) {
		t.Errorf("the panel drew %s, past the cap of %d:\n%s", last, flowStepsShown, panel)
	}
	if !strings.Contains(panel, "4 more steps") {
		t.Errorf("the steps that were not drawn are not counted:\n%s", panel)
	}
	// The total is on the panel too, so the count and the visible list cannot
	// disagree without saying so.
	if !strings.Contains(panel, itoa(flowStepsShown+4)) {
		t.Errorf("the panel does not say how many steps the flow has:\n%s", panel)
	}
}

// The bypass is on by default, so the flow that turned it off is the one whose
// steps will park in front of a prompt nobody is there to answer.
func TestTheFlowsInspectorCallsOutABypassThatWasTurnedOff(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"careful.yml": flowNoBypass,
		"sweep.yml":   flowWithoutInput,
	})

	fb.selectFlow(t, "careful")
	off := fb.renderFlowInspector(64)
	if !strings.Contains(off, "prompts") || !strings.Contains(off, "park") {
		t.Errorf("skip_permissions: false is not called out:\n%s", off)
	}

	fb.selectFlow(t, "sweep")
	on := fb.renderFlowInspector(64)
	if !strings.Contains(on, "unrestricted") {
		t.Errorf("the ordinary case should still say what it runs as:\n%s", on)
	}
	if strings.Contains(on, "park") {
		t.Errorf("a flow with the default bypass warned about parking:\n%s", on)
	}
}

// Width is measured in display columns: a styled line carries escape sequences
// that are not printed, so counting runes would flag lines that fit. A line
// past the edge is not merely ugly — lipgloss wraps it, and the extra row is
// one the pane's arithmetic never budgeted for.
func TestNoFlowsInspectorLineExceedsItsWidth(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml":  flowWithInput,
		"careful.yml": flowNoBypass,
		"long.yml":    flowWithSteps(flowStepsShown + 4),
		"broken.yml":  flowBroken,
	})
	fb.putRun(t, "r1", "triage", "parked", time.Now().Add(-time.Hour))

	for _, row := range fb.visibleFlows() {
		fb.cursor = indexOfFlowRow(t, fb, row)
		for width := inspectorMin; width <= inspectorMax; width++ {
			// A few frames, because the marquee scrolls the long values and a
			// wide rune straddling the edge is exactly where sizing goes wrong.
			for _, frame := range []int{0, 7, 40} {
				fb.frame = frame
				for _, line := range strings.Split(fb.renderFlowInspector(width), "\n") {
					if w := lipgloss.Width(line); w > width {
						t.Fatalf("at %d columns a panel line is %d wide: %q", width, w, line)
					}
				}
			}
		}
	}
}

// The whole board, not just the panel. A broken flow's row is free text where
// every other row is columns, and the table's widest line is what decides where
// the panel gets put: an error line left at the pane's width pushed the panel
// off the right of the terminal, which wraps and smears the board.
func TestTheFlowsTabFitsThePaneBesideThePanel(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{
		"triage.yml": flowWithInput,
		"long.yml":   flowWithSteps(flowStepsShown + 4),
		"broken.yml": flowBroken,
	})
	for _, width := range []int{60, 100, 126, 160, 200} {
		fb.width = width
		for i := range fb.visibleFlows() {
			fb.cursor = i
			// The body is the table and the panel joined, which is the part this
			// is about. The help line has its own width problem and its own
			// comment saying so.
			for _, line := range strings.Split(fb.listPane().body, "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("at %d columns, row %d selected, a line is %d wide: %q",
						width, i, w, line)
				}
			}
		}
	}
}

// Below the minimum the labels and values collide, so the panel is dropped
// rather than drawn cramped — the same call the jobs tab makes, from the same
// constants.
func TestTheFlowsInspectorDisappearsWhenTheTableFillsThePane(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"triage.yml": flowWithInput})
	fb.selectFlow(t, "triage")

	for _, width := range []int{60, 100} {
		fb.width = width
		if w := fb.inspectorWidth(); w != 0 {
			t.Fatalf("at %d columns the table leaves %d for a panel, want none", width, w)
		}
		if got := fb.inspector(fb.renderFlowInspector); got != "" {
			t.Fatalf("at %d columns the panel was drawn anyway:\n%s", width, got)
		}
		if path := firstRunes(fb.flows[0].Path, 20); strings.Contains(fb.View(), path) {
			t.Fatalf("at %d columns the panel is on the board:\n%s", width, fb.View())
		}
	}

	fb.width = 160
	if fb.inspector(fb.renderFlowInspector) == "" {
		t.Error("a wide pane should earn a panel")
	}
}

// A row with nothing under the cursor gets no panel, rather than a panel about
// whatever the previous tab had selected.
func TestTheFlowsInspectorIsEmptyWithNoSelection(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"triage.yml": flowWithInput})
	fb.cursor = 99
	if got := fb.renderFlowInspector(64); got != "" {
		t.Errorf("a cursor past the list drew a panel:\n%s", got)
	}
	fb.selectTab(focusJobs)
	if got := fb.renderFlowInspector(64); got != "" {
		t.Errorf("the flows panel drew on another tab:\n%s", got)
	}
}

// firstRunes is the leading part of a long value, which is all the marquee
// guarantees is on screen at rest.
func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// stripStyle removes escape sequences so a test can ask whether anything was
// actually said, rather than whether anything was written.
func stripStyle(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc && r == 'm':
			esc = false
		case esc:
		case r == 0x1b:
			esc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// indexOfFlowRow finds a row's position, so a test can put the cursor on every
// row in turn including the broken ones, which have no id to look up.
func indexOfFlowRow(t *testing.T, fb *flowBoard, want flowRow) int {
	t.Helper()
	for i, r := range fb.visibleFlows() {
		if r.err == want.err && r.flow.ID == want.flow.ID {
			return i
		}
	}
	t.Fatal("the row is not on the tab")
	return 0
}

// A step that hands work back is not a step like the others: a run sitting on
// it for forty minutes is a flow healing itself, and a reader who cannot see
// the edge diagnoses that as a hung agent.
func TestTheFlowsInspectorShowsABackwardEdge(t *testing.T) {
	fb := newFlowBoard(t, map[string]string{"heal.yml": `about: fix and check
steps:
  - id: implement
    agent: write the thing
  - id: verify
    agent: review the diff
    on_fail:
      goto: implement
      max_loops: 2
`})
	fb.selectFlow(t, "heal")

	panel := fb.renderFlowInspector(64)
	if !strings.Contains(panel, "↺implement") {
		t.Errorf("the flows inspector does not show where verify hands back:\n%s", panel)
	}
}
