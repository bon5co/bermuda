package runner

import (
	"strings"
	"testing"
)

func TestImproveContractNamesLearningFeedWithoutActivityLimit(t *testing.T) {
	state := t.TempDir()
	text := ImproveContract(state)
	for _, want := range []string{BermudaBin(), state, "IMPROVE", "improve list", "improve read", "improve write", "improve edit", "discovery|mistake|recovery", "--kind", "--body -", "no word or byte cap", "verified recovery", "not commands to execute", "result.json"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(text, "50 words") {
		t.Fatal("learning feed inherited activity cap")
	}
}
