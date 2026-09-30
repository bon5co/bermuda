package version

import (
	"runtime/debug"
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

func TestEmbeddedIdentity(t *testing.T) {
	const hash = "189e7e2a6869"
	tests := []struct {
		name, version, want string
		settings            []debug.BuildSetting
	}{
		{name: "release", version: "v3.2.0", want: "v3.2.0"},
		{name: "prerelease", version: "v3.3.0-rc.1", want: "v3.3.0-rc.1"},
		{name: "dirty release", version: "v3.2.0+dirty", want: "v3.2.0+dirty"},
		{name: "installed pseudo", version: "v3.0.0-20260908092008-" + hash, want: "189e7e2"},
		{name: "next release pseudo", version: "v3.2.1-0.20260908092008-" + hash, want: "189e7e2"},
		{name: "prerelease pseudo", version: "v3.3.0-rc.1.0.20260908092008-" + hash, want: "189e7e2"},
		{name: "zero base pseudo", version: "v0.0.0-20260908092008-" + hash, want: "189e7e2"},
		{name: "dirty pseudo", version: "v3.2.1-0.20260908092008-" + hash + "+dirty", want: "189e7e2*"},
		{name: "VCS overrides shortened module revision", version: "v3.0.0-20260908092008-" + hash, want: "abcdef0*", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef0123456789"}, {Key: "vcs.modified", Value: "true"}}},
		{name: "unstamped", version: "(devel)", want: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := short(parse(&debug.BuildInfo{Main: debug.Module{Version: tt.version}, Settings: tt.settings}))
			if got != tt.want {
				t.Fatalf("identity = %q, want %q", got, tt.want)
			}
		})
	}
}
