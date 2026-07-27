# Building and testing

```bash
make build      # stamps the version from `git describe`
make check      # vet + tests, what must pass before a merge
make version    # show what a build would stamp
```

`make` is a convenience, never a requirement: the Go toolchain is the only thing
needed to build or install bermuda, and the Herdr plugin builds with plain
`go build`. Go stamps the commit revision itself. `make` exists for the two cases
it cannot cover — a released tag, which says more than
a hash, and a build from a git worktree, where Go skips VCS stamping because the
worktree's `.git` is a file rather than a repository.

Tag a release and the version follows automatically:

```bash
git tag v1.2.3 && make build   # header reads: Bermuda v1.2.3 ●
```

## Testing against a store that is not yours

The state directory is the whole of bermuda's state, so point it somewhere else
and nothing can touch the real database:

```bash
export BERMUDA_STATE_DIR=~/scratchpad/bermuda-test
```

There is no `BERMUDA_HOME`. A name bermuda does not read is silently ignored,
and the test then writes to the live store.

Copying a store means copying the **whole directory**. SQLite runs in WAL mode
and the daemon holds the database open, so recent rows are in `bermuda.db-wal`:
`bermuda.db` alone can restore as empty.

## The demo container

`demo/` builds a clean Ubuntu with herdr, bermuda and a demo store in it, and
takes the screenshots in this documentation by driving a real terminal through
[VHS](https://github.com/charmbracelet/vhs):

```bash
docker build --build-arg VERSION=$(git describe --tags --always) \
  -f demo/Dockerfile -t bermuda-demo .
docker run --rm --cap-add SYS_ADMIN -v "$PWD/assets:/out" bermuda-demo
```

It doubles as a test of the install instructions above: the image starts from
`ubuntu:24.04` with nothing on it, so anything the README forgets to mention
fails the build.

Three details the container needs, each of which fails differently:

- `--cap-add SYS_ADMIN` — VHS screenshots through a headless Chromium, which
  cannot start in a default container. Without it: `Failed to launch the
  browser`, then a stack trace.
- **not root** — Chromium refuses to run as root without `--no-sandbox`, which
  VHS gives no way to pass. The image runs as Ubuntu's stock `ubuntu` user
  (uid 1000), which also means files written to a mounted `/out` belong to you.
- `--build-arg VERSION` — the build context has no `.git`, so an unaided build
  stamps every screenshot `dev`.

The demo store is built at run time rather than baked into the image, because
it carries timestamps: a screenshot that says "3 days ago" for something the
image built in March is worse than no screenshot. Every row in it is real —
the jobs are added through the CLI, the runs are `run` steps that execute in
the container, and the parked one actually failed.

## Nice to have

- **Logo.** A terminal bitmap (half-block cells, two pixels per row) can render
  a real image, but at any size that reads clearly it costs more vertical rows
  than a split pane can spare. Parked until there is a version that looks good
  small.

---

[← back to the README](../README.md)
