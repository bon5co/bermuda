package board

import (
	"os"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// selfRestart replaces this process with the current binary on disk.
//
// A board left open in a pane keeps whatever binary it started with, so after
// a rebuild it silently renders stale data with old code — the worst kind of
// wrong, because the data underneath is live. Re-exec keeps an open pane
// honest without anyone having to remember to restart it.
type binaryWatcher struct {
	path    string
	modTime time.Time
	size    int64
}

func newBinaryWatcher() *binaryWatcher {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return nil
	}
	return &binaryWatcher{path: exe, modTime: fi.ModTime(), size: fi.Size()}
}

// changed reports whether the binary on disk differs from the running one.
// Size is compared alongside mtime because a rebuild can land in the same
// second as the previous one.
func (w *binaryWatcher) changed() bool {
	if w == nil {
		return false
	}
	fi, err := os.Stat(w.path)
	if err != nil {
		// A missing binary means a build is mid-write; wait for the next tick.
		return false
	}
	return !fi.ModTime().Equal(w.modTime) || fi.Size() != w.size
}

// restart execs the new binary in place, preserving arguments and environment.
// It only returns if the exec fails.
func (w *binaryWatcher) restart() error {
	return syscall.Exec(w.path, os.Args, os.Environ())
}

// checkReload is the periodic binary check, emitted as a bubbletea command.
func (m *Model) checkReload() tea.Cmd {
	if m.watcher == nil || !m.watcher.changed() {
		return nil
	}
	return func() tea.Msg { return reloadMsg{} }
}

type reloadMsg struct{}

// checkDaemon refreshes the scheduler's liveness for the header, and revives
// it if it died. A board showing schedules while nothing can fire them is
// worse than no board.
func (m *Model) checkDaemon() tea.Cmd {
	if m.deps.DaemonRunning == nil {
		return nil
	}
	return func() tea.Msg {
		up := m.deps.DaemonRunning()
		if !up && m.deps.EnsureDaemon != nil {
			if err := m.deps.EnsureDaemon(); err == nil {
				up = m.deps.DaemonRunning()
			}
		}
		return daemonMsg(up)
	}
}

type daemonMsg bool
