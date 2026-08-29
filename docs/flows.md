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

- **The harness makes the call.** B is launched by Bermuda, not by A remembering
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

## A run gets its own space, and the steps share its thread

One line travels down the chain. Everything else a step learned dies with the
process — which is how the fourth step comes to rediscover that the migration
already ran, that the arm64 failure is in the C shim, that the credentials in the
README are stale. Each rediscovery costs an agent turn, and some of them fail
instead.

A team of people does not work that way: they say what they found where the
others can hear it, and the record outlives whoever was in the room. Bermuda
already has that shape — [a thread belongs to a herdr
workspace](threads.md#the-workspace-is-the-thread), and every agent in that space
is in the thread without joining it. So **a flow run opens a space of its own**,
every step's tab is created inside it, and all of them read and write one thread:

```
run  20260730T101500Z-triage
job  triage
space  FLOWS:triage:a1B2c3
thread  flows-triage-a1b2c3
```

```
$ bermuda thread log --thread flows-triage-a1b2c3
21:57  note  triage (20260730T101500Z-triage)  flow triage, run …: 3 steps — assess then patch then verify
21:58  note  triage-assess (assess)            the arm64 failure is in the C shim, not in the Go
22:04  note  triage-patch (patch)              the fixture DB was already migrated; do not run it again
22:09  note  triage (20260730T101500Z-triage)  flow triage finished: 3/3 steps ok
```

Every agent step is told this in its prompt, next to the result contract, because
a step is a fresh agent that has read nothing and a shared thread nobody mentions
is a thread nobody writes to. The instruction is about *what* to post, not only
how: findings, not status. `run:` steps get `$BERMUDA_THREAD` in their
environment for the same reason — a test runner that knows which suite is flaky
is exactly the finding a later step would otherwise go looking for.

The chain still carries the verdict, and `{{previous}}` is still one published
line rather than a transcript. The thread carries the evidence.

- **The space is per run, not per flow.** Two runs of one flow are two
  investigations, and merging them would hand the second run's second step the
  first run's stale conclusions.
- **A resume lands in the same conversation.** The run records its space and its
  thread, so the half that runs tomorrow writes where the half that ran today
  did. If the space has gone in the meantime a new one is opened and the old
  thread id is named on stderr — the earlier findings are still readable, and
  nothing pretends the record is continuous when it is not.
- **A finished flow gives its space back**, the way a finished run gives its tab
  back; a parked one leaves it open, because a human has to look at it. Closing
  is not deleting: `bermuda thread log --thread <id>` reads every word of a
  closed thread, and `bermuda flow status` prints the command.
- **A parked run says so in its own room.** The thread gets the ending — `flow
  triage parked: 1/3 steps, parked at verify (loop_stuck) — resume with: bermuda
  flow resume <run>` — the space is relabelled `FLOWS:triage:a1B2c3 · parked
  verify (loop_stuck)`, and one tab opens on the run directory where
  `result.json`, `result.attempt-N.json` and `output.txt` are. A flow of `run:`
  steps has no agent pane at all, so without that tab the room is a blank shell
  and the verdict lives only in the database.
- **Closing the space is the acknowledgement.** Bermuda never closes a parked
  run's space, because "somebody has seen this" is not a thing the harness can
  know. Herdr refuses to close the last tab in a workspace, so the room outlives
  every tab in it and there is exactly one gesture that ends it. A resume takes
  the verdict back off the label, so a room that says parked is parked.
- **No herdr, no space, and the flow still runs.** The thread is how steps
  compare notes; it is not what makes them run in order. Every failure on this
  path degrades to what happened before the feature existed, with one line on
  stderr.

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

A step may also name the checklist item it is, with `check:`. Both kinds take it,
because it configures the harness's bookkeeping rather than an agent:

```yaml
  - id: build
    check: make the change
    agent: "{{previous}} — make the change."
```

Run the flow with `--check <list>` and every step that names an item gets one on
that page before the first step starts, ticked as each reports ok. That is what
keeps the declared sequence and the page a human is reading one object rather
than two that drift — a flow's own step state dies with the run.
→ [checklists](checklists.md)

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

## Handing the work back

Parking is right when a human has to decide something. It is useless when the
last step is a reviewer that found a real defect and the step that can fix it is
the one three above it: the flow stops, and the only thing anyone does with it is
say "try again". A step can declare that itself.

```yaml
steps:
  - id: implement
    agent: "{{input}} — write the fix."
  - id: verify
    agent: Adversarially review the diff. Fail if any finding survives.
    on_fail:
      goto: implement
      max_loops: 2
```

**The edge points at the maker, not at the checker.** A `retries: 2` on `verify`
would re-read the same unchanged diff twice and reject it twice — the step that
failed is the one checking, and the step that must run again is the one that
produced what it checked. `goto` therefore has to name a step declared *before*
this one; an edge pointing forward is a branch, and a flow is a series.

What happens when the edge is taken:

- **The whole span re-runs, not just the target.** Everything from `goto` through
  the step that failed has its `result.json` set aside, so the reuse path cannot
  hand the checker a verdict produced by an attempt that never happened. The
  rejected files are kept as `result.attempt-N.json` beside the ones that
  replaced them.
- **The retried step is told why it is running again.** `{{previous}}` becomes
  the rejection, and the prompt is prefixed with which step rejected it, on which
  attempt, and an instruction to read the run's thread first. A maker usually
  reads `{{input}}` and never mentions `{{previous}}`; re-running it with the
  prompt that produced the rejected work reliably produces the same work again.
- **Only a verdict loops.** A step that ended without writing `result.json` — it
  died, it timed out, it is waiting on a human — parks the way it always did.
  Rewriting code because the machine fell over is a heal loop against something
  that was never wrong with the code.
- **It stops twice over.** `max_loops` (default 1, ceiling 8) bounds the
  attempts, and a verdict identical to the previous one parks immediately: the
  retry changed nothing the checker can see, and the loops left would spend the
  same tokens to be told the same thing. The park reasons are `loop_exhausted`
  and `loop_stuck`, and both are parks — the attempts are in the thread and the
  rejected verdicts are on disk.
- **The budget survives the resume.** What each edge has spent is written to
  `loops.json` in the run directory, beside the results, for the same reason
  completion lives there: a resume is a different process on a different day.
  A bound held in memory is not a bound — `flow resume` would hand back a full
  allowance every time, and the thing calling resume is not always a human who
  looked. A self-heal job that unparks whatever broke overnight would refill an
  exhausted loop every morning, forever, at the cost of a whole flow each time.
  `bermuda flow resume <run> --reset-loops` is how somebody who fixed the
  underlying problem asks for the allowance back, out loud.
- **Each attempt is its own agent.** The retry runs under its own run id, so it
  cannot be handed the agent that produced the work it is meant to redo, and the
  step's row on the board says which attempt it is on.

**Everything inside the loop must be re-runnable.** A step between the target and
the checker runs again every time the edge is taken, so pushing, deploying,
publishing and opening PRs belong strictly *after* the checker passes. A loop
that pushed on attempt one has to clean up after itself on attempt two, and that
is where this goes bad.

One thing the harness cannot tell yet: "the reviewer found a real defect" and
"the database was down" are both a step reporting failure. A step that fails for
an environmental reason will be looped against, up to its bound, before it parks.
Until `result.json` carries a category, keep `on_fail` on steps whose failure
means the work is wrong.

## On the board

Flows have their own tab: what is callable, what each takes, and when it last
ran. `enter` runs one, asking for the input first. `u` unparks a parked run — the
board could always *show* a parked flow and never act on it, which was the
sharper of the two gaps.

The tab fills the space to the right of the table with an inspector for the
selected flow: its file path, what it is about, what input it takes, the steps
in order with the model each agent step runs on, whether the permission bypass
is on, and how the last run went. The step list is the point of it — the table
has room for a count, and `4` says nothing about whether the fourth step is the
one that verifies. A long sequence is cut with the rest counted out loud, and a
flow that would not parse gets the parser's own complaint here rather than an
empty panel. As on the jobs tab, the panel is dropped when the pane is too
narrow to render it legibly.

A flow *run* stays one row in RUNS, because it was launched once, reading
`2/4 · verify`: how many steps are done, of how many, and which one it is on.
Space opens the steps under it.

## Deliberately out of scope

Templating beyond `{{input}}` and `{{previous}}`, the `expect:` assertion
vocabulary, `check:` commands, `gate: human`, `fresh:`, parallelism, and
branching. Series only — the failures this was built from were sequencing
failures, not throughput ones. `on_fail` is not an exception to that: it is one
edge backward along the same line, which is why it may only point at a step
already declared.

**Still unbounded: wall clock and tokens.** The loop count is capped and now
survives a resume, so a misconfigured flow cannot cycle forever. What it can do
is take a long time doing it: the job timeout applies *per step*, so one run is
bounded by `(steps + loopbacks) × timeout`, and eight loops of a forty-minute
agent step is a night. There is no deadline that parks a run for spending too
long, only one that parks a step.

Also out for now: an escalation ladder on the retry (`escalate: {model: opus}`),
failure classes in `result.json`, and per-attempt counters on the board's own
columns. The first two are what turn a bounded retry into a heal that gets
smarter; the counters are what stop a healing run from looking hung.

## The overwatch

Every step of a flow is a fresh agent that has read nothing but its own prompt.
That is the point — it is what stops step four inheriting step one's confusion —
and it leaves nobody holding the shape of the run. So when a step failed, the
harness could only ever take the decision available from outside: park, and wait
for a person. That is right when nobody knows why the step failed, and a wasted
night when the answer is legible from two steps up.

The overwatch is the reader that has the whole thing. It is handed the flow as
declared, every step's outcome and note, and the artifacts of whatever just went
wrong, and it answers one question: what should this run do now.

**Every flow that runs agents has one, declared or not.** A flow with no
`overwatch:` block is not unsupervised; it is supervised on the defaults.

```yaml
overwatch:
  model: opus            # the decision is usually harder than the steps it judges
  effort: high
  watch: on_trouble      # or every_step
  budget: 3              # decisions per run
  timeout: 10m           # one consult
  allow: [retry, goto, park, abort]
  brief: |
    The deploy step is not safe to retry. Park instead.
```

### When it is asked

`on_trouble`, the default, consults it only where the run would otherwise park:
a step failed, a step died, or a declared `on_fail` edge ran out. **A run where
nothing goes wrong never starts one**, which is most runs, and is why this
costs nothing to leave on.

`every_step` consults it after every step, successful or not. It buys a reader
on every result and costs one agent call per step, so a flow declares it rather
than inheriting it.

### What it may decide

It writes `decision.json` in its own directory:

```json
{"decision": "goto", "step": "write", "why": "the verifier is rejecting a fixture the writer never saw"}
```

| decision | what happens |
|---|---|
| `retry` | the same step runs again, unchanged — for a transient |
| `goto` | the run goes back to a named earlier step, as a declared edge would |
| `park` | stop here, resumable. The default, and the expected answer |
| `abort` | stop, and say the run is not worth resuming |
| `continue` | nothing needs doing (the answer to an `every_step` consult) |
| `skip` | accept the failed step and carry on — **never available unless `allow` names it** |

### The three things that keep this honest

**A declared `on_fail` edge wins.** It is explicit, free, and the author's. The
overwatch is consulted where the flow would park — including when an edge has
run out, which is the most valuable moment it gets, because "this loop is not
converging" and "this loop was pointed at the wrong step" look identical from
inside the loop.

**`skip` is not in the default allow-list.** Accepting a failed step is the one
decision that ends what a flow is for: B must not run on A's unverified claim.
A flow that wants an agent able to take it says so in the file, where a reviewer
sees it — and `bermuda flow list` prints `+skip` in its WATCH column so it is
visible without opening anything.

**Ambiguity parks.** No decision file, unreadable JSON, a verb the flow does not
allow, a `goto` naming a step that does not exist or has not run yet — every one
of them parks the run with the reason on the record. Nothing resolves towards
carrying on.

### How a decision actually heals

The steps of one run share a workspace and its thread, and the overwatch posts
to the same thread. So a step re-run after a `goto` is not simply repeated: it
reads what the overwatch concluded before it starts.

Observed on the first live run of this feature — a `write` step whose prompt
said to write `goodbye`, a shell `verify` that grepped for `hello`, and an
overwatch that chose `goto write`. The second attempt wrote `hello` and said so
in its own result: *"overriding literal prompt text, to match the verify
expectation from the thread log"*. That is the mechanism the feature turns on,
and it is worth knowing it exists: **the overwatch's reasoning carries weight
against a step's own prompt.** A flow whose steps must follow their prompts
exactly should say so in the overwatch `brief`.

The overwatch itself is told to decide and not to do: it has the same tools the
steps had, and a fix it applied itself would be work no step made, no step
reported, and no reviewer saw.

### What bounds it

`budget` (default 3, ceiling 12) is how many decisions one run gets, and it is
recorded in the run directory so a **resume does not hand it back** — the thing
that resumes a parked run is not always a human deciding it is worth another go.
`timeout` (default 10m) bounds one consult: a job's own timeout defaults to
waiting for ever, and a supervisor that can hang the run it supervises is worse
than no supervisor.

A flow of nothing but `run:` steps is not given an overwatch unless it declares
one. Its failures are exit codes, and summoning a model to interpret
`test -f gate` spends an agent on a boolean while making a deliberately
deterministic flow depend on one.

The verb used to be `workflow`. It still dispatches, with a line on stderr, so a
cron entry or launcher script written before the rename keeps running.

---

[← back to the README](../README.md)
