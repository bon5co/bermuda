package runner

import (
	"strings"
	"testing"
)

func TestALogContractNamesCLIStateAndMeaningfulUpdates(t *testing.T) {
	state := t.TempDir()
	text := ALogContract(state)
	for _, want := range []string{BermudaBin(), state, "alog list --limit 20", "alog write", "--repo", "--branch", "--topic", "--body", "50 words", "milestones", "blockers", "finish", "alog edit", "result.json"} {
		if !strings.Contains(text, want) {
			t.Errorf("contract missing %q", want)
		}
	}
}
