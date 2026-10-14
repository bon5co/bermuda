package board

import (
	"fmt"
	"os"
	"runtime/pprof"
)

// profileEnv names a file the board writes a CPU profile to for as long as it
// is open. It is a debugging aid for the board's own cost, the thing a pane
// left open for a day pays for: set it, leave the board idle, quit, and read
// the file with `go tool pprof`.
//
// A file rather than a listener on purpose. A profile endpoint is a port that
// stays open on every machine the board runs on; a file exists only when
// somebody asked for one.
const profileEnv = "BERMUDA_CPUPROFILE"

// startCPUProfile begins profiling when profileEnv is set, and returns what
// ends it. Unset, both are no-ops.
func startCPUProfile() (stop func(), err error) {
	path := os.Getenv(profileEnv)
	if path == "" {
		return func() {}, nil
	}
	f, err := os.Create(path) // #nosec G304 -- the path is the operator's own debug setting
	if err != nil {
		return nil, fmt.Errorf("%s: %w", profileEnv, err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", profileEnv, err)
	}
	return func() {
		pprof.StopCPUProfile()
		_ = f.Close()
	}, nil
}
