#!/usr/bin/env bash
# Move bermuda's state from the old XDG location to ~/.bermuda.
#
#   scripts/migrate-state-dir.sh [--dry-run]
#
# Run once, by hand, after upgrading to a binary that looks in ~/.bermuda.
# There is no automatic migration inside the binary: this is a one-time move
# for the machines that ran the older layout, and a move done by a daemon that
# might be one of several processes holding the database open is a worse idea
# than a move done deliberately with everything stopped.
#
# What it does:
#   1. refuses if both paths hold real state, so nothing is merged
#   2. stops the daemon/sentinel pair, which revive each other, so both must
#      go down together and stay down for longer than their 5s watch interval
#   3. holds the new path's locks, so anything an open board pane revives
#      during the move exits instead of writing there
#   4. copies the state across, discarding the empty store an upgraded binary
#      leaves behind, and removes the old directory
#   5. starts the pair again against the new location
#
# Open board panes keep a handle on the old database file and go on writing to
# it, and that file is gone at the end of this. Restart them after this.
set -euo pipefail

DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1

OLD="${XDG_STATE_HOME:-$HOME/.local/state}/bermuda"
NEW="$HOME/.bermuda"
BERMUDA="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin/bermuda"

run() {
    if [ "$DRY" = 1 ]; then
        echo "would: $*"
    else
        "$@"
    fi
}

has_db() { [ -e "$1/bermuda.db" ]; }

# is_running reports a pid that is actually running.
#
# Not `kill -0`: bermuda's roles are spawned by board panes that never wait on
# them, so a stopped daemon sits as a zombie until its parent exits or dies.
# A zombie answers kill -0 and has a /proc entry, and is not running -- reading
# it as alive made this script give up on a pair that had already stopped.
is_running() {
    local pid="$1" raw rest
    [ -n "$pid" ] || return 1
    raw="$(cat "/proc/$pid/stat" 2>/dev/null || true)"
    [ -n "$raw" ] || return 1
    # The command name sits in parentheses and may contain spaces; state is the
    # first field after it.
    rest="${raw##*) }"
    [ "${rest%% *}" != "Z" ]
}

# empty_upgrade_db reports a database that exists only because the binary was
# replaced. Anything at all -- a board pane reviving the pair, a stray command
# -- makes a new-path binary create an empty store, and refusing to migrate on
# account of that file would strand the real one.
empty_upgrade_db() {
    [ -x "$BERMUDA" ] || return 1
    [ -d "$1/runs" ] && return 1
    [ "$(BERMUDA_STATE_DIR="$1" "$BERMUDA" job list 2>/dev/null)" = "no jobs" ]
}

# stop_pair takes down the daemon and sentinel holding locks in a directory.
#
# Both at once, deliberately: each revives the other when it sees the peer's
# lock go free, so signalling them one at a time only produces a replacement.
stop_pair() {
    local dir="$1" pids=() role lock pid
    for role in daemon sentinel; do
        lock="$dir/$role.lock"
        [ -f "$lock" ] || continue
        pid="$(cat "$lock" 2>/dev/null || true)"
        is_running "$pid" && pids+=("$pid")
    done
    [ "${#pids[@]}" -gt 0 ] || return 0

    echo "migrate: stopping ${pids[*]} ($dir)"
    run kill "${pids[@]}"
    # Longer than watchInterval, so a survivor that already spawned a
    # replacement has finished doing it and that replacement is visible here.
    run sleep 8

    # A dry run killed nothing, so checking would only report on processes it
    # deliberately left alone.
    [ "$DRY" = 0 ] || return 0
    for pid in "${pids[@]}"; do
        if is_running "$pid"; then
            echo "migrate: pid $pid did not stop" >&2
            return 1
        fi
    done
    # A replacement may already have been spawned by a board pane. Report it
    # rather than deciding here: for the directory being moved out of that is
    # fatal, and for the one being moved into it is what the lock is for.
    for role in daemon sentinel; do
        lock="$dir/$role.lock"
        [ -f "$lock" ] || continue
        pid="$(cat "$lock" 2>/dev/null || true)"
        if is_running "$pid"; then
            echo "migrate: $role in $dir came back as pid $pid"
            return 1
        fi
    done
}

# grab_locks takes the new path's daemon and sentinel locks on fds 9 and 8.
#
# A blocking flock is no good here: the process holding the lock is a daemon,
# and a daemon does not let go. The lock only comes free when it is killed --
# and a board pane notices the gap and starts another within a poll or two. So
# kill the holders and then spend that gap asking for the lock in a tight loop,
# which gets there first because it is already running while the replacement
# still has to be exec'd. Losing the gap costs one more round.
grab_locks() {
    local attempt i role lock pid pids
    for attempt in $(seq 15); do
        pids=()
        for role in daemon sentinel; do
            lock="$NEW/$role.lock"
            [ -f "$lock" ] || continue
            pid="$(cat "$lock" 2>/dev/null || true)"
            is_running "$pid" && pids+=("$pid")
        done
        [ "${#pids[@]}" -gt 0 ] && kill "${pids[@]}" 2>/dev/null
        for i in $(seq 40); do
            if flock -n 9 && flock -n 8; then
                # A departing daemon removes its lock file, and the next one
                # creates a fresh file at the same path. Holding a lock on the
                # unlinked inode would guard nothing, so check identity and go
                # round again on the file that is actually there now.
                if same_file 9 "$NEW/daemon.lock" && same_file 8 "$NEW/sentinel.lock"; then
                    return 0
                fi
                exec 9<>"$NEW/daemon.lock"
                exec 8<>"$NEW/sentinel.lock"
            fi
            sleep 0.05
        done
    done
    return 1
}

# same_file reports whether an open fd still refers to the named path.
same_file() {
    local fd="$1" path="$2" a b
    a="$(stat -Lc %i "/proc/self/fd/$fd" 2>/dev/null || true)"
    b="$(stat -c %i "$path" 2>/dev/null || true)"
    [ -n "$a" ] && [ "$a" = "$b" ]
}

if has_db "$NEW" && ! has_db "$OLD"; then
    echo "migrate: already at $NEW, nothing to do"
    exit 0
fi

if has_db "$NEW" && ! empty_upgrade_db "$NEW"; then
    echo "migrate: both $OLD and $NEW hold a database." >&2
    echo "migrate: refusing to merge -- keep the one you want and remove the other." >&2
    exit 1
fi

if ! has_db "$OLD"; then
    echo "migrate: no state at $OLD, nothing to move"
    exit 0
fi

if ! stop_pair "$OLD"; then
    echo "migrate: something keeps restarting bermuda against $OLD; re-run" >&2
    exit 1
fi
# Stopping the pair is not enough to keep it stopped: an open board pane
# revives it whenever it polls and finds no daemon, and the revived pair runs
# the upgraded binary, so it recreates the new path seconds after the old one
# is cleared. Waiting that out is a race the script loses.
#
# So take the new path's two locks and hold them for the whole move. A daemon
# or sentinel that starts meanwhile takes its lock before it opens anything,
# finds it held, prints "already running" and exits -- so nothing writes to the
# destination while it is being filled, and no board needs to be closed.
if [ "$DRY" = 1 ]; then
    echo "would: hold $NEW/daemon.lock and $NEW/sentinel.lock for the move"
    echo "would: copy $OLD into $NEW and remove $OLD"
else
    mkdir -p "$NEW"
    exec 9<>"$NEW/daemon.lock"
    exec 8<>"$NEW/sentinel.lock"
    if ! grab_locks; then
        echo "migrate: could not take the locks in $NEW -- close open board panes and re-run" >&2
        exit 1
    fi

    # Whatever an upgraded binary left here is an empty store, checked above,
    # and must not survive to be merged with the real one. The lock files stay:
    # they are the ones being held.
    find "$NEW" -mindepth 1 -maxdepth 1 ! -name '*.lock' -exec rm -rf {} +

    echo "migrate: $OLD -> $NEW"
    # Copy rather than mv: the destination has to exist already to hold its
    # locks, and mv into an existing directory would nest the old one inside.
    # The lock files are skipped so the held ones are not replaced.
    find "$OLD" -mindepth 1 -maxdepth 1 ! -name '*.lock' -exec cp -a {} "$NEW/" \;
    rm -rf "$OLD"

    # Release before starting the pair, or it would find its own locks taken.
    exec 9>&-
    exec 8>&-
fi

if [ -x "$BERMUDA" ]; then
    echo "migrate: starting the daemon pair against $NEW"
    run "$BERMUDA" ensure
else
    echo "migrate: $BERMUDA not built; start the pair yourself with 'bermuda ensure'"
fi

echo "migrate: done -- restart any open board panes"
