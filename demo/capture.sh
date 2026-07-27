#!/usr/bin/env bash
# Build a demo store and photograph the board from it.
#
# Every row in the screenshots is real: the jobs are added through the CLI, the
# runs are `run` steps that actually execute in this container, and the failing
# one actually fails. Nothing is inserted into the database by hand, because a
# screenshot of fabricated rows is a drawing, not a screenshot.
set -euo pipefail

OUT=${OUT:-/out}
mkdir -p "$OUT"
rm -rf "${BERMUDA_STATE_DIR:?}" && mkdir -p "$BERMUDA_STATE_DIR"

say() { printf '\n== %s\n' "$1"; }

say "flows"
FLOWS="${BERMUDA_STATE_DIR:-$HOME/.bermuda}/flows"
mkdir -p "$FLOWS"

cat > "$FLOWS/nightly-build.yml" <<'YAML'
about: build and test on a schedule
steps:
  - id: build
    run: go build ./...
  - id: test
    run: go test ./internal/version/ -count=1
YAML

cat > "$FLOWS/docs-sweep.yml" <<'YAML'
about: count the docs
steps:
  - id: count
    run: ls -1 /src/*.md | wc -l
YAML

cat > "$FLOWS/release-check.yml" <<'YAML'
about: the pre-release gate
input: the version being cut, e.g. v2.1.0
steps:
  - id: version
    run: bermuda --version
  - id: vet
    run: go vet ./internal/store/
  - id: record
    run: 'echo "checked $BERMUDA_INPUT: $BERMUDA_PREVIOUS"'
YAML

# A flow that takes an input and is never run here, because the screenshot has
# to show the INPUT column carrying something. Every other demo flow is
# `run:`-only and input-less — there are no API credentials in this container,
# so an agent step would park — and a FLOWS tab where that column is all dashes
# hides the one thing that makes a flow callable.
cat > "$FLOWS/triage.yml" <<'YAML'
about: triage an incoming report, then act on it
input: a report, a PR number, or a stack trace
steps:
  - id: assess
    agent: Look at {{input}} and say in one line whether it is real.
    model: opus
  - id: patch
    agent: "{{previous}} — if that says it is real, write the fix."
  - id: verify
    run: go test ./...
YAML

cat > "$FLOWS/link-audit.yml" <<'YAML'
about: find broken links
steps:
  - id: broken
    run: test -f /src/README.md && false
YAML

say "jobs"
bermuda job add --id nightly-build --name "Nightly build" \
    --flow nightly-build --cron '0 4 * * *' --model sonnet --tags ci,go --favorite
bermuda job add --id docs-sweep --name "Docs sweep" \
    --flow docs-sweep --interval 6h --model sonnet --tags docs
bermuda job add --id release-check --name "Release check" \
    --flow release-check --cron '30 9 * * 1' --model opus --tags release
bermuda job add --id link-audit --name "Link audit" \
    --flow link-audit --cron '0 12 * * *' --model sonnet --tags docs

say "runs — these execute for real"
bermuda flow run nightly-build || true
bermuda flow run docs-sweep    || true
bermuda flow run release-check --input v2.1.0 || true
bermuda flow run link-audit    || true   # fails on purpose: a parked run

say "threads"
bermuda thread new deploys --about 'what shipped, and what broke doing it'
bermuda thread event --as ada 'go 1.26.5 is the toolchain on this box now'
bermuda thread post --as scout '@ada link-audit parked on a broken step, not on the links'
bermuda thread post --as ada '@all the browser is free again, I released it'
bermuda thread post --thread deploys --as scout 'nightly-build is green on the new toolchain'
bermuda thread claim browser --ttl 20m --why 'screenshotting the board' --as ada || true

# A second agent asking for the resource that is already taken. The refusal
# names the holder and its expiry, which is the whole point of a claim, and it
# is worth a screenshot of its own.
bermuda thread claim browser --ttl 5m --why 'checking a login' --as scout || true

say "screenshots"
cd "$OUT"
vhs /src/demo/board.tape
vhs /src/demo/threads.tape
bermuda thread release browser --as ada || true

ls -l "$OUT"
