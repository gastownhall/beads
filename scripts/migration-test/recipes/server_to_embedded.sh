#!/bin/bash
# Recipe: Dolt-server era (v0.50.0–v0.58.0) → current embedded Dolt.
#
# Server-era versions store data in .beads/dolt/ and expect a running
# Dolt SQL server. The current binary uses .beads/embeddeddolt/ with
# an in-process engine. When metadata.json says server mode, the
# candidate tries to TCP connect instead of falling back to embedded.
#
# Strategy:
#   1. Stop any running Dolt server
#   2. Clear stale server metadata
#   3. Init with candidate (will detect dolt/ data and convert to embedded)
#   4. If that fails, export JSONL with old binary and reimport
#
# User-facing instructions:
#   If upgrading from a Dolt-server version (v0.50–v0.58):
#     1. Stop your Dolt server: dolt sql-server --stop (or kill the process)
#     2. Remove stale metadata: rm .beads/metadata.json
#     3. Run: bd init --quiet
#     4. Verify: bd list --all

recipe_server_to_embedded() {
    local ws="$1"
    local old_bin="$2"
    local cand_bin="$3"
    local version="$4"

    echo "  Trying server→embedded recipe..."

    # Step 1: Stop any running server
    stop_dolt_server "$ws"

    # Step 2: Clear stale server metadata that causes TCP connect attempts
    rm -f "$ws/.beads/metadata.json" 2>/dev/null || true
    rm -f "$ws/.beads/dolt-server.pid" 2>/dev/null || true
    rm -f "$ws/.beads/dolt-server.lock" 2>/dev/null || true

    # Step 3: Try candidate init (may auto-detect dolt/ and convert)
    if bd_in "$ws" "$cand_bin" init --quiet --non-interactive </dev/null >/dev/null 2>&1; then
        echo "  candidate init succeeded after clearing server metadata"
        return 0
    fi

    # Step 4: Fallback — export via old binary and reimport
    echo "  direct init failed, trying JSONL export fallback..."

    # Start old server briefly to export
    if bd_in "$ws" "$old_bin" list --json -n 0 --all > "$ws/.beads/issues.jsonl.tmp" 2>/dev/null; then
        if [ -s "$ws/.beads/issues.jsonl.tmp" ]; then
            jq -c '.[]' "$ws/.beads/issues.jsonl.tmp" > "$ws/.beads/issues.jsonl" 2>/dev/null || true
            rm -f "$ws/.beads/issues.jsonl.tmp"
        fi
    fi
    stop_dolt_server "$ws"

    if [ -s "$ws/.beads/issues.jsonl" ]; then
        if bd_in "$ws" "$cand_bin" init --from-jsonl --quiet --non-interactive </dev/null >/dev/null 2>&1; then
            echo "  candidate init --from-jsonl succeeded (JSONL fallback)"
            return 0
        fi
    fi

    echo "  FAILED: could not migrate from server mode"
    return 1
}
