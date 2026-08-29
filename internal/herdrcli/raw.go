package herdrcli

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// runRaw executes a herdr subcommand whose stdout is not a JSON envelope.
// "agent read" is the only such command we use: it emits terminal text.
func runRaw(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("herdr %v: %w: %s", args, err, stderr.String())
	}
	return stdout.String(), nil
}
