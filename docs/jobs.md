# Jobs

A job is a schedule plus the exact agent invocation it produces.

## How a run behaves

A headless run is invisible until it finishes, and a run that stops to ask a
question is a lost run. Bermuda runs each job as an interactive Herdr agent
instead, so `herdr agent attach` drops you into one while it is happening. Three
things follow from that, and they are most of the design:

- **Park, never drop.** A timeout, a `blocked` agent, and "the agent exited
  without writing `result.json`" all park the run for a human rather than
  discarding it. The tab is left open, so the work is still there to look at.
- **One result channel.** Each run gets `BERMUDA_RUN_DIR`, and the `result.json`
  the agent writes there is the *only* authority on the outcome. Terminal output
  is archived as `transcript.txt` for humans and is never parsed — an agent's
  narration of its own success is the least reliable artifact in the system.
- **A workspace Bermuda owns.** Runs live in a workspace Bermuda created,
  identified by the id recorded in `~/.bermuda/workspace.json` — never by being
  called Bermuda, which may be a name you already used. A space Bermuda did not
  make is never adopted, so your own session is never touched.

Every job has two identifiers. The **id** is the handle you type
(`bermuda job run bl-board-review`) and is used for agent names and run
directories, so it stays slug-shaped and stable. The **name** is free text for
humans, and is what the board lists. Both are unique: `job add` refuses an id
that already exists, and no two jobs may share a name, since the board
identifies jobs by name.

```bash
bermuda job add --id daily-brief --name "Daily brief" \
  --prompt 'Summarize the day.' --cron '0 7 * * *' \
  --model sonnet --permission-mode acceptEdits \
  --add-dir /path/one,/path/two --timeout 20m --favorite

bermuda job add --id oneshot  --prompt '...' --at '2026-07-26 09:00'
bermuda job add --id hourly   --prompt '...' --interval 1h --persistent

bermuda job add --id promo --prompt '...' --tags marketing,daily

bermuda job list
bermuda job list --tag daily        # only jobs carrying a tag
bermuda job list --all             # include finished one-shots
bermuda job prune                  # list finished one-shots, delete nothing
bermuda job prune --yes            # actually delete them; their runs are kept
bermuda job show <id>              # config, resolved argv, prompt, run history
bermuda job edit <id> --model opus # only the flags you pass are changed
bermuda job pause <id> / resume <id>
bermuda job run <id>               # run now
bermuda run list [--state parked] [--json]
bermuda run show <run-id>          # artifacts: prompt.md, transcript.txt, result.json
bermuda usage [--since 24h]        # token totals per job, newest first
```

Every run records what it cost. Input, output, cache-read and cache-creation
tokens are stored separately because they are billed differently, alongside the
model the run actually used. The numbers come from the agent's own session
transcript, correlated by the run directory Bermuda names in its prompt — never
by picking the newest session, which would mis-charge concurrent agents and
persistent jobs. A transcript that cannot be read leaves the counts at zero: a
run keeps its real outcome regardless, since this is bookkeeping, not the work.

Jobs run with permission checks disabled by default. A scheduled job is
unattended, so a permission prompt has nobody to answer it: the run would park
until a human noticed, which is worse than useless for work meant to happen at
04:00. Pass `--skip-permissions=false` for a job you intend to supervise.

Every job names a model. Leaving `--model` unset resolves to `sonnet` rather
than to whatever the agent happens to default to, so a schedule's cost and
capability cannot change underneath it without the job changing.

Schedules: `manual`, `--cron`, `--interval`, `--at` (one-shot, disables itself
after it runs). `--catchup` decides what happens to fires missed while nothing
was running:

| policy | after six missed hourly fires |
|--------|-------------------------------|
| `latest` (default) | one run, for the window as a whole |
| `all` | six runs, in series, one after another |
| `skip` | nothing — the window is gone |

A fire less than a minute old counts as current rather than missed, so `skip`
still runs the fire the scheduler is standing on. `all` replays in series under
one claim, so a backlog cannot stack up parallel agents, and replay is bounded
at 100 fires however long the downtime was.

A one-shot whose most recent run came out `done` is finished: it can never fire
again, so both the board and `job list` put it away, and both say how many they
withheld (`3 finished hidden`) rather than letting it vanish. `F` on the board
and `--all` on the command line bring them back, marked `(finished)`. Nothing
else is hidden: a parked one-shot still wants a human, a failed one must be
noticed, one that has never run is still pending, and a recurring job is never
finished whatever its last run says. `bermuda job prune` clears them, and lists
what it would delete without deleting anything until `--yes`. Deleting a job
keeps its runs, so pruning loses the schedule, not the record of what happened.

`--persistent` reuses one long-lived agent per job instead of starting a fresh
one each run, skipping tab and startup cost. The agent's context is cleared
with `/clear` before every run, so runs stay independent.

The jobs tab fills the space to the right of the table with an inspector for
the selected job: state, schedule, when it next fires, model, timeout, tags,
working directory, permissions, and how the last run went. It is a summary —
the full record, the prompt, and the run history are on the detail page. The
panel is dropped when the pane is too narrow to render it legibly.

Text too long for its column scrolls, the way a station board scrolls a
destination that does not fit, rather than being cut off. An ellipsis says
something was hidden; scrolling says what it was. Every overflowing cell shares
one frame counter, so the board scrolls in step instead of each cell drifting on
its own phase.

## Editing from the board

Jobs are fully editable in the TUI — no CLI round trip needed.

| key | action |
|-----|--------|
| `n` | new job (id is derived from the name) |
| `e` | edit the selected job |
| `D` | delete the job (run history is kept) |
| `l` `→` `enter` | descend: jobs → job → agent, or runs → run → its job |
| `h` `←` `esc` | ascend one level |
| `tab` | cycle the THREADS / JOBS / RUNS tabs |
| `space` | toggle a yes/no field |
| `ctrl+s` | save (in a text field: commit the field; otherwise: save the job) |

Edits are made against a copy and only reach the store on save, so abandoning
an edit cannot leave a job half-changed, and a run scheduled mid-edit still
uses the old definition. Cross-field rules are checked on save: a job set to
`cron` with no expression is rejected rather than stored as something that can
never fire.

---

[← back to the README](../README.md)
