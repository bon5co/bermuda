// Package usage reads token counts for a run out of the agent's own session
// transcript.
//
// Bermuda never sees the agent's API traffic: it drives an interactive Claude
// Code session through Herdr. The only record of what a run cost is the session
// transcript Claude Code writes per working directory, so that is what this
// package reads.
//
// Attribution is exact rather than nearest-in-time. Several agents can work in
// the same directory at once, and a persistent job reuses one session across
// many runs, so picking the newest session or the last few messages would
// charge one job for another's work. Every bermuda run submits a one-line
// prompt naming its own run directory, and that path is unique to the run; it
// is the anchor everything here keys off.
package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Usage is what one run consumed. The four counts are kept apart because they
// are billed at different rates, and the model is kept because the same counts
// cost very differently on opus and sonnet.
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Model               string
}

// Empty reports whether nothing was recorded.
func (u Usage) Empty() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheReadTokens == 0 && u.CacheCreationTokens == 0
}

// transcriptDir is where Claude Code keeps sessions for a working directory:
// the path with everything outside [a-zA-Z0-9-] replaced by '-'. Separators,
// dots and underscores all collapse to the same character, so a worktree such
// as bermuda_worktrees/feat_x is found as bermuda-worktrees-feat-x.
func transcriptDir(home, cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return filepath.Join(home, ".claude", "projects", b.String())
}

// runDirRE matches any bermuda run directory mentioned in a prompt, so a
// persistent session's next run can be recognised as a boundary.
var runDirRE = regexp.MustCompile(`/runs/([^/"\s]+)/prompt\.md`)

// Collect sums the token usage attributable to one run.
//
// It returns a zero Usage and no error when no session mentions the run: a
// missing or unreadable transcript is a bookkeeping gap, not a failed run.
func Collect(home, cwd, runDir string) (Usage, error) {
	dir := transcriptDir(home, cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Usage{}, nil
		}
		return Usage{}, err
	}
	runID := filepath.Base(runDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		u, found, err := collectFile(filepath.Join(dir, e.Name()), runID)
		if err != nil {
			return Usage{}, err
		}
		if found {
			return u, nil
		}
	}
	return Usage{}, nil
}

// entry is the part of a transcript line this package cares about.
type entry struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// collectFile sums one session's usage for a run, reporting whether the run was
// mentioned at all.
//
// Lines are in append order, so the run's own prompt opens the window and the
// next prompt naming a different run closes it. That boundary is what keeps a
// persistent job's runs from absorbing each other.
func collectFile(path, runID string) (Usage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return Usage{}, false, err
	}
	defer f.Close()

	var u Usage
	var collecting, found bool
	seen := map[string]bool{}

	sc := bufio.NewScanner(f)
	// Transcript lines carry whole tool results and can be far larger than the
	// scanner's default 64KiB ceiling; a long line must not truncate a session.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // a malformed line costs one message, not the whole run
		}
		switch e.Type {
		case "user":
			m := runDirRE.FindSubmatch(line)
			if m == nil {
				continue
			}
			if string(m[1]) == runID {
				collecting, found = true, true
			} else if collecting {
				// The session moved on to another run.
				return u, true, nil
			}
		case "assistant":
			if !collecting {
				continue
			}
			// A streamed message can appear more than once; its id makes the
			// repeat recognisable so its tokens are not counted twice.
			if id := e.Message.ID; id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			u.InputTokens += e.Message.Usage.InputTokens
			u.OutputTokens += e.Message.Usage.OutputTokens
			u.CacheReadTokens += e.Message.Usage.CacheReadInputTokens
			u.CacheCreationTokens += e.Message.Usage.CacheCreationInputTokens
			if e.Message.Model != "" {
				u.Model = e.Message.Model
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Usage{}, false, err
	}
	return u, found, nil
}

// FormatCount renders a token count compactly, so a long number cannot break a
// column layout.
func FormatCount(n int64) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1000_000:
		return trimZero(float64(n)/1000) + "k"
	default:
		return trimZero(float64(n)/1000_000) + "M"
	}
}

func trimZero(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

// Line renders a run's usage as one short line for the board and the CLI.
func Line(u Usage) string {
	return fmt.Sprintf("%s in · %s out · %s cache read · %s cache write",
		FormatCount(u.InputTokens), FormatCount(u.OutputTokens),
		FormatCount(u.CacheReadTokens), FormatCount(u.CacheCreationTokens))
}

// CollectSettled is Collect, waiting briefly for the transcript to catch up.
//
// Claude Code appends to the session file with a small lag, so a run's last
// messages are often not on disk at the moment the agent settles and bermuda
// persists the run. Without this the counts land at zero for exactly the runs
// that finish quickest. It gives up rather than delaying a run for long: a
// missing count is a bookkeeping gap, not a reason to hold up the harness.
func CollectSettled(home, cwd, runDir string, wait time.Duration) (Usage, error) {
	deadline := time.Now().Add(wait)
	for {
		u, err := Collect(home, cwd, runDir)
		if err != nil {
			return Usage{}, err
		}
		if !u.Empty() || time.Now().After(deadline) {
			return u, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}
