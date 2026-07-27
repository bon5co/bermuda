# bermuda

An agent harness. Bermuda schedules work and runs each job as an **interactive
[herdr](https://herdr.dev) agent** — not a headless `claude -p` — so every run
can be attached, inspected, interrupted, and answered while it is happening.

![The board's jobs tab](assets/board-jobs.png)

## Why

A headless run is invisible until it finishes, and a run that stops to ask a
question is a lost run. Running on herdr instead means `herdr agent attach` drops
you into a live run, and three things follow that are most of the design:

- **Park, never drop.** Timeout, `blocked`, and "the agent exited without writing
  `result.json`" all park the run for a human rather than discarding it.
- **One result channel.** Each run gets `BERMUDA_RUN_DIR`; the `result.json` the
  agent writes there is the *only* authority on the outcome. Terminal output is
  archived for humans and never parsed.
- **A workspace bermuda owns.** Runs live in a workspace bermuda created,
  identified by the id in `~/.bermuda/workspace.json` — never by being called
  Bermuda, which may be a name you already use. Your own spaces are never
  touched.

## Install

```bash
herdr plugin install bon5co/bermuda
```

A plain `go build`, so **a Go toolchain is the only prerequisite**. Herdr shows
you the source and every command it will run first; `--yes` skips the prompt,
`--ref` pins a revision. Registering is what starts the scheduler and puts the
board [one keystroke away](docs/board.md#in-herdrs-sidebar).

For `bermuda` as a command anywhere, install the CLI too — a separate copy on
your `$PATH`, talking to the same store:

```bash
go install github.com/bon5co/bermuda/cmd/bermuda@latest
```

Everything lives in `~/.bermuda` (`$BERMUDA_STATE_DIR` overrides). No config
file, no daemon to install. `herdr plugin uninstall bon5co/bermuda` removes it
and leaves your store alone.

Working on bermuda itself? See [building and testing](docs/development.md).

## A first job

```bash
bermuda job add --id daily-brief --name "Daily brief" \
  --prompt 'Summarize what changed in this repo today.' \
  --cron '0 7 * * *' --model sonnet --cwd ~/code/project

bermuda job run daily-brief    # now, without waiting for the schedule
bermuda board                  # watch it, and everything else
```

Anything with more than one step is a flow rather than a longer prompt, because
the step a prompt asks an agent to remember is the step that gets skipped:

```bash
echo '[{"id":"build","run":"go build ./..."},
       {"id":"review","agent":"review the diff","model":"opus"}]' |
  bermuda job add --id nightly --steps - --cron '0 4 * * *'
```

An ad-hoc run needs no job at all:

```bash
bermuda run-once --prompt 'Summarize today.' --timeout 15m
```

## Threads: what is currently true

Agents are ephemeral; the machine they act on is not. Memory files are snapshots
of a world other agents keep changing, and nothing marks them stale. A thread is
the opposite — append-only, written by whoever changed the thing, read by whoever
comes next:

```bash
bermuda thread event 'removed camoufox'         # anyone whose memory is now stale
bermuda thread post '@all the browser is free'  # everyone in this workspace
bermuda thread log --since 1h
```

![The board's threads tab, with a claim and an @mention](assets/board-threads.png)

It is deliberately not a chat channel: five message kinds, no more, because an
agent given a conversation will fill it at real token cost for no information.
For the same reason a read is bounded — the last 50 messages, nothing older than
24h — and `@all` reaches your workspace rather than every agent on the machine.

**You never create a thread.** Your herdr workspace already has one, created the
first time anything is said in that space, and every agent in the window is
already in it. Close the space and the thread closes with it — still readable,
never deleted.

The same record carries **claims**, which is how one browser stays one browser:

```bash
bermuda thread with browser --ttl 20m --why 'reddit posting' -- ./post.sh
```

`thread with` takes the lease, runs the command, and releases on every exit path
— including a signal. An agent that asks for something somebody else holds is
told who has it and when it frees, so it can decide whether to wait:

![thread status, and the refusal a second agent gets](assets/thread-claim.png)

Leases expire at read time rather than by a sweeper, so a killed agent cannot
hold the browser forever. Full detail in
[threads, claims and mentions](docs/threads.md).

## Documentation

| | |
|---|---|
| [Jobs](docs/jobs.md) | what a job is, its fields, schedules, tags, editing from the board |
| [Flows](docs/flows.md) | declared steps, parking, resuming, per-step model and effort |
| [Threads, claims and mentions](docs/threads.md) | the record agents leave each other, exclusive resources, `@name` delivery, identity |
| [The board](docs/board.md) | every key, the tabs, the inspector, search |
| [The scheduler](docs/scheduler.md) | the daemon and its sentinel, catchup, stopping it |
| [Building and testing](docs/development.md) | make targets, version stamping, the demo container |

## For the agents

Most of bermuda's users are not people.
[`skills/bermuda/`](skills/bermuda/SKILL.md) is an
[Agent Skill](https://agentskills.io): what an agent should read before it writes
to a thread, takes a claim, or declares a flow — including the traps, which is
the half a command's `--help` cannot tell it.

A skill is just a folder with a `SKILL.md`, so installing it is a symlink:

```bash
ln -s "$PWD/bermuda/skills/bermuda" ~/.claude/skills/bermuda
```

`~/.claude/skills/` for every project, `<project>/.claude/skills/` for one (commit
it and your team has it too), `<project>/.agents/skills/` for the cross-tool
location other clients read. Symlink rather than copy when you want it to follow
the code — a copy is a snapshot that will quietly age past the commands it
documents. Inside this repo it loads by itself; if you installed the plugin,
herdr already has a copy.

## Status

**v1.1.1**, in daily use on the machine it was written for. Every part of it —
jobs, flows, threads, claims, the scheduler and its off switch — is checked on
each release by installing it from GitHub into a bare Ubuntu container and using
it there: see [end to end, as a stranger](docs/development.md#end-to-end-as-a-stranger).

The contract is the CLI. Everything else lives under `internal/`, so nothing here
is importable as a Go library — deliberately.

## License

MIT — see [LICENSE](LICENSE).
