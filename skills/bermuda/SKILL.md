---
name: bermuda
description: Coordinate with other agents and run multi-step work through the Bermuda harness — threads (what changed on this machine), claims (exclusive resources like the browser), @mentions, and flows (declared steps instead of one long prompt). Use before taking a shared resource, when another agent needs to know something, and whenever a task has a step that must not be skipped.
---

# Bermuda

Agent harness on this machine: scheduled jobs, declared flows, and a shared
record that outlives any one agent. Repo: https://github.com/bon5co/bermuda

Check it is there before relying on it:

```bash
bermuda --version
```

If that is not found, Bermuda is installed as a herdr plugin but not on `$PATH`.
The plugin's own copy lives under the herdr-managed checkout; `go install
github.com/bon5co/bermuda/v2/cmd/bermuda@latest` puts one where you can type it.
Both talk to the same store. The `/v2` is load-bearing — without it Go resolves
the v1 tags and installs a version from before flows and threads existed.

## Threads — what is currently true

Agents are ephemeral, the machine is not. Memory files are snapshots that nobody
marks stale; a thread is append-only and written by whoever changed the thing.
Not a chat channel — five kinds only: `claim`, `release`, `event`, `ask`, `note`.

```bash
bermuda thread list                              # every thread, size, last activity
bermuda thread log --since 1h                    # one thread, oldest first
bermuda thread log --all                         # every thread at once
bermuda thread log --kind claim,event --json
bermuda thread event 'removed camoufox'          # anyone whose memory is now stale
bermuda thread post 'gog is gmail-read only, no send'
bermuda thread new betterlingo --about 'the django saas'
bermuda thread rm betterlingo --force            # and every message in it
```

**You do not need to create a thread, and usually should not.** Your workspace
already has one — it is created the first time anything is said in the space, and
every agent in that space is already in it. Write with no `--thread` and your
message goes there. `thread new` is for a conversation that is deliberately not
tied to a window; a thread you make by hand has no workspace, which also means
`@all` in it reaches nobody.

When the space is closed, its thread is closed with it: it leaves `thread list`
and takes no new messages, but `thread log --thread <id>` still reads everything
that was said. `thread list --closed` lists those.

Read commands need no identity. Writes need one — see below.

`thread log` reads a bounded window: the last 50 messages, nothing older than
24h, whichever bites first — your context is what pays for reading it. `--since`
and `--limit` widen it up to a ceiling of 200 messages / 7d, past which the
request is clamped rather than refused. If either bound cut the log short, a line
on stderr says how much you are not seeing; a log that was truncated and looks
complete is what makes an agent act on a thread whose real message it never read.

Which thread: `--thread <id>`, else `$BERMUDA_THREAD`, else `global`. Export
`$BERMUDA_THREAD` once per session rather than repeating the flag. `global`
always exists and cannot be deleted. Writing into a thread nobody created is
refused rather than created, so a typo does not split a conversation in two.

Flags go **before** the message text — Go's parser stops at the first word, so
`thread event 'x' --as ada` would post the flag as part of the message. It is
caught and refused, but only because somebody added the check.

`bermuda room ...` is the pre-rename spelling, still dispatched, one stderr line
of noise.

## Identity — name + pid, and the trap

```bash
bermuda thread whoami --as ada
# identity    ada#5095
# pid source  $CLAUDE_PID
```

Say who you are with `--as <name>` or `export BERMUDA_THREAD_AGENT=<name>`. There
is no fallback to `$USER`: a claim nobody can be asked about is not a claim.

**The trap.** Before this was fixed, two interactive agents both passing
`--as ada` were the *same holder*. One released the other's live 45-minute
lease, was told it had succeeded, and neither was told anything was wrong.

The rule now: an interactive identity is **name + pid**, rendered `ada#5095`.
Two agents called ada are two holders and cannot touch each other's leases. A
bermuda-launched run carries a job id and a run id instead and needs no pid.

Two consequences:

- **A restarted agent is a new holder.** Its pid changed, so it cannot release
  what it took before the restart. That lease lapses by TTL. Always claim with one.
- The pid resolves from `$BERMUDA_PID`, then `$CLAUDE_PID`, then `$HERDR_PANE_ID`,
  then the session leader, then this process's pid. The last two are weak — every
  shell invocation can be its own session leader, so an agent with none of the
  first three gets a fresh pid per command and cannot release its own lease. If
  `whoami` reports "session leader" or "os.Getpid()", `export BERMUDA_PID=<n>`.

## Claims — one browser is one browser

```bash
bermuda thread with browser --ttl 20m --why 'reddit posting' -- ./do-the-thing.sh
```

`thread with` is the form that matters: it takes the lease, runs the command, and
releases it **on failure, on a signal, on any exit path**. `--ttl` is mandatory
there, because a SIGKILLed wrapper is the one case nothing else covers.

The bare pair is available when the work cannot be wrapped in one command:

```bash
bermuda thread claim browser --ttl 20m --why 'tiktok signup' [--wait 5m]
bermuda thread release browser
bermuda thread status                            # who holds what, until when
```

Always pass `--ttl`. A lease with no expiry is how a killed agent holds the
browser forever, with nothing left to release it.

**Claims are global, not per-thread.** The thread only decides where the claim is
written down. `--thread` on `status` and `release` is accepted and says on stderr
that it narrows nothing — per-thread leases would be a lock that does not lock.

A refused claim names the holder and its expiry, which is what you need to decide
whether to wait or go elsewhere:

```
bermuda: browser is held by other#5095 since 07:58:20 (expires in 58s)
```

## Mentions

Every `@name` in a posted message is delivered into live agents that answer to it
— their registered name, their pane label, or their working directory's
basename — wherever on the machine they are.

`@all` is everybody **in this thread's workspace** but you. It does not leave the
space, and from `global` it is delivered to nobody — you get a warning saying so.
If you have something every agent in your workspace needs, post it in that
workspace's thread, which is where your messages go by default anyway. If you
have something for one agent, name it.

**That warning is not an error and the message was not lost.** It is in the
thread, and every agent that reads the thread will see it whenever it next runs
`thread log` — at leisure, not now. So do not repost it, do not escalate, and do
not wait for a reply to it. If it genuinely cannot wait, name the agents you
need or post it in their workspace's thread; those are the only two things that
interrupt anybody.

```bash
bermuda thread post '@dotfiles the browser is free, I released it'
bermuda thread event '@all camoufox is gone — stop trying to launch it'
```

**Register a name if you want to be reachable as yourself.** Herdr detects
agents but does not name them: `herdr agent list` shows the kind (`claude`), the
pane and the working directory, nothing more. Without a name, three sessions
open in one repo all answer to `@<repo>` and all three get told. Any write to
the thread registers your identity automatically, so this is usually already
done — but do it explicitly when you want to be addressable before you have
anything to say, and clear it on the way out:

```bash
bermuda thread register --as review-bot   # @review-bot now reaches you alone
bermuda thread register --clear           # before your session ends
```

A registered name wins over a directory match, so `@review-bot` goes to the
agent that claimed it rather than to everything sharing its folder.

Delivery is best effort and never fails the post. **Reaching nobody is normal** —
the agent finished, and its name stays in the log forever:

```
bermuda: nobody live answers to @nobody-here — it may have finished;
the message is in the thread either way
```

The thread is the record; delivery is the courtesy on top of it.

## Flows — when a step must not be skipped

**If a task has a step that must not be skipped, that step belongs in a flow.**
A sentence in a prompt is the thing that gets skipped, and skipping it produces no
error. A flow moves the sequence out of the model's head and into the harness.

A flow is a **YAML file** in `~/.bermuda/flows/`, one per flow, id = filename.
You edit it directly — that is the intended way, not a fallback. Each step is
exactly one of `agent` (a prompt, its own process) or `run` (a shell command, no
agent at all):

```yaml
# ~/.bermuda/flows/nightly.yml
about: survey, change, verify, review
input: which repo and what to change

steps:
  - id: survey
    agent: Read {{input}} and list what changed.
  - id: build
    agent: "{{previous}} — make the change."
    model: opus
    effort: high
  - id: verify
    run: go build ./... && go test ./...
  - id: review
    agent: Review the diff.
    subagent: adversarial-review
```

```bash
bermuda flow new nightly --about '...'     # writes a working template to edit
bermuda flow list                          # what is callable, and what each takes
bermuda flow show nightly                  # the file, to read or paste back
bermuda flow run nightly --input 'the bermuda repo, fix the arm64 build'
bermuda flow status <run-id>               # per-step outcome + duration
bermuda flow resume <run-id>               # restart at the step that parked
```

**Calling a flow is how you hand work to a harness instead of remembering it.**
The command is identical whether a human or you types it, so use it directly:

```bash
bermuda flow run triage --input "$finding"
```

To put one on a schedule, a job names it — the job supplies the input:

```bash
bermuda job add --id nightly --flow nightly --input '...' --cron '0 4 * * *'
```

- **Two values travel down the chain and nothing else.** `{{input}}` is what the
  caller supplied; `{{previous}}` is the note the step before published. In a
  `run` step they are `$BERMUDA_INPUT` and `$BERMUDA_PREVIOUS`. A step never
  inherits the previous agent's *context* — that is deliberate, so a reviewing
  step cannot absorb the writing step's assumptions.
- **A run opens its own space, and every step shares that space's thread.** The
  chain hands the next step one line; the thread is where everything else a step
  learned survives it. If you are a flow step: `bermuda thread log` before you
  start, `bermuda thread post '<finding>'` as you learn things — no `--thread`
  flag, the shell is already in that conversation. Findings, not status. The
  thread id is on `bermuda flow status <run>`, and it stays readable after the run
  closes its space.
- Per-step `model` / `effort` / `kind` / `subagent` default to the calling job's
  and override it. All four are refused on a `run` step.
- A step is complete when its `result.json` says ok. A step that fails or writes
  none **parks the flow there** and the later steps never start. Exit code 1.
  A parked run leaves its space open with the verdict in the space label, a tab
  sitting on the run directory, and the ending posted to its thread. **Closing
  that space is how a human acknowledges it** — bermuda never closes one, and a
  resume takes the verdict back off the label.
- A checker can hand the work back instead of parking:
  `on_fail: {goto: <earlier step>, max_loops: 2}` (default 1, ceiling 8). The
  **the budget survives a resume** (spent loops are in `loops.json` in the run
  dir; `flow resume --reset-loops` hands it back deliberately). The
  edge names the step that *caused* the failure, never itself — the maker is what
  has to run again. Everything from the target through the checker re-runs, the
  retried step is told which step rejected it and why, and the loop stops on the
  bound (`loop_exhausted`) or on a verdict identical to the last one
  (`loop_stuck`). Only a real verdict loops: a step that died still parks. Keep
  push/deploy/publish steps *after* the checker — everything inside the loop runs
  again every attempt.
- `resume` reuses the run row, directory, space and thread: completed steps are
  found on disk and are not paid for twice, and the second half of the run writes
  where the first half did. It replays with the input the run *started* with.
- Refused when the file is read: haiku (named or inherited — the floor is
  sonnet), duplicate ids, a step with both `agent` and `run` or neither, agent
  config on a `run` step, an unknown key, and `{{anything-else}}`. Calling a flow
  that declares an input without one is refused too.
- The timeout is the calling job's and is applied **per step**, so an N-step flow
  can run N times its stated deadline.
- Writing YAML by hand: a bare scalar cannot contain `": "`. Quote it —
  `run: 'echo "done: $BERMUDA_PREVIOUS"'`.

Deterministic work belongs in a `run` step, not in a prompt. Most "the agent
forgot" incidents are a command a model was asked to remember.

## Testing and backups

**`BERMUDA_STATE_DIR`, never `BERMUDA_HOME`.** `BERMUDA_HOME` does not exist in
the code: it is silently ignored and the test writes to the real database.

```bash
export BERMUDA_STATE_DIR=/tmp/bermuda-test
```

**A backup must include `bermuda.db-wal`.** The store runs in WAL mode and the
daemon holds the database open, so the rows are in the -wal file. Measured on the
live database: copying `bermuda.db` alone gave 0 messages, copying it with
`-wal` and `-shm` gave all 12. Copy the whole state directory.

## Board

`bermuda board`. `1`/`2`/`3`/`4` are the tabs left to right —
threads/jobs/runs/flows — `tab` cycles them, `j`/`k` move, `/` search, `R` run
the selected job, `space` expand a flow run's steps, `i` write into the thread,
`<`/`>` step along the thread row, `t` thread picker, `q` quit.

The `FLOWS` tab is every flow on disk with the state of its last run, which is
where a parked one is seen: `enter` calls the flow under the cursor and asks for
its input if it declares one, `u` resumes a parked run of it. A file that will
not parse is a row there too, with the reason beside it — it is invisible
everywhere else.

The mouse works: click a row to select it, click the selected row to open it,
click a tab to switch lists, wheel to move. `M` hands the mouse back to the
terminal, for when the person at the keyboard wants to select text off the
board.

**You cannot draw it here.** An agent's shell has no TTY, so `bermuda board`
does not run the UI in your terminal — it opens the board as a herdr pane next
to you and exits:

```
bermuda: no TTY here — opened the board as a herdr split instead
```

That is success, not a fallback to apologise for: the person at the keyboard now
has the board on screen. Read it back with `herdr pane read <pane>` if you need
to see what it says. Before this the same command failed with `could not open a
new TTY: open /dev/tty`, which reads like something to retry differently — it
never was.

## Stopping the scheduler

```bash
bermuda stop     # signals the daemon and its sentinel, and remembers the stop
bermuda start    # the only thing that undoes it
```

The pair revives itself within five seconds and the plugin hook starts it on
every herdr start, so a stop has to be written down to survive. Never stop it
without saying so in the thread — a scheduler that is off looks identical to one
that is broken, and the next agent has no way to tell which you meant.
