#!/usr/bin/env bash
# Install bermuda the way the README says to, then use it.
#
# Every check is a thing the README or the docs promise. A promise that cannot
# be checked from outside the repository is not in here.
set -uo pipefail

REPO=${REPO:-bon5co/bermuda}
PLUGIN_ID=bon5co.bermuda
pass=0
fail=0

ok()   { printf '  \033[32mok\033[0m   %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '       %s\n' "$2"; fail=$((fail + 1)); }
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# plugin_root finds the directory herdr keeps the plugin in.
#
# `herdr plugin list` prints `[github:owner/repo@sha]` for an installed plugin
# and `[local:/path]` for a linked one, so only the linked form carries a path.
# The managed checkout lives under the plugin data directory, named for the
# plugin id and a hash of its source.
plugin_root() {
    local linked
    linked=$(herdr plugin list 2>/dev/null | sed -n 's/.*\[local:\([^]]*\)\].*/\1/p' | head -1)
    if [ -n "$linked" ]; then
        printf '%s\n' "$linked"
        return
    fi
    ls -d "$HOME/.config/herdr/plugins/github/${PLUGIN_ID}-"* 2>/dev/null | head -1
}

# check <name> <expected-substring> <command...>
check() {
    local name=$1 want=$2; shift 2
    local out status
    out=$("$@" 2>&1); status=$?
    if [ -n "$want" ] && ! grep -qF -- "$want" <<<"$out"; then
        bad "$name" "wanted \"$want\", got: $(head -3 <<<"$out" | tr '\n' ' ')"
        return 1
    fi
    if [ -z "$want" ] && [ $status -ne 0 ]; then
        bad "$name" "exit $status: $(head -3 <<<"$out" | tr '\n' ' ')"
        return 1
    fi
    ok "$name"
}

step "install from GitHub, as a stranger would"
if [ -n "${GH_TOKEN:-}" ]; then
    # Private repository: stand in for the anonymous clone a public one gets.
    git config --global url."https://${GH_TOKEN}@github.com/".insteadOf "https://github.com/"
    echo "  (using GH_TOKEN — the repository is not public yet)"
fi

if ! out=$(herdr plugin install "$REPO" --yes 2>&1); then
    bad "herdr plugin install $REPO" "$(tail -3 <<<"$out")"
    echo; echo "install failed, nothing else can run"; exit 1
fi
ok "herdr plugin install $REPO"

check "plugin is registered and enabled" "$PLUGIN_ID" herdr plugin list

ROOT=$(plugin_root)
[ -d "$ROOT" ] && ok "plugin root exists: $ROOT" || bad "plugin root" "not found in plugin list output"
BIN="$ROOT/bin/bermuda"
[ -x "$BIN" ] && ok "build command produced $BIN" || bad "the manifest build did not produce a binary"
export PATH="$ROOT/bin:$PATH"

# The manifest's own contents, since asking herdr what it registered needs a
# running server and this container has none.
MANIFEST="$ROOT/herdr-plugin.toml"
grep -q 'id = "board"' "$MANIFEST" 2>/dev/null && ok "manifest declares the board pane" || bad "board pane missing from the manifest"
grep -q 'id = "run-now"' "$MANIFEST" 2>/dev/null && ok "manifest declares its actions" || bad "actions missing from the manifest"
# Every subcommand the manifest invokes has to exist. Two of them did not, once,
# and each failed only on a user's machine. `unknown command` is what main prints
# for a name it does not dispatch — any other outcome means the command is real,
# whatever it then goes on to complain about.
for cmd in $(grep -o '"\./bin/bermuda", "[a-z-]*"' "$MANIFEST" 2>/dev/null | sed 's/.*, "//;s/"//' | sort -u); do
    if "$BIN" "$cmd" --help 2>&1 | grep -q "unknown command"; then
        bad "manifest invokes '$cmd', which the binary does not have"
    else
        ok "manifest command '$cmd' exists in the binary"
    fi
done

step "the binary the manifest built"
check "bermuda --version reports a build"  "revision" bermuda --version
check "usage lists the documented commands" "bermuda stop" bermuda --help

step "go install, the README's other way in"
# The only path here that asks the module proxy anything. Everything else builds
# from a checkout, which is why `module github.com/bon5co/bermuda` survived five
# v2 releases: `go install …@latest` could not see any of the v2 tags, quietly
# resolved the newest v1 one, and installed a bermuda from before flows and
# threads existed — reporting itself as v1.1.1 and complaining about nothing.
MODULE=$(sed -n 's/^module //p' "$ROOT/go.mod" | head -1)
MAJOR=$(sed -n 's/^version = "\([0-9]*\)\..*/\1/p' "$MANIFEST" | head -1)
if [ "${MAJOR:-0}" -ge 2 ] 2>/dev/null; then
    case "$MODULE" in
        */v"$MAJOR") ok "the module path carries the released major ($MODULE)" ;;
        *) bad "go.mod says $MODULE, but this is a v$MAJOR release" \
               "go install would resolve the newest v1 tag and say nothing" ;;
    esac
fi
out=$(GOBIN=/tmp/gobin go install "$MODULE/cmd/bermuda@latest" 2>&1); status=$?
if [ $status -ne 0 ] && grep -q "no matching versions" <<<"$out"; then
    # The first release at a new major has no published tag under the new path
    # until it is tagged. Not a failure, but never silent either.
    echo "  --   $MODULE has no published tag yet, so go install @latest has nothing to fetch"
elif [ $status -ne 0 ]; then
    bad "go install $MODULE/cmd/bermuda@latest" "$(tail -3 <<<"$out")"
else
    ok "go install $MODULE/cmd/bermuda@latest"
    got=$(/tmp/gobin/bermuda --version 2>&1 | head -1)
    case "$got" in
        *"v$MAJOR."*) ok "the installed CLI is a v$MAJOR build ($got)" ;;
        *) bad "go install produced \"$got\", which is not a v$MAJOR build" ;;
    esac
fi

step "the skill ships with it"
SKILL="$ROOT/skills/bermuda/SKILL.md"
[ -f "$SKILL" ] && ok "skills/bermuda/SKILL.md is present" || bad "the published skill is missing"
head -1 "$SKILL" 2>/dev/null | grep -q -- --- && ok "skill has frontmatter" || bad "skill frontmatter missing"
grep -q "^name: bermuda" "$SKILL" 2>/dev/null && ok "skill name matches its directory" || bad "skill name does not match"
[ -L "$ROOT/.claude/skills/bermuda" ] && ok ".claude/skills symlink survives the clone" || bad ".claude/skills symlink missing"

step "jobs and flows really run"
# `flow new` has to produce something that parses. It is the first flow anybody
# sees, and a broken template turns "write a flow" into "debug bermuda".
bermuda flow new scratch --about 'the shipped template' >/dev/null 2>&1
check "flow new writes a template" "scratch" bermuda flow list

FLOWS="${BERMUDA_STATE_DIR:-$HOME/.bermuda}/flows"
mkdir -p "$FLOWS"
cat > "$FLOWS/greenfield.yml" <<'YAML'
about: prove the chain
input: the thing to act on
steps:
  - id: one
    run: 'echo "one saw [$BERMUDA_INPUT]"'
  - id: two
    run: 'echo "two saw [$BERMUDA_PREVIOUS]"'
YAML
out=$(bermuda flow run greenfield --input xyzzy 2>&1)
grep -q '"outcome": "done"' <<<"$out" && ok "flow run completes" || bad "flow run did not finish" "$out"
# A flow run opens a space of its own so its steps share a thread. There is no
# herdr server in this container, which is the case that has to degrade rather
# than refuse: the thread is how steps compare notes, not what makes them run in
# order, and a flow that would not start without a window is a worse failure than
# one whose steps cannot talk.
grep -q "share no thread" <<<"$out" && ok "with no herdr the flow says so and runs anyway" \
    || bad "a flow with no space did not say so" "$out"
check "run is recorded"          "greenfield" bermuda run list

# latest_run picks the newest run of one flow.
#
# By job, never by row position: `run list` prints oldest-first, so taking the
# first data row silently inspected an *earlier* flow's run and then asserted
# against its steps. That is a check that passes for the wrong reason, which is
# worse than one that fails.
latest_run() { bermuda run list 2>/dev/null | awk -v j="$1" '$2==j{id=$1} END{print id}'; }

# The feature itself: the caller's x reaches the first step, and the first
# step's published result reaches the second. A flow whose steps cannot see
# each other is just two jobs.
run_id=$(latest_run greenfield)
out=$(bermuda flow status "$run_id" 2>&1)
grep -q "one saw \[xyzzy\]" <<<"$out" && ok "the input reaches the first step" || bad "input did not reach step one" "$out"
grep -q "two saw \[one saw \[xyzzy\]\]" <<<"$out" && ok "a step's result reaches the next" || bad "the chain did not carry" "$out"

# A flow that declares an input must not run with a blank one: every {{input}}
# would become a hole an agent then invents something to fill.
out=$(bermuda flow run greenfield 2>&1); status=$?
[ $status -ne 0 ] && ok "a flow that needs an input refuses a blank one" || bad "a flow ran with no input" "$out"

cat > "$FLOWS/breaks.yml" <<'YAML'
steps:
  - id: boom
    run: exit 3
  - id: never
    run: echo should not run
YAML
out=$(bermuda flow run breaks 2>&1); status=$?
grep -q "parked" <<<"$out" && ok "a failing step parks the run" || bad "failing step did not park" "$out"
[ $status -ne 0 ] && ok "a parked flow exits nonzero" || bad "parked flow exited 0"
run_id=$(latest_run breaks)
out=$(bermuda flow status "$run_id" 2>&1)
grep -q "never .*pending" <<<"$out" && ok "the step after a failure never starts" || bad "a step ran behind a failed one" "$out"

# A checker that hands the work back. The maker only gets it right on its
# second run, which is the shape the feature exists for: parking here would be
# correct and useless, because the step that can fix it is the one above.
cat > "$FLOWS/heals.yml" <<'YAML'
steps:
  - id: implement
    run: 'if [ -f "$HOME/heal-tried" ]; then touch "$HOME/heal-fixed"; else touch "$HOME/heal-tried"; fi'
  - id: verify
    run: 'test -f "$HOME/heal-fixed"'
    on_fail:
      goto: implement
      max_loops: 2
YAML
out=$(bermuda flow run heals 2>&1); status=$?
grep -q '"outcome": "done"' <<<"$out" && ok "a flow heals itself and finishes" || bad "the loopback did not heal" "$out"
[ $status -eq 0 ] && ok "a healed flow exits zero" || bad "a healed flow exited nonzero"
run_id=$(latest_run heals)
out=$(bermuda flow status "$run_id" 2>&1)
grep -q "attempt 2" <<<"$out" && ok "the retried step says which attempt it is on" \
    || bad "a healed run looks like one that worked first time" "$out"
grep -q "2/2 steps" <<<"$out" && ok "a step run twice is still counted once" || bad "the retry was counted as extra work" "$out"

# The two ways a loop stops on its own. Both are parks: the attempts are on
# record and a human can resume, rather than an unattended run rewriting the
# same code until somebody reads the token bill.
cat > "$FLOWS/stuck.yml" <<'YAML'
steps:
  - id: implement
    run: echo nothing changes
  - id: verify
    run: exit 1
    on_fail:
      goto: implement
      max_loops: 5
YAML
bermuda flow run stuck >/dev/null 2>&1
out=$(bermuda flow status "$(latest_run stuck)" 2>&1)
grep -q "loop_stuck" <<<"$out" && ok "an unchanged verdict parks instead of looping again" \
    || bad "a loop that changed nothing kept going" "$out"

cat > "$FLOWS/exhausts.yml" <<'YAML'
steps:
  - id: implement
    run: echo trying again
  - id: verify
    run: 'date +%s%N; exit 1'
    on_fail:
      goto: implement
      max_loops: 2
YAML
bermuda flow run exhausts >/dev/null 2>&1
out=$(bermuda flow status "$(latest_run exhausts)" 2>&1)
grep -q "loop_exhausted" <<<"$out" && ok "a loop that runs out of attempts parks" \
    || bad "a bounded loop did not stop where it said" "$out"
grep -q "resume with" <<<"$out" && ok "a parked loop says how to resume it" || bad "no resume line on a parked loop" "$out"

# An edge pointing forward is a branch, and a flow is a series. Refused when the
# file is read, so the flow that would loop into itself never starts.
cat > "$FLOWS/branchy.yml" <<'YAML'
steps:
  - id: verify
    run: 'exit 1'
    on_fail:
      goto: ship
  - id: ship
    run: echo shipped
YAML
out=$(bermuda flow run branchy 2>&1); status=$?
[ $status -ne 0 ] && ok "an on_fail pointing forward is refused" || bad "a forward edge ran" "$out"

# A job starts a flow on a schedule; the job supplies the x.
bermuda job add --id breaks-job --name "Breaks" --flow breaks --input none >/dev/null 2>&1
check "a job can start a flow"   "breaks"    bermuda job list
out=$(bermuda job run breaks-job 2>&1); status=$?
[ $status -ne 0 ] && ok "job run exits nonzero on failure" || bad "job run exited 0 on a failed run"

# Removing a flow a job depends on fails silently at 04:00 otherwise.
out=$(bermuda flow rm breaks 2>&1); status=$?
[ $status -ne 0 ] && ok "a flow in use cannot be removed" || bad "removed a flow a job needs" "$out"

step "threads, claims and identity"
check "thread post"              ""          bermuda thread post --as ada 'first light'
check "thread event"             ""          bermuda thread event --as ada 'toolchain replaced'
check "log reads it back"        "first light" bermuda thread log --limit 5
check "whoami reports name+pid"  "ada#"      bermuda thread whoami --as ada
check "claim is taken"           "claimed"   bermuda thread claim browser --ttl 5m --why e2e --as ada
check "status shows the holder"  "browser"   bermuda thread status
out=$(bermuda thread claim browser --ttl 5m --why 'second agent' --as scout 2>&1)
grep -q "held by ada" <<<"$out" && ok "a second agent is refused, and told who holds it" || bad "claim refusal" "$out"
check "release"                  ""          bermuda thread release browser --as ada
check "unnamed writes are refused" "cannot tell who you are" bermuda thread post 'nobody'

step "the scheduler, and its off switch"
check "start brings the pair up" "running"   bermuda start
sleep 1
pgrep -f "bermuda daemon" >/dev/null && ok "daemon is alive" || bad "no daemon after start"
pgrep -f "bermuda sentinel" >/dev/null && ok "sentinel is alive" || bad "no sentinel after start"
check "stop reports stopping"    "stop"      bermuda stop
sleep 1
pgrep -f "bermuda daemon" >/dev/null && bad "daemon survived stop" || ok "daemon stopped"
bermuda ensure >/dev/null 2>&1
sleep 1
pgrep -f "bermuda daemon" >/dev/null && bad "ensure revived a stopped scheduler" || ok "stop survives the plugin startup hook"
check "start again"              "running"   bermuda start
bermuda stop >/dev/null 2>&1

step "the board, with no terminal to draw in"
out=$(bermuda board 2>&1); status=$?
grep -qi "no TTY" <<<"$out" && ok "board explains it needs a terminal" || bad "board error is unhelpful" "$out"
[ $status -ne 0 ] && ok "board exits nonzero with nowhere to draw" || bad "board exited 0 having drawn nothing"

step "state lives where the README says"
[ -f "$BERMUDA_STATE_DIR/bermuda.db" ] && ok "store is in \$BERMUDA_STATE_DIR" || bad "no database in $BERMUDA_STATE_DIR"
[ -f "$BERMUDA_STATE_DIR/bermuda.db-wal" ] && ok "WAL file exists (a backup must include it)" || echo "  --   no -wal right now, which is fine when nothing is open"

step "uninstall leaves the store alone"
check "herdr plugin uninstall" "" herdr plugin uninstall "$REPO"
herdr plugin list 2>&1 | grep -q "$PLUGIN_ID" && bad "plugin still registered after uninstall" || ok "plugin is gone"
[ -f "$BERMUDA_STATE_DIR/bermuda.db" ] && ok "the store survived the uninstall" || bad "uninstall deleted the store"

printf '\n\033[1m%d passed, %d failed\033[0m\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
