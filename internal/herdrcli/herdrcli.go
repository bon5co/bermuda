// Package herdrcli is a thin wrapper over the herdr binary.
//
// Every herdr subcommand returns a single JSON envelope on stdout of the shape
// {"id":..., "result":{...}} on success or {"id":..., "error":{...}} on
// failure, so all calls funnel through run() and decode into Envelope.
//
// We always talk to herdr through HERDR_BIN_PATH when it is set (that is what
// herdr injects into plugin commands) and fall back to "herdr" on PATH.
package herdrcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Client invokes the herdr CLI.
type Client struct {
	Bin string
}

// New returns a Client bound to HERDR_BIN_PATH, or "herdr" on PATH.
func New() *Client {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	return &Client{Bin: bin}
}

// Error is a structured error returned by herdr itself.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("herdr: %s: %s", e.Code, e.Message) }

// Code reports whether err is a herdr error with the given code.
// Callers use this to branch on "timeout" without string matching.
func Code(err error, code string) bool {
	var he *Error
	if !AsHerdrError(err, &he) {
		return false
	}
	return he.Code == code
}

// AsHerdrError unwraps err into target when it is a *Error.
//
// errors.As rather than a type assertion: this package wraps with %w, and so
// does the runner, so an assertion answered "not a herdr error" for anything
// that had passed through one wrap — which is what `Code(err, "timeout")` is
// asked about.
func AsHerdrError(err error, target **Error) bool {
	if err == nil {
		return false
	}
	return errors.As(err, target)
}

type envelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// run executes a herdr subcommand and decodes result into out (may be nil).
func (c *Client) run(ctx context.Context, out any, args ...string) error {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// herdr reports its own failures as a JSON error envelope with a nonzero
	// exit status, and writes that envelope to stderr rather than stdout, so
	// try both streams before treating runErr as fatal.
	payload := stdout.Bytes()
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = stderr.Bytes()
	}
	var env envelope
	if jsonErr := json.Unmarshal(payload, &env); jsonErr != nil {
		if runErr != nil {
			return fmt.Errorf("herdr %v: %w: %s", args, runErr, stderr.String())
		}
		return fmt.Errorf("herdr %v: decode: %w", args, jsonErr)
	}
	if env.Error != nil {
		return env.Error
	}
	// A well-formed envelope with no error in it is not proof the command
	// worked: herdr can exit nonzero having printed one, and taking the
	// envelope's word for it turned those into silent successes.
	if runErr != nil {
		return fmt.Errorf("herdr %v: %w: %s", args, runErr, strings.TrimSpace(stderr.String()))
	}
	if out != nil {
		if len(env.Result) == 0 {
			// Distinct from malformed JSON, which is what "decode result:
			// unexpected end of JSON input" used to blame for this.
			return fmt.Errorf("herdr %v: no result in reply: %s",
				args, strings.TrimSpace(string(payload)))
		}
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("herdr %v: decode result: %w", args, err)
		}
	}
	return nil
}
