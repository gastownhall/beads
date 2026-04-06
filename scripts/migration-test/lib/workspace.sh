#!/bin/bash
# Workspace helpers — create isolated git repos, run bd commands, cleanup.

export BEADS_TEST_MODE="${BEADS_TEST_MODE:-1}"
export GIT_CONFIG_NOSYSTEM="${GIT_CONFIG_NOSYSTEM:-1}"

# Timeout for bd operations (seconds). Prevents hangs from dolt server
# startup, embedded engine locks, etc.
BD_OP_TIMEOUT="${BD_OP_TIMEOUT:-30}"

new_workspace() {
    local dir
    dir=$(mktemp -d /tmp/bd-migration-XXXXXX)
    git -C "$dir" init --quiet
    git -C "$dir" config user.name "migration-test"
    git -C "$dir" config user.email "test@beads.test"
    touch "$dir/.gitkeep"
    git -C "$dir" add .
    git -C "$dir" commit --quiet -m "initial"
    echo "$dir"
}

bd_in() {
    local ws="$1"
    local bin="$2"
    shift 2
    (cd "$ws" && timeout "$BD_OP_TIMEOUT" "$bin" "$@")
}

# Create an issue, returning just the ID on stdout.
# Tries --silent first, falls back to parsing output.
bd_create() {
    local ws="$1"
    local bin="$2"
    shift 2
    local id
    id=$(bd_in "$ws" "$bin" create --silent "$@" 2>/dev/null) && [ -n "$id" ] && echo "$id" && return 0
    id=$(bd_in "$ws" "$bin" create "$@" 2>&1 | grep -oP 'Created issue: \K\S+' || true)
    [ -n "$id" ] && echo "$id" && return 0
    return 1
}

stop_dolt_server() {
    local ws="$1"
    local pid=""
    for pidfile in "$ws/.beads/dolt-server.pid" "$ws/.beads/dolt-monitor.pid" "$ws/.beads/daemon.pid"; do
        if [ -f "$pidfile" ]; then
            pid=$(cat "$pidfile" 2>/dev/null) || true
            [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true
        fi
    done
    pkill -9 -f "$ws" 2>/dev/null || true
    sleep 1
    rm -f "$ws/.beads/bd.sock" "$ws/.beads/dolt-server.lock" 2>/dev/null || true
}

cleanup_workspace() {
    local ws="$1"
    stop_dolt_server "$ws"
    rm -rf "$ws"
}
