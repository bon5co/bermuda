# Build bermuda with a version it can report.
#
# This is a convenience, never a dependency: the Herdr plugin and anyone
# installing bermuda build with plain `go build`, so the Go toolchain is the only
# requirement. Nothing here may become necessary to build or run bermuda.
#
# Go already stamps the git revision into a plain `go build`, so this exists for
# the two cases it cannot cover:
#
#   1. a released version — a tag says more than a hash
#   2. a build from a git worktree — Go skips VCS stamping there, because the
#      worktree's .git is a file rather than a repository, so an unaided build
#      reports "dev"
#
# Both are handled by asking git what this commit is called and passing the
# answer through -ldflags.

BIN     := bin/bermuda
PKG     := ./cmd/bermuda
VERSION_PKG := github.com/bon5co/bermuda/v2/internal/version

# `git describe` gives the most specific name this commit has:
#   v1.2.3            exactly a tag
#   v1.2.3-4-gabc1234 four commits past v1.2.3
#   abc1234           no tags in the repo yet
# --dirty appends -dirty when the tree has uncommitted changes, so a build is
# never mistaken for the commit it was merely started from.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)

ifeq ($(VERSION),)
LDFLAGS :=
else
LDFLAGS := -ldflags "-X $(VERSION_PKG).Tag=$(VERSION)"
endif

.PHONY: build check sec test vet fmt version clean install-plugin

## build: compile the binary with its version stamped in
build:
	go build $(LDFLAGS) -o $(BIN) $(PKG)

## check: everything that must pass before a PR is merged
check: vet test

test:
	go test ./... -cover

vet:
	go vet ./...

## sec: the two security scans CI runs, with the same rules and exclusions
##      (see .github/workflows/security.yml for why each rule is left out)
sec:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@latest \
		-exclude=G104,G202,G203,G204,G304,G702,G703 \
		-severity=medium -confidence=medium \
		-exclude-generated -quiet ./...

fmt:
	gofmt -w .

## version: show what a build would stamp, without building
version:
	@echo "$(if $(VERSION),$(VERSION),unstamped — go build will use the embedded revision)"

clean:
	rm -f $(BIN)

## install-plugin: rebuild and re-register the Herdr plugin from this checkout
install-plugin: build
	herdr plugin unlink bon5co.bermuda 2>/dev/null || true
	herdr plugin link $(CURDIR)
