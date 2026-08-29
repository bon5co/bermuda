package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeHelper stands in for Python: it reads the request bermuda writes, logs
// it, and answers with whatever the test wants. It exists so the wiring --
// argv, stdin, the response file, the error path -- is tested on a machine
// with no chromadb, which is every CI runner and was this one.
func fakeHelper(t *testing.T, response string) (script, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake helper is a shell script")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "requests.log")
	script = filepath.Join(dir, "fakepy")
	body := `#!/bin/sh
req=$(cat)
printf '%s\n' "$req" >> "` + log + `"
out=$(printf '%s' "$req" | sed -n 's/.*"out":"\([^"]*\)".*/\1/p')
printf '%s' '` + response + `' > "$out"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERMUDA_INDEX_PYTHON", script)
	return script, log
}

func requests(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestFindPythonPrefersTheExplicitInterpreter(t *testing.T) {
	t.Setenv("BERMUDA_INDEX_PYTHON", "/opt/python/bin/python")
	py, err := findPython(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(py.argv) != 1 || py.argv[0] != "/opt/python/bin/python" {
		t.Errorf("argv = %v", py.argv)
	}
}

// uv builds the environment on demand and caches it, so it beats a system
// python that happens to import chromadb -- the least reproducible of the
// three and not something that should win by accident.
func TestFindPythonUsesUvWithTheEmbeddingDependenciesNamed(t *testing.T) {
	t.Setenv("BERMUDA_INDEX_PYTHON", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uv"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	py, err := findPython(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(py.argv, " ")
	for _, want := range []string{"run", "--no-project", "chromadb", "onnxruntime", "tokenizers", "python"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q is missing %q", argv, want)
		}
	}
}

// Nothing to run the helper with is the default state of a fresh install, not
// a failure -- so it comes back as a named error the commands can turn into
// instructions.
func TestFindPythonSaysWhenNothingCanRunTheHelper(t *testing.T) {
	t.Setenv("BERMUDA_INDEX_PYTHON", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := findPython(context.Background()); !errors.Is(err, ErrNoPython) {
		t.Errorf("err = %v, want ErrNoPython", err)
	}
}

// An upgraded bermuda driving an older helper would show up as a wrong answer
// rather than an error, so the script on disk is replaced whenever it differs
// from what this build embeds.
func TestHelperPathRewritesAStaleScript(t *testing.T) {
	dir := t.TempDir()
	p, err := helperPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("# an older helper\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := helperPath(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(helperSource) {
		t.Error("the stale helper was left in place")
	}
}

// A helper that dies without writing a response must not be read as an empty
// result: "nothing matched" and "the search never ran" are different answers.
func TestCallReportsAHelperThatWroteNoResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake helper is a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fakepy")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\necho 'ImportError: no chromadb' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERMUDA_INDEX_PYTHON", script)
	c := New(filepath.Join(t.TempDir(), "index"), t.TempDir())
	_, err := c.call(context.Background(), queryTimeout, map[string]any{"op": "stats"})
	if err == nil {
		t.Fatal("a dead helper reported success")
	}
	if !strings.Contains(err.Error(), "ImportError") {
		t.Errorf("err = %v, want the helper's own last words", err)
	}
}

// The helper reports its own failures in the response rather than raising, so
// the caller has to read ok:false as an error.
func TestCallSurfacesTheHelpersOwnError(t *testing.T) {
	fakeHelper(t, `{"ok":false,"error":"ValueError: collection is corrupt"}`)
	c := New(filepath.Join(t.TempDir(), "index"), t.TempDir())
	_, err := c.call(context.Background(), queryTimeout, map[string]any{"op": "stats"})
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("err = %v", err)
	}
}

// Chroma, onnxruntime and the model downloader all write to stdout whenever
// they feel like it, which is why the response travels in a file.
func TestCallIgnoresWhateverTheHelperPrints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake helper is a shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fakepy")
	body := `#!/bin/sh
req=$(cat)
echo "downloading model: 42%"
out=$(printf '%s' "$req" | sed -n 's/.*"out":"\([^"]*\)".*/\1/p')
printf '{"ok":true,"count":7}' > "$out"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERMUDA_INDEX_PYTHON", script)
	c := New(filepath.Join(t.TempDir(), "index"), t.TempDir())
	resp, err := c.call(context.Background(), queryTimeout, map[string]any{"op": "stats"})
	if err != nil {
		t.Fatal(err)
	}
	if resp["count"].(float64) != 7 {
		t.Errorf("resp = %v", resp)
	}
}
