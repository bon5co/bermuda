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

say "jobs"
bermuda job add --id nightly-build --name "Nightly build" \
    --steps - --cron '0 4 * * *' --model sonnet --tags ci,go --favorite <<'JSON'
[{"id": "build", "run": "go build ./..."},
 {"id": "test",  "run": "go test ./internal/version/ -count=1"}]
JSON

bermuda job add --id docs-sweep --name "Docs sweep" \
    --steps - --interval 6h --model sonnet --tags docs <<'JSON'
[{"id": "count", "run": "ls -1 /src/*.md | wc -l"}]
JSON

bermuda job add --id release-check --name "Release check" \
    --steps - --cron '30 9 * * 1' --model opus --tags release <<'JSON'
[{"id": "version", "run": "bermuda --version"},
 {"id": "vet",     "run": "go vet ./internal/store/"}]
JSON

bermuda job add --id link-audit --name "Link audit" \
    --steps - --cron '0 12 * * *' --model sonnet --tags docs <<'JSON'
[{"id": "broken", "run": "test -f /src/README.md && false"}]
JSON

say "runs — these execute for real"
bermuda flow run nightly-build || true
bermuda flow run docs-sweep    || true
bermuda flow run release-check || true
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
