---
name: bermuda
description: Coordinate with other agents and run multi-step work through the bermuda harness — threads (what changed on this machine), claims (exclusive resources like the browser), @mentions, and workflows (declared steps instead of one long prompt). Use before taking a shared resource, when another agent needs to know something, and whenever a task has a step that must not be skipped.
---

# Bermuda

Agent harness on this machine: scheduled jobs, declared workflows, and a shared
record that outlives any one agent. Repo: https://github.com/bon5co/bermuda

Check it is there before relying on it:

```bash
bermuda --version
```

If that is not found, bermuda is installed as a herdr plugin but not on `$PATH`.
The plugin's own copy lives under the herdr-managed checkout; `go install
github.com/bon5co/bermuda/cmd/bermuda@latest` puts one where you can type it.
Both talk to the same store.

## Threads — what is currently true

Agents are ephemeral, the machine is not. Memory files are snapshots that nobody
marks stale; a thread is append-only and written by whoever changed the thing.
Not a chat channel — five kinds only: `claim`, `release`, `event`, `ask`, `note`.

```bash
bermuda thread list                              # every thread, size, last activity
bermuda thread log --since 1h                    # one thread, oldest first
bermuda thread log --all --limit 200             # every thread at once
bermuda thread log --kind claim,event --json
bermuda thread event 'removed camoufox'          # anyone whose memory is now stale
bermuda thread post 'gog is gmail-read only, no send'
bermuda thread new betterlingo --about 'the django saas'
bermuda thread rm betterlingo --force            # and every message in it
```

Read commands need no identity. Writes need one — see below.

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
basename. `@all` is everybody but you.

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

## Workflows — when a step must not be skipped

**If a task has a step that must not be skipped, that step belongs in a workflow.**
A sentence in a prompt is the thing that gets skipped, and skipping it produces no
error. A workflow moves the sequence out of the model's head and into the harness.

Steps are JSON. Exactly one of `agent` (a prompt, its own process) or `run` (a
shell command, no agent at all):

```json
[
  {"id": "survey", "agent": "read the repo and list what changed", "model": "sonnet"},
  {"id": "build",  "agent": "make the change", "model": "opus", "effort": "high"},
  {"id": "verify", "run": "go build ./... && go test ./..."},
  {"id": "review", "agent": "review the diff", "subagent": "adversarial-review"}
]
```

```bash
bermuda job add --id nightly --steps steps.json --cwd ~/Projects/x [--cron '0 4 * * *']
echo '[...]' | bermuda job add --id nightly --steps -      # or from stdin
bermuda job show nightly                                   # step table
bermuda workflow run nightly
bermuda workflow status <run-id>                           # per-step outcome + duration
bermuda workflow resume <run-id>                           # restart at the step that parked
```

- Per-step `model` / `effort` / `kind` / `subagent` default to the job's and
  override it — a two-line mechanical edit need not burn the model a
  judgement-heavy step needs. All four are refused on a `run` step.
- A step is complete when its `result.json` says ok. A step that fails or writes
  none **parks the workflow there** and the later steps never start. Exit code 1.
- `resume` reuses the run row and directory: completed steps are found on disk and
  are not paid for twice.
- Refused at `job add` time: haiku (named or inherited — the floor is sonnet),
  duplicate ids, a step with both `agent` and `run` or neither, agent config on a
  `run` step, and `--prompt` together with `--steps`.
- The timeout is the job's and is applied **per step**, so an N-step workflow can
  run N times its stated deadline.

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

`bermuda board`. `1`/`2`/`3` jobs/runs/threads, `j`/`k` move, `/` search, `R` run
the selected job, `space` expand a workflow run's steps, `i` write into the
thread, `<`/`>` step along the thread row, `t` thread picker, `q` quit.

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
