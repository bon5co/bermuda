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

`make` is a convenience. Herdr already requires Git to install from GitHub;
Bermuda additionally needs Go. Herdr fetches a single commit without local tag
refs, so the manifest fetches shallow tag refs before running plain `go build`.
Go embeds an exact release tag automatically. Builds between releases show the
short source revision; modified builds remain marked as modified. The identity
comes from the binary, without a committed `VERSION` or manifest fallback.

For a release, bump the manifest version in a reviewed commit, merge it, and
run the reusable release procedure from a clean checkout at `origin/main`:

```bash
scripts/release.sh --check-only v3.4.0
scripts/release.sh --draft v3.4.0 /path/to/release-notes.md "v3.4.0 — agent activity at a glance"
scripts/release.sh --publish v3.4.0 /path/to/release-notes.md "v3.4.0 — agent activity at a glance"
herdr plugin install bon5co/bermuda --ref v3.4.0 --yes
```

The script checks formatting, build, vet, the full test suite and the executable
version before tagging the exact validated commit. Tests use a temporary home
under the original home directory, outside system temporary directories,
clear any inherited `BERMUDA_STATE_DIR`, and retain the existing Go caches;
Git and GitHub CLI keep the original home and credentials. It publishes source-only
releases, refuses to move existing tags, and can resume a draft or a failed
publication safely. Go must be on the validation machine's `PATH`; Git and
GitHub CLI must be on the calling machine's `PATH`.

To run Go validation on another machine, set `BERMUDA_RELEASE_CHECK_RUNNER` to
an executable runner path:

```bash
BERMUDA_RELEASE_CHECK_RUNNER=/path/to/remote-runner \
  scripts/release.sh --publish v3.4.0 /path/to/release-notes.md
```

The runner receives `-- scripts/release.sh --go-checks v3.4.0`. It must synchronize
the current source tree, execute that command from the source root, and return
its exit status. The private `--go-checks` mode needs no Git metadata and runs
formatting, build, vet, the full tests with an isolated home, and a stamped
executable version check on the execution machine. It never tags or publishes.
The outer script still checks the clean commit, fresh `origin/main`, manifest,
and tags locally, and rechecks the commit after validation. With no runner,
validation runs locally as before. A failing runner stops publication.

Without `--ref`, Herdr installs the repository's default HEAD, which can be newer
than the latest release. A `go install …@<pseudo-version>` binary likewise reports
the source revision embedded in that module version.

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

A store under `/tmp`, `/var/tmp` or the system temp directory is a **scratch
store**, and Bermuda refuses to start the daemon or the sentinel detached
against one. A detached pair outlives whatever started it, and when the temp
directory it serves is removed the pair is unreachable — `bermuda stop` writes
its flag into a directory that no longer exists, while each half revives the
other every five seconds. Foreground runs and every other command are
unaffected; a background pair in a temp directory is available on request:

```bash
export BERMUDA_ALLOW_SCRATCH_DAEMON=1
```

Prefer a scratch store somewhere that is not temporary, as above.

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

Bermuda ships two [Agent Skills](https://agentskills.io):

- [bermuda](../skills/bermuda/SKILL.md): A.LOG updates and harness operations,
  including threads, claims, memory, checklists and flows.
- [bermuda-self-improve](../skills/bermuda-self-improve/SKILL.md): retrieve
  relevant lessons, record discoveries, mistakes and verified recoveries in
  IMPROVE, then apply and verify what was learned.

```bash
npx skills add bon5co/bermuda
```

Select both skills for the agent you use. To place them manually, copy or link
both folders into its discovery directory:

| directory | agent and scope |
|---|---|
| `~/.agents/skills/<skill>/` | Codex, every project |
| `<project>/.agents/skills/<skill>/` | Codex, that repository |
| `~/.claude/skills/<skill>/` | Claude Code, every project |
| `<project>/.claude/skills/<skill>/` | Claude Code, that project |

Codex scans repository `.agents/skills` directories from the working directory
up to the repository root, and follows symlinked skill folders. Its user
location is `~/.agents/skills`; see [OpenAI's skill discovery
documentation](https://learn.chatgpt.com/docs/build-skills#where-codex-loads-local-skills).

For example, install both skills for Codex from a checkout:

```bash
git clone https://github.com/bon5co/bermuda
mkdir -p "$HOME/.agents/skills"
ln -s "$PWD/bermuda/skills/bermuda" "$HOME/.agents/skills/bermuda"
ln -s "$PWD/bermuda/skills/bermuda-self-improve" "$HOME/.agents/skills/bermuda-self-improve"
```

For Claude Code, use `~/.claude/skills` as the target directory instead.
Link a checkout when the skills should follow its updates; a copy remains a
snapshot. Verify that each link resolves to the intended `SKILL.md` file.
The repository keeps Claude discovery links under `.claude/skills`. A Herdr
plugin installation also contains the sources under
`~/.config/herdr/plugins/github/bon5co.bermuda-<hash>/skills/`; `herdr plugin
list` reports the installed source and revision.

The shipped [bermuda-install](../skills/bermuda-install/SKILL.md) setup skill
places a short pointer in the user's standing instructions:
`~/.codex/AGENTS.md` for Codex or `~/.claude/CLAUDE.md` for Claude Code, unless
the user names another file. It follows symlinks, updates its managed block
without duplication and leaves execution permissions unchanged. Ask the
agent to set up Bermuda for the agent you use. Manuals remain in the two
skills, rather than being copied into every session's instructions.

## Nice to have

- **Logo.** A terminal bitmap (half-block cells, two pixels per row) can render
  a real image, but at any size that reads clearly it costs more vertical rows
  than a split pane can spare. Parked until there is a version that looks good
  small.

---

[← back to the README](../README.md)
