package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Release checks are exercised with fake external tools in an isolated source
// directory. These tests cannot build, tag, fetch, or publish a real release.
func releaseFixture(t *testing.T) (string, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release procedure uses Bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash is unavailable")
	}
	root := t.TempDir()
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("scripts/release.sh", string(source))
	write("herdr-plugin.toml", "version = \"1.2.3\"\n")
	write("fixture.go", "package fixture\n")
	write("bin/gofmt", "#!/usr/bin/env bash\nprintf 'gofmt\\n' >> \"$RELEASE_TEST_LOG\"\n")
	write("bin/go", `#!/usr/bin/env bash
set -euo pipefail
printf 'go %s\n' "$*" >> "$RELEASE_TEST_LOG"
case "$1" in
  env)
    printf '%s/cache-%s\n' "$RELEASE_TEST_HOME" "$2"
    ;;
  test)
    [[ "$HOME" != "$RELEASE_TEST_HOME" && "$HOME" = "$RELEASE_TEST_HOME"/.bermuda-release.*/home ]]
    [[ -z ${BERMUDA_STATE_DIR+x} && "$HERDR_BIN_PATH" = "$RELEASE_TEST_HOME"/.bermuda-release.*/no-herdr ]]
    [[ ! -e "$HERDR_BIN_PATH" ]]
    [[ "$GOPATH" = "$RELEASE_TEST_HOME/cache-GOPATH" && "$GOCACHE" = "$RELEASE_TEST_HOME/cache-GOCACHE" ]]
    ;;
  build)
    if [[ ${2:-} = -ldflags ]]; then
      tag=${3##*=}
      [[ "$4" = -o ]]
      printf '#!/usr/bin/env bash\nprintf "bermuda %s\\n"\n' "$tag" > "$5"
      chmod +x "$5"
    fi
    ;;
  vet) ;;
  *) exit 90 ;;
esac
`)
	write("bin/git", `#!/usr/bin/env bash
set -euo pipefail
printf 'git %s\n' "$*" >> "$RELEASE_TEST_LOG"
[[ ${RELEASE_TEST_FORBID_GIT:-} != 1 ]] || exit 91
case "$*" in
  'rev-parse --show-toplevel') printf '%s\n' "$RELEASE_TEST_ROOT" ;;
  'rev-parse HEAD'|'rev-parse origin/main') printf '0123456789abcdef\n' ;;
  'status --porcelain'|'fetch origin main') ;;
  'show-ref --verify --quiet refs/tags/v1.2.3') exit 1 ;;
  'ls-remote --tags origin refs/tags/v1.2.3 refs/tags/v1.2.3^{}') ;;
  *) exit 92 ;;
esac
`)
	write("bin/gh", "#!/usr/bin/env bash\nprintf 'gh %s\\n' \"$*\" >> \"$RELEASE_TEST_LOG\"\nexit 93\n")
	write("runner with spaces", `#!/usr/bin/env bash
set -euo pipefail
printf 'runner %s\n' "$*" >> "$RELEASE_TEST_LOG"
[[ ${RELEASE_TEST_RUNNER_FAIL:-} != 1 ]] || exit 42
[[ "$1" = -- ]]
shift
exec "$@"
`)
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return root, home, filepath.Join(root, "calls.log")
}

func runReleaseFixture(t *testing.T, root, home, log string, extra []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"scripts/release.sh"}, args...)...)
	cmd.Dir = root
	// Filter inherited settings so a developer's runner and Bermuda state can
	// never leak into the fixture. The test intentionally supplies stale state.
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if key != "HOME" && key != "PATH" && key != "BERMUDA_STATE_DIR" && key != "BERMUDA_RELEASE_CHECK_RUNNER" {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RELEASE_TEST_HOME="+home, "RELEASE_TEST_ROOT="+root, "RELEASE_TEST_LOG="+log, "BERMUDA_STATE_DIR="+filepath.Join(root, "stale-state"))
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestReleaseGoChecksNeedNoGitAndNeverPublish(t *testing.T) {
	root, home, log := releaseFixture(t)
	out, err := runReleaseFixture(t, root, home, log, []string{"RELEASE_TEST_FORBID_GIT=1"}, "--go-checks", "v1.2.3")
	if err != nil {
		t.Fatalf("check helper: %v\n%s", err, out)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gofmt", "go build ./...", "go vet ./...", "go test ./...", "internal/version.Tag=v1.2.3"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("missing %q in calls:\n%s", want, calls)
		}
	}
	if strings.Contains(string(calls), "git ") || strings.Contains(string(calls), "gh ") {
		t.Fatalf("check helper reached publication tools:\n%s", calls)
	}
	leftovers, err := filepath.Glob(filepath.Join(home, ".bermuda-release.*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("scratch not cleaned: %v, %v", leftovers, err)
	}
}

func TestReleaseExternalRunnerReceivesCheckHelper(t *testing.T) {
	root, home, log := releaseFixture(t)
	out, err := runReleaseFixture(t, root, home, log, []string{"BERMUDA_RELEASE_CHECK_RUNNER=" + filepath.Join(root, "runner with spaces")}, "--check-only", "v1.2.3")
	if err != nil {
		t.Fatalf("runner: %v\n%s", err, out)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "runner -- scripts/release.sh --go-checks v1.2.3") {
		t.Fatalf("runner received wrong arguments:\n%s", calls)
	}
	if strings.Contains(string(calls), "git tag ") || strings.Contains(string(calls), "git push ") || strings.Contains(string(calls), "gh ") {
		t.Fatalf("check-only reached publication:\n%s", calls)
	}
}

func TestReleaseRunnerFailureStopsPublication(t *testing.T) {
	root, home, log := releaseFixture(t)
	notes := filepath.Join(root, "notes.md")
	if err := os.WriteFile(notes, []byte("Release notes."), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runReleaseFixture(t, root, home, log, []string{"BERMUDA_RELEASE_CHECK_RUNNER=" + filepath.Join(root, "runner with spaces"), "RELEASE_TEST_RUNNER_FAIL=1"}, "--publish", "v1.2.3", notes)
	if err == nil {
		t.Fatalf("failed runner accepted:\n%s", out)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "git tag ") || strings.Contains(string(calls), "git push ") || strings.Contains(string(calls), "gh ") || strings.Contains(string(calls), "go ") {
		t.Fatalf("failed runner reached checks or publication:\n%s", calls)
	}
}
