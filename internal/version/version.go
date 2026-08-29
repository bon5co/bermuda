// Package version reports which build of bermuda is running.
//
// The identity comes from the binary itself rather than a file someone has to
// remember to bump. Go stamps the git revision, its time, and whether the tree
// was dirty into every `go build`, which matters here because bermuda rebuilds
// itself: the board re-execs after a rebuild and scheduled jobs rebuild it too,
// so any scheme relying on a build flag would silently produce blank versions.
//
// One limit worth knowing: Go does not stamp VCS information when building from
// a git worktree, because the worktree's .git is a file rather than a
// repository. Such builds report "dev". That is the honest answer — a worktree
// build is work in progress, not the deployed artifact, which is built in the
// primary checkout after merge.
package version

import (
	"runtime/debug"
	"strings"
	"time"
)

// Tag is set at build time for a released version:
//
//	go build -ldflags "-X github.com/bon5co/bermuda/v2/internal/version.Tag=v1.2.3"
//
// When it is set it wins, because a human-chosen semver says more than a
// commit hash. When it is not, the revision is the honest answer.
var Tag string

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
	info := read()
	if info.tag != "" {
		return info.tag
	}
	if info.revision == "" {
		return "dev"
	}
	v := info.revision
	if len(v) > revisionLen {
		v = v[:revisionLen]
	}
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
	out := buildInfo{goVersion: bi.GoVersion}
	// A module built by `go install pkg@v1.2.3` carries a real version here;
	// a plain `go build` leaves it empty or "(devel)", which says nothing.
	if v := bi.Main.Version; v != "" && v != "(devel)" && !strings.HasPrefix(v, "v0.0.0-") {
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
			out.modified = s.Value == "true"
		}
	}
	return out
}
