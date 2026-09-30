// Package version reports which build of bermuda is running.
//
// The identity comes from Go's embedded build information, so a plain go build
// reports a version without requiring build flags or a runtime git checkout.
// Go sometimes records a module pseudo-version even at a release tag; those
// builds use the plugin manifest's version plus their exact source revision.
package version

import (
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

// Tag is set at build time for a released version:
//
//	go build -ldflags "-X github.com/bon5co/bermuda/v3/internal/version.Tag=v1.2.3"
//
// When it is set it wins, because a human-chosen semver says more than a
// commit hash. When it is not, the revision is the honest answer.
var Tag string

// Keep this in sync with herdr-plugin.toml; TestPluginVersionMatchesManifest
// catches a release that changes the manifest without changing this label.
const pluginVersion = "v3.2.0"

var pseudoVersion = regexp.MustCompile(`(?:-|\.)0?\.?\d{14}-[0-9a-f]{12,}$`)

// revisionLen is how much of the commit hash to show. Seven is enough to be
// unambiguous in a repo this size and short enough to sit in a header.
const revisionLen = 7

// String is the short version, for a header or a status line.
//
// A build from a modified tree is marked with a trailing '*': it corresponds to
// no commit anyone could check out, and saying so is the difference between
// "this is revision X" and "this is roughly revision X".
func String() string {
	if Tag != "" {
		return Tag
	}
	return short(read())
}

func short(info buildInfo) string {
	if info.tag != "" && !info.modified {
		return info.tag
	}
	if info.revision == "" {
		if info.tag != "" {
			return info.tag + "*"
		}
		return "dev"
	}
	revision := info.revision
	if len(revision) > revisionLen {
		revision = revision[:revisionLen]
	}
	v := info.tag
	if v == "" {
		v = pluginVersion
	}
	v += "+g" + revision
	if info.modified {
		v += "*"
	}
	return v
}

// Full is the long form, for `bermuda --version`.
func Full() string {
	info := read()
	var b strings.Builder
	b.WriteString("bermuda " + String())
	if info.revision != "" {
		b.WriteString("\nrevision  " + info.revision)
	}
	if !info.built.IsZero() {
		b.WriteString("\ncommitted " + info.built.Local().Format("2006-01-02 15:04:05 MST"))
	}
	if info.revision != "" {
		state := "clean"
		if info.modified {
			state = "modified — built from an uncommitted tree"
		}
		b.WriteString("\ntree      " + state)
	}
	if info.goVersion != "" {
		b.WriteString("\ngo        " + info.goVersion)
	}
	return b.String()
}

type buildInfo struct {
	tag       string
	revision  string
	built     time.Time
	modified  bool
	goVersion string
}

func read() buildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return buildInfo{}
	}
	return parse(bi)
}

func parse(bi *debug.BuildInfo) buildInfo {
	out := buildInfo{goVersion: bi.GoVersion}
	// A module built by `go install pkg@v1.2.3` carries a real version here;
	// a plain build can carry a pseudo-version from the module graph instead.
	v := bi.Main.Version
	if strings.HasSuffix(v, "+dirty") {
		v = strings.TrimSuffix(v, "+dirty")
		out.modified = true
	}
	if v != "" && v != "(devel)" && !pseudoVersion.MatchString(v) {
		out.tag = v
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			out.revision = s.Value
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				out.built = t
			}
		case "vcs.modified":
			out.modified = out.modified || s.Value == "true"
		}
	}
	// Versioned go installs omit VCS settings, but pseudo-versions carry the
	// source revision. Prefer a VCS revision when one is available.
	if out.revision == "" && pseudoVersion.MatchString(v) {
		out.revision = v[strings.LastIndexByte(v, '-')+1:]
	}
	return out
}
