# Building and testing

## A checkout you can work in

Link the checkout rather than installing it, which registers it where it stands
and leaves you editing the files that actually run:

```bash
git clone https://github.com/bon5co/bermuda && cd bermuda
make build                       # link does not run build commands — install does
herdr plugin link "$PWD"
```

`herdr plugin unlink bon5co.bermuda` undoes that and leaves your files alone.
Installing over a locally linked plugin is refused, so unlink before going back
to the released one.

```bash
make build      # stamps the version from `git describe`
make check      # vet + tests, what must pass before a merge
make version    # show what a build would stamp
```

`make` is a convenience, never a requirement: the Go toolchain is the only thing
needed to build or install Bermuda, and the Herdr plugin builds with plain
`go build`. Go stamps the commit revision itself. `make` exists for the two cases
it cannot cover — a released tag, which says more than
a hash, and a build from a git worktree, where Go skips VCS stamping because the
worktree's `.git` is a file rather than a repository.

Tag a release and the version follows automatically:

```bash
git tag v1.2.3 && make build   # header reads: Bermuda v1.2.3 ●
```

## Running your checkout as the plugin

```bash
make install-plugin    # build, then unlink and relink this directory
```

`herdr plugin link` registers a directory where it stands and — unlike
`herdr plugin install` — **does not run the manifest's build commands**, so a
linked checkout runs whatever binary is in `./bin` right now. That is why the
make target builds first, and why a linked plugin can silently run last week's
code after a `git pull`.

The board notices anyway: it watches its own binary and re-execs when it
changes, so a board left open picks up a rebuild without being restarted.

## Testing against a store that is not yours

The state directory is the whole of Bermuda's state, so point it somewhere else
and nothing can touch the real database:

```bash
export BERMUDA_STATE_DIR=~/scratchpad/bermuda-test
```

There is no `BERMUDA_HOME`. A name Bermuda does not read is silently ignored,
and the test then writes to the live store.

Copying a store means copying the **whole directory**. SQLite runs in WAL mode
and the daemon holds the database open, so recent rows are in `bermuda.db-wal`:
`bermuda.db` alone can restore as empty.

## End to end, as a stranger

`demo/e2e.Dockerfile` starts from a bare Ubuntu with Go, git and herdr on it and
nothing else, installs Bermuda **from GitHub the way the README says to**, and
then uses it: jobs, a flow that really runs, a failing step that parks, the
thread and its claims, the scheduler and its off switch, the board's refusal to
draw with no terminal, and an uninstall that leaves the store behind.

```bash
docker build -f demo/e2e.Dockerfile -t bermuda-e2e .
docker run --rm bermuda-e2e
```

It tests what the demo container cannot: the demo builds from the working tree,
which proves the code works and says nothing about whether anyone else can
install it. Every check here is a promise the README or the docs make. Two of
them were already wrong when the suite was first run — `herdr plugin list` does
not print the plugin directory, and asking herdr what actions it registered
needs a running server — and both were documentation bugs, not test bugs.

## The demo container

`demo/` builds a clean Ubuntu with herdr, Bermuda and a demo store in it, and
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

## The skill

`skills/bermuda/` is an [Agent Skill](https://agentskills.io) — what an agent
should read before it writes to a thread, takes a claim, or calls a flow. A
skill is just a folder with a `SKILL.md` in it, so installing it is a copy or a
symlink and nothing more.

| where it goes | who reads it |
|---|---|
| `~/.claude/skills/bermuda/` | Claude Code, in every project |
| `<project>/.claude/skills/bermuda/` | Claude Code, that project only — commit it and your team has it too |
| `<project>/.agents/skills/bermuda/` | the cross-tool location other agent clients read |

```bash
git clone https://github.com/bon5co/bermuda
ln -s "$PWD/bermuda/skills/bermuda" ~/.claude/skills/bermuda
```

**Symlink rather than copy** when you want it to follow the code: a linked skill
picks up the next `git pull`, and a copied one is a snapshot that will quietly
age past the commands it documents — which is worse than no skill, because an
agent trusts it either way.

Inside this repo it loads by itself through `.claude/skills/bermuda`, a symlink
to the same directory. If you installed the plugin rather than cloning, Herdr
already has a copy under
`~/.config/herdr/plugins/github/bon5co.bermuda-<hash>/skills/bermuda`. (`herdr
plugin list` names the source, `github:bon5co/bermuda@<commit>`, rather than that
path.)

## Nice to have

- **Logo.** A terminal bitmap (half-block cells, two pixels per row) can render
  a real image, but at any size that reads clearly it costs more vertical rows
  than a split pane can spare. Parked until there is a version that looks good
  small.

---

[← back to the README](../README.md)
