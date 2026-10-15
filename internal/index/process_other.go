//go:build !unix && !windows

package index

import "os/exec"

// Other platforms retain direct-process cancellation and bounded pipe waits.
func configureHelperProcess(cmd *exec.Cmd) func() {
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}
