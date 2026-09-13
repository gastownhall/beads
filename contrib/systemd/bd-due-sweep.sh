#!/usr/bin/env bash
# One timer body: run the due sweep in one workspace and stamp what it did.
#
# Beads fire lazily on ready-work reads, which is a latency FLOOR, not a clock:
# a workspace nobody reads never fires anything. This script is the clock — a
# systemd timer runs it on a fixed cadence and it calls `bd due sweep --json`.
#
# It stays deliberately dumb: take a lock, run the sweep under a timeout, write
# the report and a heartbeat, hand the summary to an optional publish hook. Any
# decision about what to DO with a due bead belongs to whatever consumes the
# rail, not here.
set -euo pipefail

WORKSPACE=${BD_DUE_SWEEP_WORKSPACE:-}
BD=${BD_DUE_SWEEP_BD:-bd}
TIMEOUT=${BD_DUE_SWEEP_TIMEOUT:-120}
STATE=${BD_DUE_SWEEP_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/bd-due-sweep}
# Optional hook, called as: "$PUBLISH" "<summary line>" "<path to report.json>"
PUBLISH=${BD_DUE_SWEEP_PUBLISH:-}
# Deliver queued notify rows in the same tick. Set BD_DUE_SWEEP_DRAIN=0 to let
# a separate timer own delivery.
DRAIN=${BD_DUE_SWEEP_DRAIN:-1}
DRAIN_EXEC=${BD_DUE_SWEEP_DRAIN_EXEC:-}
DRAIN_LIMIT=${BD_DUE_SWEEP_DRAIN_LIMIT:-50}

if [ -z "$WORKSPACE" ]; then
    echo "bd-due-sweep: BD_DUE_SWEEP_WORKSPACE is unset" >&2
    exit 2
fi
if [ ! -d "$WORKSPACE" ]; then
    echo "bd-due-sweep: workspace $WORKSPACE is not a directory" >&2
    exit 2
fi

mkdir -p "$STATE"
LOCK="$STATE/sweep.lock"
HEARTBEAT="$STATE/last-sweep"
REPORT_JSON="$STATE/last-report.json"

# A sweep that outruns its interval must not stack: the next tick skips rather
# than running a second sweep against the same workspace.
exec 9>"$LOCK"
if ! flock -n 9; then
    echo "bd-due-sweep: previous sweep still running; skipping this tick"
    exit 0
fi

report=$(cd "$WORKSPACE" && timeout "$TIMEOUT" "$BD" due sweep --json)
printf '%s\n' "$report" > "$REPORT_JSON.tmp"
mv -f "$REPORT_JSON.tmp" "$REPORT_JSON"

summary=$(printf '%s' "$report" | sed -n 's/.*"summary"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
if [ -z "$summary" ]; then
    echo "bd-due-sweep: sweep returned no summary" >&2
    exit 1
fi
echo "bd-due-sweep: $summary"

# Delivery runs in the sweep's own tick so there is one timer, not two. A seat
# whose transport is down keeps its rows pending and is retried next tick, so a
# drain failure is never fatal here.
if [ "$DRAIN" != "0" ]; then
    if [ -n "$DRAIN_EXEC" ]; then
        (cd "$WORKSPACE" && "$BD" notify drain --all --limit "$DRAIN_LIMIT" --exec "$DRAIN_EXEC") || \
            echo "bd-due-sweep: notify drain had failures (non-fatal)" >&2
    else
        (cd "$WORKSPACE" && "$BD" notify drain --all --limit "$DRAIN_LIMIT") || \
            echo "bd-due-sweep: notify drain had failures (non-fatal)" >&2
    fi
fi

if [ -n "$PUBLISH" ]; then
    "$PUBLISH" "$summary" "$REPORT_JSON" || \
        echo "bd-due-sweep: publish hook failed (non-fatal)" >&2
fi

# The heartbeat is written LAST, so its mtime means "a sweep completed", not
# "a sweep started". A monitor on another host alerts when it goes stale.
printf '%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$summary" > "$HEARTBEAT.tmp"
mv -f "$HEARTBEAT.tmp" "$HEARTBEAT"
