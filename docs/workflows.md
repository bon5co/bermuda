# Workflows

A job is one prompt to one agent. Anything with more than one step is carried in
that agent's head: it has to remember to pull first, to verify after, to hand
its output to the next stage. It usually does. The failures are not dramatic —
the agent does four of five steps and reports success.

A **workflow** is a job whose steps are declared instead of remembered:

```
AgentA("prompt") -> AgentB("prompt") -> AgentC("prompt")
```

declared once, and *ensured* to happen in that order. What that buys over one
prompt saying "then do B, then do C":

- **The harness makes the call.** B is launched by bermuda, not by A remembering
  to hand off. A cannot skip B, inline B, or decide B was already covered.
- **B runs even if A says B's work is done.** An agent's report of its own work
  is the least reliable artifact in the system, and it is not an input to
  whether B runs.
- **If A dies, the workflow parks at A.** B never runs on a lie.
- **Each agent step is its own process**, with its own `BERMUDA_STEP_DIR` and its
  own `result.json` — the same contract every run already uses.

Steps are declared as JSON and given to `job add` or `job edit`:

```json
[
  {"id": "sync",   "run":   "~/dotfiles/scripts/wt.sh workflow/shorts ."},
  {"id": "author", "agent": "Write five N5 stories into stories/n5.json.",
                   "model": "opus", "effort": "high"},
  {"id": "verify", "agent": "Review only the stories the previous step added.",
                   "subagent": "cavecrew-reviewer"}
]
```

```bash
bermuda job add --id shorts --name "Shorts batch" --steps steps.json --cron '0 7 * * 1'
bermuda workflow run shorts      # or bermuda job run shorts, or the schedule
bermuda workflow status <run>    # per-step outcome, duration, and note
bermuda workflow resume <run>    # restart at the step that parked
```

A step is either an `agent` (a prompt) or a `run` (a shell command), never both.
`run:` steps have no agent at all, and that is the cheapest reliability win
here: most "the agent forgot" incidents are a shell command a model was asked to
remember instead of a step the harness executes.

An agent step takes the job's agent settings as defaults and may override
`model`, `effort`, `kind`, and `subagent` — a two-line mechanical edit should
not burn the model a judgement-heavy step needs, and a reviewing step should be
able to wear a different charter than the step that wrote the thing. **No step
may run on haiku**, whether it names the model itself or inherits it from the
job: the house floor is sonnet, and a workflow is exactly where a cheap model
quietly does four of five steps. `run:` steps take none of this configuration
and are refused if they try, since a step that looks configured but is not is
worse than one that says so.

Failure is the point of the feature. A step that reports failure, or that ends
without writing `result.json`, **parks the workflow at that step**, and the
steps after it never start. Everything the completed steps wrote stays on disk,
so `bermuda workflow resume` picks up at the failed step and does not redo the
expensive ones — completion is decided by a successful `result.json` in the
step's directory, not by a database row, so a resume works long after the
process that ran the first attempt is gone. A retried step's stale result is
removed before it starts, so an agent that dies cannot be classified by the last
attempt's verdict.

On the board a workflow run is one row — it was launched once — reading
`2/4 · verify`: how many steps are done, of how many, and which one it is on.
Space opens the steps under it with each one's status and duration.

Out of scope for now, and deliberately: `{{step.key}}` templating between steps,
the `expect:` assertion vocabulary, `check:` commands, `gate: human`, `fresh:`,
parallelism, and branching. Series only — the failures this was built from were
sequencing failures, not throughput ones.

---

[← back to the README](../README.md)
