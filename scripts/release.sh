#!/usr/bin/env bash
# Validate a clean main commit, then optionally publish its source-only release.
set -euo pipefail
usage() {
  cat <<'EOF'
Usage: scripts/release.sh [--check-only|--draft|--publish] vX.Y.Z [notes-file] [title]

The default is --check-only. Draft/publish require a notes file and a clean
checkout at origin/main. Existing tags must identify the same validated commit;
this script never moves tags. No binary assets are uploaded.
EOF
}
fail() { printf 'release: %s\n' "$*" >&2; exit 1; }
mode=--check-only
case "${1:-}" in
  --help|-h) usage; exit 0 ;;
  --check-only|--draft|--publish) mode=$1; shift ;;
  --*) usage >&2; exit 2 ;;
esac
[[ $# -ge 1 && $# -le 3 ]] || { usage >&2; exit 2; }
tag=$1
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "expected tag vX.Y.Z"
notes=${2:-}
title=${3:-$tag}
if [[ $mode != --check-only ]]; then
  [[ -n $notes && -s $notes ]] || fail "a nonempty notes file is required"
  [[ $notes = /* ]] || notes="$PWD/$notes"
fi
cd "$(git rev-parse --show-toplevel)"
[[ -z $(git status --porcelain) ]] || fail "commit or remove local changes first"
git fetch origin main
head=$(git rev-parse HEAD)
[[ $head = "$(git rev-parse origin/main)" ]] || fail "HEAD must equal freshly fetched origin/main"
manifest=$(sed -n 's/^version = "\([^"]*\)"$/\1/p' herdr-plugin.toml)
[[ $manifest = "${tag#v}" ]] || fail "manifest version $manifest does not match $tag"
check_tags() {
  if git show-ref --verify --quiet "refs/tags/$tag"; then
    [[ $(git rev-parse "$tag^{commit}") = "$head" ]] || fail "local $tag identifies another commit"
  fi
  remote_tags=$(git ls-remote --tags origin "refs/tags/$tag" "refs/tags/$tag^{}")
  remote_head=$(printf '%s\n' "$remote_tags" | awk '$2 ~ /\^\{\}$/ {peeled=$1} NF {value=$1} END {print peeled ? peeled : value}')
  [[ -z $remote_head || $remote_head = "$head" ]] || fail "remote $tag identifies another commit"
}
check_tags
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
# Keep tests off real Herdr. Give each test its own HOME-based state instead
# of one shared BERMUDA_STATE_DIR, which overrides tests that isolate HOME.
export HERDR_BIN_PATH="$scratch/no-herdr"
mkdir -p "$scratch/home"
test_gopath=$(go env GOPATH)
test_gocache=$(go env GOCACHE)
go_files=()
while IFS= read -r -d '' file; do go_files+=("$file"); done < <(git ls-files -z '*.go')
[[ ${#go_files[@]} -gt 0 ]] || fail "no tracked Go files"
unformatted=$(gofmt -l "${go_files[@]}")
[[ -z $unformatted ]] || fail "gofmt required: $unformatted"
go build ./...
go vet ./...
env -u BERMUDA_STATE_DIR HOME="$scratch/home" \
  GOPATH="$test_gopath" GOCACHE="$test_gocache" go test ./...
go build -ldflags "-X github.com/bon5co/bermuda/v3/internal/version.Tag=$tag" \
  -o "$scratch/bermuda" ./cmd/bermuda
reported=$("$scratch/bermuda" --version)
[[ ${reported%%$'\n'*} = "bermuda $tag" ]] || fail "built binary version does not match $tag"
[[ $(git rev-parse HEAD) = "$head" && -z $(git status --porcelain) ]] || fail "checkout changed during validation"
printf 'Validated %s at %s\n' "$tag" "$head"
[[ $mode != --check-only ]] || exit 0
# Recheck remote main and tag immediately before mutation; retries never retarget.
git fetch origin main
[[ $(git rev-parse origin/main) = "$head" ]] || fail "origin/main advanced; validate its new HEAD first"
check_tags
if [[ -z $remote_head ]]; then
  if ! git show-ref --verify --quiet "refs/tags/$tag"; then
    git tag -a "$tag" "$head" -m "$tag"
  fi
  git push origin "refs/tags/$tag:refs/tags/$tag"
fi
if existing=$(gh release view "$tag" --json isDraft --jq '.isDraft' 2>/dev/null); then
  if [[ $existing = false ]]; then
    printf 'Release %s is already published; left unchanged.\n' "$tag"
  elif [[ $mode = --publish ]]; then
    gh release edit "$tag" --draft=false --verify-tag --title "$title" --notes-file "$notes"
  else
    gh release edit "$tag" --draft --verify-tag --title "$title" --notes-file "$notes"
  fi
else
  release_args=("$tag" --verify-tag --title "$title" --notes-file "$notes")
  [[ $mode != --draft ]] || release_args+=(--draft)
  gh release create "${release_args[@]}"
fi
gh release view "$tag" --json url,isDraft,tagName
