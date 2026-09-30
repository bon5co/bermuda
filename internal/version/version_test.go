package version

import (
	"fmt"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

func TestPseudoVersionUsesPluginVersionAndRevision(t *testing.T) {
	for _, tc := range []struct{ module, want string }{
		{"v3.0.0-20260908092008-189e7e2a6869", "v3.2.0+g189e7e2"},
		{"v3.0.0-20260908092008-189e7e2a6869+dirty", "v3.2.0+g189e7e2*"},
	} {
		info := &debug.BuildInfo{Main: debug.Module{Version: tc.module}, Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "189e7e2a68693ab97581046808111b6ada332cd8"},
		}}
		if got := short(parse(info)); got != tc.want {
			t.Errorf("short(%q) = %q, want %q", tc.module, got, tc.want)
		}
	}
}

func TestModifiedReleaseNamesItsRevision(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v3.2.0+dirty"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "189e7e2a68693ab97581046808111b6ada332cd8"},
		{Key: "vcs.modified", Value: "true"},
	}}
	if got := short(parse(info)); got != "v3.2.0+g189e7e2*" {
		t.Fatalf("short modified release = %q, want revision and modified marker", got)
	}
}

func TestPluginVersionMatchesManifest(t *testing.T) {
	raw, err := os.ReadFile("../../herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^version = "([^"]+)"$`).FindSubmatch(raw)
	if len(match) != 2 || pluginVersion != "v"+string(match[1]) {
		t.Fatalf("plugin version %q does not match herdr-plugin.toml", pluginVersion)
	}
}

func FuzzPseudoVersionNeverBecomesReleaseTag(f *testing.F) {
	f.Add(uint8(3), uint8(2), uint8(0), uint64(20260908092008), uint64(0x189e7e2a6869))
	f.Fuzz(func(t *testing.T, major, minor, patch uint8, timestamp, revision uint64) {
		date := timestamp % 100000000000000
		hash := revision % 0x1000000000000
		for _, v := range []string{
			fmt.Sprintf("v%d.%d.%d-%014d-%012x", major, minor, patch, date, hash),
			fmt.Sprintf("v%d.%d.%d-0.%014d-%012x", major, minor, patch, date, hash),
			fmt.Sprintf("v%d.%d.%d-rc.1.0.%014d-%012x", major, minor, patch, date, hash),
		} {
			got := parse(&debug.BuildInfo{Main: debug.Module{Version: v}})
			if got.tag != "" {
				t.Fatalf("pseudo-version %q accepted as release tag %q", v, got.tag)
			}
		}
	})
}

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
	// A revision-derived version names the plugin version and short revision.
	info := read()
	if info.tag != "" && !info.modified {
		if got != info.tag {
			t.Errorf("String() = %q, want release %q", got, info.tag)
		}
		return
	}
	if r := info.revision; len(r) >= revisionLen && !strings.Contains(got, "+g"+r[:revisionLen]) {
		t.Errorf("String() = %q, missing short revision", got)
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
