package index

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bon5co/bermuda/v2/internal/statefs"
)

//go:embed helper.py
var helperSource []byte

// ErrNoPython is returned when nothing on this machine can run the helper.
//
// It is a named error because it is not a failure: an index nobody has set up
// is the default state of a fresh install, and every command has to be able to
// say "not configured, here is how" instead of "error".
var ErrNoPython = errors.New("no python with chromadb")

// chromaSpec is what uv installs into its throwaway environment.
//
// Pinned to a major version, not a release: Chroma's storage format has
// survived point releases and its API has not survived majors. $BERMUDA_CHROMA
// overrides it for anyone who needs a specific build.
const chromaSpec = "chromadb>=1.0,<2"

// Timeouts. Every call to another process gets a ceiling, because a helper
// that hangs on a model download must not hang the daemon that called it.
const (
	// queryTimeout is generous by search standards because the first call
	// after a reboot pays for loading the embedding model.
	queryTimeout = 3 * time.Minute
	// writeTimeout covers a full reindex of a large vault, including the
	// one-time model download on a machine that has never embedded anything.
	writeTimeout = 30 * time.Minute
)

// python is a resolved way to run the helper.
type python struct {
	// argv is the command up to but not including the script path.
	argv []string
	// how names what was found, for `--status` and for error messages.
	how string
}

// findPython resolves an interpreter that can import chromadb.
//
// Three ways, in order of how much the caller asked for: an explicit
// interpreter, uv (which builds the environment on demand and caches it), or a
// python3 that already has chromadb. The order matters — uv before python3 —
// because a system python3 that happens to import chromadb is the least
// reproducible of the three and should not win by accident.
func findPython(ctx context.Context) (python, error) {
	if p := os.Getenv("BERMUDA_INDEX_PYTHON"); p != "" {
		return python{argv: []string{p}, how: "$BERMUDA_INDEX_PYTHON (" + p + ")"}, nil
	}
	spec := os.Getenv("BERMUDA_CHROMA")
	if spec == "" {
		spec = chromaSpec
	}
	if uv, err := exec.LookPath("uv"); err == nil {
		return python{
			// onnxruntime and tokenizers are named explicitly because they
			// are what Chroma's default, local embedding function needs, and
			// a resolver that leaves them out produces an install that only
			// fails once there is something to embed.
			argv: []string{uv, "run", "--no-project", "--quiet",
				"--with", spec, "--with", "onnxruntime", "--with", "tokenizers",
				"python"},
			how: "uv (" + spec + ")",
		}, nil
	}
	if py, err := exec.LookPath("python3"); err == nil && importsChroma(ctx, py) {
		return python{argv: []string{py}, how: "python3 (" + py + ")"}, nil
	}
	return python{}, ErrNoPython
}

// importsChroma checks a candidate interpreter without installing anything.
func importsChroma(ctx context.Context, py string) bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, py, "-c", "import chromadb").Run() == nil
}

// helperPath writes the embedded script into the index directory and returns
// where it landed.
//
// It is rewritten whenever it differs from what this build embeds, so an
// upgraded bermuda never drives an older helper — a mismatch that would show
// up as a wrong answer rather than as an error.
func helperPath(dir string) (string, error) {
	if err := os.MkdirAll(dir, statefs.Dir); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "chroma_helper.py")
	if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, helperSource) {
		return p, nil
	}
	return p, os.WriteFile(p, helperSource, statefs.File)
}

// call runs one helper request and decodes its response.
//
// The response comes back through a file the request names, not through
// stdout: Chroma, onnxruntime and a model downloader all write to stdout
// whenever they feel like it, and a progress bar interleaved with a JSON
// document is not something a parser can be made robust against.
func (c *Client) call(ctx context.Context, timeout time.Duration, req map[string]any) (map[string]any, error) {
	py, err := findPython(ctx)
	if err != nil {
		return nil, err
	}
	script, err := helperPath(c.dir)
	if err != nil {
		return nil, err
	}
	out, err := os.CreateTemp(c.dir, "response-*.json")
	if err != nil {
		return nil, err
	}
	out.Close()
	defer os.Remove(out.Name())

	req["out"] = out.Name()
	req["dir"] = filepath.Join(c.dir, "chroma")
	req["collection"] = c.collection
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append(append([]string{}, py.argv...), script)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = nil
	runErr := cmd.Run()

	raw, readErr := os.ReadFile(out.Name())
	if readErr != nil || len(raw) == 0 {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("chroma helper timed out after %s", timeout)
		}
		return nil, fmt.Errorf("chroma helper wrote no response (%v): %s",
			runErr, lastLines(stderr.String(), 5))
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("chroma helper wrote an unreadable response: %w", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		msg, _ := resp["error"].(string)
		if msg == "" {
			msg = "unknown error"
		}
		return nil, errors.New(msg)
	}
	return resp, nil
}

// lastLines keeps an error message to the part of stderr that says something.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
