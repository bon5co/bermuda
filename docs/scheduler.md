# The scheduler

The daemon evaluates schedules on a short tick and launches whatever is due.

```bash
bermuda daemon --tick 5s --concurrency 4   # foreground
bermuda daemon --detach                    # background, used by the plugin hook
```

It is hosted by the plugin's startup hook, which fires again on live handoff,
so the daemon takes an advisory file lock: **only one ever runs**. A second
start prints the holder's PID and exits cleanly. The lock is held by the kernel
and released on exit or crash, so a killed daemon cannot wedge it.

Because the runner *is* Herdr, a scheduler that lives with the Herdr server
loses nothing: with no server there is nothing to run jobs on. Fires missed
while it was down are resolved by each job's `--catchup` policy on next start.

Overlap is skipped by default: a job whose run outlasts its interval will not
stack up a second agent. Catchup replay is bounded so long downtime cannot
enqueue thousands of runs.

Shutdown lets running jobs finish. The signal stops the sweep and stops a
backlog part-way through, but a run already started keeps its own context and
its own timeout — killing an agent mid-turn is what produces a run that is
neither done nor parked.

### Stopping it

```bash
bermuda stop     # stops the pair, and remembers that you did
bermuda start    # undoes it
```

The stop is written down because it has to outlive everything that would undo
it: the daemon and the sentinel revive each other within five seconds, the
plugin hook starts them on every Herdr start, and the board starts them on its
refresh tick. Only `bermuda start` clears it.

The board watches its own binary and re-execs when it changes, so a pane left
open always shows the current build.

## Keeping the scheduler alive

Bermuda runs two processes that watch each other:

- **daemon** — evaluates schedules and launches runs
- **sentinel** — does nothing but watch the daemon

Each holds its own lock and revives the other when that lock goes free, so
killing either one leaves a survivor that restores the pair. The sentinel is
deliberately tiny: it opens no database and evaluates no schedules, so the bugs
that could take down the scheduler are absent from the process whose job is to
restart it.

Three independent triggers start them, and all of them are no-ops when the pair
is already up, because the locks decide:

1. the plugin startup hook, on every Herdr start and live handoff
2. the board, on its refresh tick
3. either process, watching the other every 5s

This is deliberately self-contained: no unit files, no system supervisor,
nothing to install beyond the binary. The case a system supervisor would add —
nothing running at all — is covered by the startup hook, and is not really a
gap: bermuda runs jobs *inside* Herdr, so with no Herdr there is nothing for a
scheduler to do.

The board header shows **scheduler stopped** whenever no daemon holds the lock.

## Herdr integration

Bermuda confines itself to what it creates. It never changes Herdr's default
behaviour, its settings, or any pane it did not open:

- runs live in their own `Bermuda` workspace, one tab per run
- run panes are labelled (`display_agent`, `title`, and tokens carrying the job
  and run ids) so a run is identifiable rather than showing as a bare `claude`
- nothing else in Herdr is touched: no status vocabulary is redefined, no agent
  view is registered, no other pane is modified

Herdr does have an agent-view API that can filter the agents list, but it keeps
a *single* view rather than a set of tabs — setting one replaces the whole list
and hides every agent that is not a bermuda run. That is too invasive for a
scheduler to do, so bermuda does not use it.

---

[← back to the README](../README.md)
