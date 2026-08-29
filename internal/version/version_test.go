package version

import (
	"strings"
	"testing"
)

// A released build states its semver; that is the whole reason Tag exists.
func TestTagWinsWhenSet(t *testing.T) {
	defer func(prev string) { Tag = prev }(Tag)
	Tag = "v1.2.3"
	if got := String(); got != "v1.2.3" {
		t.Errorf("String() = %q, want the tag", got)
	}
	if !strings.Contains(Full(), "v1.2.3") {
		t.Error("Full() should lead with the tag")
	}
}

// Without a tag the version must still say something specific: a test binary is
// built from the repo, so it carries a revision.
func TestFallsBackToSomethingUseful(t *testing.T) {
	defer func(prev string) { Tag = prev }(Tag)
	Tag = ""
	got := String()
	if got == "" {
		t.Fatal("String() is empty; a build must always identify itself")
	}
	if got == "dev" {
		// Acceptable only when the build carries no VCS stamp at all.
		if r := read().revision; r != "" {
			t.Errorf("reported dev despite having revision %s", r)
		}
		return
	}
	// A revision-derived version is short, and only ever marked with '*'.
	if len(strings.TrimSuffix(got, "*")) > revisionLen {
		t.Errorf("String() = %q, longer than a short revision", got)
	}
}

func TestFullDescribesTheTreeState(t *testing.T) {
	out := Full()
	if !strings.HasPrefix(out, "bermuda ") {
		t.Errorf("Full() should name the program: %q", out)
	}
	if read().revision != "" && !strings.Contains(out, "tree") {
		t.Error("Full() should say whether the tree was clean")
	}
}
