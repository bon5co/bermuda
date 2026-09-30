# Run-settled hooks

A run can finish in the daemon, on the board, in `job run`, in `flow run`, or
while an old run is being reconciled. None of those callers is a good place to
wait for somebody else's program. They all write the same run row, so Bermuda
records a `run.settled` event beside that row in one SQLite transaction. The
daemon delivers it later. If the daemon is down, the event waits for it.

Put an executable at `$BERMUDA_STATE_DIR/hooks/run-settled` (normally
`~/.bermuda/hooks/run-settled`). On Windows, Bermuda looks for
`run-settled.exe`, then `.cmd`, then `.bat`; command scripts run through
`cmd.exe`. There is one hook for every job. It can inspect `run.ref` and ignore
events it does not care about.

The hook receives one versioned JSON object on stdin. Its working directory is
the state directory. The same identifiers are available as environment
variables: `BERMUDA_EVENT_ID`, `BERMUDA_RUN_ID`, `BERMUDA_JOB_ID`,
`BERMUDA_RUN_OUTCOME`, `BERMUDA_PARK_REASON`, `BERMUDA_REF`, and
`BERMUDA_RUN_DIR`. The object includes the run, its settlement number, the
previous *settled* outcome, and the parsed root `result.json` when one was
readable. A flow normally has `result: null`: its authoritative results are in
step directories. Bermuda never judges an outcome from the transcript.

Each event payload is a snapshot. Changing the end time of an already settled
run without changing its outcome or whether it has an end time does not emit a
new event, and redelivery retains the original timestamp.

For a local log:

```sh
state_dir=${BERMUDA_STATE_DIR:-"$HOME/.bermuda"}
mkdir -p "$state_dir/hooks"
cat > "$state_dir/hooks/run-settled" <<'SH'
#!/bin/sh
cat >> "$BERMUDA_STATE_DIR/hooks/settlements.jsonl"
SH
chmod +x "$state_dir/hooks/run-settled"
```

For a webhook, put this body in the executable instead:

```sh
#!/bin/sh
curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  --data-binary @- https://daemon.example/run-events
```

An exit code of zero acknowledges the event. A nonzero exit or a 30-second
timeout retries after 30 seconds, 2 minutes, 10 minutes, then 1 hour; five
failed calls leave the event dead. Any undelivered event, including a dead one,
holds later settlements of the same run until it is redelivered and delivered;
other runs keep moving. Hook output goes to
`$BERMUDA_STATE_DIR/hooks/run-settled.log`, headed by event ID. Missing or
non-executable hooks are recorded as `skipped` with `no hook`; they do not
quietly retry when a hook is installed later.

```sh
bermuda hook status                 # pending, retrying, dead, and skipped
bermuda hook redeliver 42           # requeue one event, including a skipped one
bermuda hook redeliver --dead       # requeue all dead events
```

Redelivery keeps the event ID and frozen payload. Delivery is at least once
when a hook is installed: if the daemon crashes after the hook succeeds but
before it records that success, it calls the hook again. A receiver can use
`event_id` to deduplicate. The hook reports an outcome to another system; its
exit status never changes the run. `result.json` remains the only authority on
what the run did, and a hook failure never stops scheduling.

---

[← back to the README](../README.md)
