# bermuda

An agent harness. Bermuda schedules work and runs each job as an **interactive
herdr agent** — not a headless `claude -p` — so every run can be attached,
inspected, interrupted, and answered while it is happening.

![The board's jobs tab](assets/board-jobs.png)

## Why

Headless agent runs are invisible until they finish, and a run that stops to
ask a question is simply a lost run. Bermuda drives [herdr](https://herdr.dev)
as its execution substrate, which means:

- a run that needs a human goes to `blocked`, and bermuda **parks** it with the
  tab left open instead of failing it
- `herdr agent attach` drops you straight into a live run
- `herdr agent send-keys` lets bermuda (or you) answer a prompt and resume

Three things follow from that, and they are most of the design:

- **A workspace bermuda owns.** All runs live in a `Bermuda` workspace, one tab
  per run. It is bermuda's by having been created by bermuda, whose id is
  recorded in `~/.bermuda/workspace.json` — not by being called Bermuda, which
  is a name you may already have used. A space bermuda did not make is never
  adopted, so your own session is never touched.
- **One result channel.** Each run gets `BERMUDA_RUN_DIR` in its shell. The
  agent writes `result.json` there, and that file is the *only* authority on a
  run's outcome. Terminal output is archived as `transcript.txt` for humans and
  is never parsed.
- **Park, never drop.** Timeout, `blocked`, and "agent exited without writing
  result.json" all park the run for a human rather than discarding it.

## Install

Bermuda is a [herdr](https://herdr.dev) plugin — herdr is where the runs live —
so it installs the way every herdr plugin does:

```bash
herdr plugin install bon5co/bermuda
```

That clones the repo, runs the one build command in `herdr-plugin.toml` (a plain
`go build`, so a **Go toolchain is the only prerequisite**), and registers the
plugin. Herdr shows you the source and every command it will run before it does
any of it; add `--yes` to skip the prompt, or `--ref` to pin a revision.

Registration is what starts the scheduler, puts bermuda in herdr's sidebar, and
puts the board one keystroke away — see [the board](docs/board.md#in-herdrs-sidebar).

To reach `bermuda` as a command anywhere, install the CLI too. The plugin runs
from its own managed checkout, so this is a separate copy on your `$PATH`:

```bash
go install github.com/bon5co/bermuda/cmd/bermuda@latest
```

Both talk to the same store, so it does not matter which one you type at.

Everything bermuda keeps lives in `~/.bermuda` (override with
`$BERMUDA_STATE_DIR`). There is no config file and no daemon to install.

To remove it: `herdr plugin uninstall bon5co/bermuda`, which unregisters it and
deletes the managed checkout. Your store is left alone.

Working on bermuda itself rather than using it? See
[building and testing](docs/development.md), which starts with linking a
checkout instead of installing one.

## A first job

```bash
bermuda job add --id daily-brief --name "Daily brief" \
  --prompt 'Summarize what changed in this repo today.' \
  --cron '0 7 * * *' --model sonnet --cwd ~/code/project

bermuda job run daily-brief    # now, without waiting for the schedule
bermuda board                  # watch it, and everything else
```

Anything with more than one step is a workflow rather than a longer prompt,
because the step a prompt asks an agent to remember is the step that gets
skipped:

```bash
echo '[{"id":"build","run":"go build ./..."},
       {"id":"review","agent":"review the diff","model":"opus"}]' |
  bermuda job add --id nightly --steps - --cron '0 4 * * *'
```

An ad-hoc run needs no job at all:

```bash
bermuda run-once --prompt 'Summarize today.' --timeout 15m
```

## Documentation

| | |
|---|---|
| [Jobs](docs/jobs.md) | what a job is, its fields, schedules, tags, editing from the board |
| [Workflows](docs/workflows.md) | declared steps, parking, resuming, per-step model and effort |
| [Threads, claims and mentions](docs/threads.md) | the record agents leave each other, exclusive resources, `@name` delivery, identity |
| [The board](docs/board.md) | every key, the tabs, the inspector, search |
| [The scheduler](docs/scheduler.md) | the daemon and its sentinel, catchup, stopping it, what bermuda touches in herdr |
| [Building and testing](docs/development.md) | make targets, version stamping, the demo container |

## For the agents

Most of bermuda's users are not people. [`skills/bermuda/`](skills/bermuda/SKILL.md)
is an [Agent Skill](https://agentskills.io): what an agent should read before it
writes to a thread, takes a claim, or declares a workflow — including the traps,
which is the half a command's `--help` cannot tell it.

It is loaded automatically in this repo through `.claude/skills/bermuda`, a
symlink to the same directory. To give it to an agent working somewhere else,
copy or symlink that folder into the skills directory your agent reads:

```bash
ln -s "$PWD/skills/bermuda" ~/.claude/skills/bermuda        # Claude Code, every project
```

## Status

Early, and in daily use on the machine it was written for. `bermuda run-once`
executes a single job through the full runner lifecycle; the scheduler daemon,
the store and the board all build on that same runner.

The contract is the CLI. Everything else lives under `internal/`, so nothing
here is importable as a Go library — deliberately.

## License

MIT — see [LICENSE](LICENSE).
