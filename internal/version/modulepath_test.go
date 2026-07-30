package version

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The module path has to carry the major version, and nothing in a normal build
// notices when it does not.
//
// Bermuda shipped v2.0.0 through v2.4.0 with `module github.com/bon5co/bermuda`
// in go.mod. Everything worked — it builds, it tests, `herdr plugin install`
// installs it, because both of those build from a checkout and never ask the
// module proxy anything. The one path that does ask is the one in the README:
//
//	go install github.com/bon5co/bermuda/cmd/bermuda@latest
//
// Go's rule is that a module tagged v2 or above must say so in its path, so the
// v2 tags were invisible to it and `@latest` resolved to the newest tag that was
// still v1 — installing v1.1.1, from before flows and threads existed, and
// reporting itself as v1.1.1 without a word of complaint. Asking for a v2 tag
// explicitly at least errored. An agent that followed the documented command got
// a bermuda that was missing half the commands the same document describes.
//
// So the two places the major version is written down are checked against each
// other here: go.mod, which decides what `go install` can see, and the plugin
// manifest, which is what a release bumps by hand.

// moduleLine is the first line of go.mod: `module <path>`.
var moduleLine = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)

// manifestVersion is the plugin manifest's own version, `version = "2.4.0"`.
var manifestVersion = regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"`)

func TestModulePathCarriesTheReleasedMajor(t *testing.T) {
	root := repoRoot(t)

	modPath := match(t, filepath.Join(root, "go.mod"), moduleLine)
	release := match(t, filepath.Join(root, "herdr-plugin.toml"), manifestVersion)

	major, err := strconv.Atoi(strings.SplitN(release, ".", 2)[0])
	if err != nil {
		t.Fatalf("herdr-plugin.toml version %q does not start with a major version: %v", release, err)
	}

	want := ""
	if major >= 2 {
		want = "/v" + strconv.Itoa(major)
	}
	base := strings.TrimSuffix(modPath, want)
	switch {
	case want == "" && majorSuffix(modPath) != "":
		t.Fatalf("go.mod says %q but the release is v%s: a v1 module must have no major suffix",
			modPath, release)
	case want != "" && !strings.HasSuffix(modPath, want):
		t.Fatalf("go.mod says %q but the release is v%s, so `go install %s/cmd/bermuda@latest` "+
			"cannot see it and silently installs the newest v1 tag instead. The module path "+
			"needs %q — remember the imports and the Makefile's VERSION_PKG.",
			modPath, release, modPath, base+want)
	}
}

// majorSuffix is the `/vN` a module path ends with, or empty.
func majorSuffix(modPath string) string {
	i := strings.LastIndex(modPath, "/v")
	if i < 0 {
		return ""
	}
	if _, err := strconv.Atoi(modPath[i+2:]); err != nil {
		return ""
	}
	return modPath[i:]
}

// repoRoot is where go.mod lives, found by walking up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above this package")
		}
		dir = parent
	}
}

func match(t *testing.T, path string, re *regexp.Regexp) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := re.FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s does not say what this test is about to check", path)
	}
	return string(m[1])
}
