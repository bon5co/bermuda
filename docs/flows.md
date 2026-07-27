# Flows

A job is one prompt to one agent. Anything with more than one step is carried in
that agent's head: it has to remember to pull first, to verify after, to hand its
output to the next stage. It usually does. The failures are not dramatic — the
agent does four of five steps and reports success.

A **flow** is a harness that forces an agent through steps it cannot skip:

```
flow(x) = A(x) -> B(A's result) -> C(B's result)
```

where each of A, B and C is its own agent process, and `x` is supplied at the
moment the flow is called — by a person, by another agent, or by a schedule.

What that buys over one prompt saying "then do B, then do C":

- **The harness makes the call.** B is launched by bermuda, not by A remembering
  to hand off. A cannot skip B, inline B, or decide B was already covered.
- **B runs even if A says B's work is done.** An agent's report of its own work
  is the least reliable artifact in the system, and it is not an input to whether
  B runs.
- **If A dies, the flow parks at A.** B never runs on a lie.
- **Each agent step is its own process**, with its own `BERMUDA_STEP_DIR` and its
  own `result.json` — the same contract every run already uses.

## A flow is a file

Flows are YAML in `~/.bermuda/flows/`, one file per flow, and the id is the
filename. They are files rather than database rows because the two populations
that write them are a person in an editor and an agent with a filesystem, and
both already know how to open a file. A file can be diffed, reviewed, copied
between machines, and committed next to the code it is about.

```yaml
# ~/.bermuda/flows/triage.yml
about: triage an incoming report
input: a report, a PR number, or a stack trace

steps:
  - id: assess
    agent: |
      Look at {{input}} and say in one line whether it is real.
    model: opus

  - id: patch
    agent: |
      {{previous}}

      If that says it is real, write the fix. Otherwise stop and say why.

  - id: verify
    run: go test ./...
```

```bash
bermuda flow new triage --about 'triage an incoming report'
bermuda flow list
bermuda flow show triage        # the file itself, to paste back
bermuda flow edit triage        # $EDITOR, then re-validated on the way out
bermuda flow rm triage
```

`flow new` writes a working flow rather than an empty skeleton, because the first
thing anyone does with a new flow is run it to see what happens.

## Calling one

```bash
bermuda flow run triage --input 'PR #431 fails on arm64'
bermuda flow status <run>       # per-step outcome, duration, and note
bermuda flow resume <run>       # restart at the step that parked
```

**The same command for both callers.** There is no agent-only path and no
human-only path, so anything an agent can start a person can start identically,
and both land in the same run list. An agent calls it by shelling out:

```bash
bermuda flow run triage --input "$finding"
```

A flow that declares an input and is called without one is refused rather than
run with a blank — every prompt saying `{{input}}` would otherwise get a hole
where its subject should be, and an agent handed that will invent something to
fill it.

A job calls one on a clock. The job supplies the input, because a schedule is the
only thing available to supply it:

```bash
bermuda job add --id nightly --flow triage \
  --input 'anything filed since yesterday' --cron '0 4 * * *'
```

A job has `--prompt` or `--flow`, never both.

## What travels down the chain

Two values, and nothing else.

| | in an `agent:` step | in a `run:` step |
|---|---|---|
| the input | `{{input}}` | `$BERMUDA_INPUT` |
| the previous step's result | `{{previous}}` | `$BERMUDA_PREVIOUS` |

`{{previous}}` is the `note` the step before published in its `result.json` —
**not its context**. That distinction is load-bearing. Each agent step is its own
process precisely so that the step reviewing something is not the agent that
wrote it; handing B the transcript of A would let B quietly inherit every
assumption A made. A published result is the one line A chose to hand on, which
is a chain. The transcript would be one agent in three costumes.

A `run:` step's command text is never rewritten — it reads the same two values
from the environment, because rewriting it would corrupt any command that
legitimately contains braces.

Placeholders are checked when the file is read, not when it runs. A `{{inpt}}`
discovered mid-flow has already spent an agent turn producing a prompt with
literal braces in it, which the agent will then do its best to interpret. For the
same reason `{{previous}}` in the *first* step is refused: nothing ran before it.

## Steps

A step is either an `agent` (a prompt) or a `run` (a shell command), never both.
`run:` steps have no agent at all, and that is the cheapest reliability win here:
most "the agent forgot" incidents are a shell command a model was asked to
remember instead of a step the harness executes.

An agent step takes the calling job's agent settings as defaults and may override
`model`, `effort`, `kind`, and `subagent` — a two-line mechanical edit should not
burn the model a judgement-heavy step needs, and a reviewing step should be able
to wear a different charter than the step that wrote the thing. **No step may run
on haiku**, whether it names the model itself or inherits it: the house floor is
sonnet, and a flow is exactly where a cheap model quietly does four of five
steps. `run:` steps take none of this configuration and are refused if they try,
since a step that looks configured but is not is worse than one that says so.

An unknown key is refused rather than ignored. A typo in a hand-written file is
the ordinary case here, and a silently dropped `agnet:` is a step that never runs
in a flow that then reports success.

### Permissions

Agent steps run with `--dangerously-skip-permissions` **by default**. That is not
a convenience: a flow step has nobody sitting in its pane, so a permission prompt
is a step that waits out its grace and then parks. Every agent step in a flow is
unattended by construction.

A flow that does something consequential can take the bypass back, and a single
step can override either way:

```yaml
skip_permissions: false        # this whole flow asks

steps:
  - id: survey
    agent: read and report
    skip_permissions: true     # ...except this one, which only reads
  - id: apply
    agent: make the change
```

Precedence is step, then flow, then whatever the calling job already said. Only
an explicit value in the file overrides the job — a flow with no opinion behaves
like anything else that job started, rather than silently undoing a job that
deliberately asked for permission checks.

With the bypass off, the job's `--permission-mode` applies instead. On a `run:`
step the key is refused: a shell command has no agent to configure and runs with
whatever permissions the process already has.

## Failure is the point

A step that reports failure, or that ends without writing `result.json`, **parks
the flow at that step**, and the steps after it never start. Everything the
completed steps wrote stays on disk, so `bermuda flow resume` picks up at the
failed step and does not redo the expensive ones — completion is decided by a
successful `result.json` in the step's directory, not by a database row, so a
resume works long after the process that ran the first attempt is gone. A retried
step's stale result is removed before it starts, so an agent that dies cannot be
classified by the last attempt's verdict.

A resumed run carries the input it *started* with, not today's. The run records
its own flow and input, which is also why a flow called with no job at all can
still be resumed.

```
STEP  KIND  OUTCOME  REASON  DURATION  NOTE
a     run   done             0s        A saw [PR #431]
b     run   done             0s        B saw [A saw [PR #431]]
gate  run   failed           0s        exit status 1
c     run   pending
```

`c` never started. That is the feature.

## On the board

Flows have their own tab: what is callable, what each takes, and when it last
ran. `enter` runs one, asking for the input first. `u` unparks a parked run — the
board could always *show* a parked flow and never act on it, which was the
sharper of the two gaps.

A flow *run* stays one row in RUNS, because it was launched once, reading
`2/4 · verify`: how many steps are done, of how many, and which one it is on.
Space opens the steps under it.

## Deliberately out of scope

Templating beyond `{{input}}` and `{{previous}}`, the `expect:` assertion
vocabulary, `check:` commands, `gate: human`, `fresh:`, parallelism, and
branching. Series only — the failures this was built from were sequencing
failures, not throughput ones.

The verb used to be `workflow`. It still dispatches, with a line on stderr, so a
cron entry or launcher script written before the rename keeps running.

---

[← back to the README](../README.md)
