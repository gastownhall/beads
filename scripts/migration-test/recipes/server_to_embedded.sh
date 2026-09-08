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
#   2. Export all data via old binary (which auto-starts its own server)
#   3. Stop server, clear stale metadata, init with candidate
#   4. If candidate DB is empty, reimport from JSONL export
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

    # Step 1: Stop any running server (we'll restart via old binary as needed)
    stop_dolt_server "$ws"

    # Step 2: Export data via old binary BEFORE clearing metadata.
    # The old binary needs metadata.json to know it's in server mode and
    # to auto-start its Dolt server. Removing metadata first (as was done
    # previously) makes the old binary unable to find its data. (GH#3071)
    echo "  exporting data via old binary..."
    local export_ok=false
    if bd_in "$ws" "$old_bin" list --json -n 0 --all > "$ws/.beads/issues.jsonl.tmp" 2>/dev/null; then
        if [ -s "$ws/.beads/issues.jsonl.tmp" ]; then
            jq -c '.[]' "$ws/.beads/issues.jsonl.tmp" > "$ws/.beads/issues.jsonl" 2>/dev/null || true
            rm -f "$ws/.beads/issues.jsonl.tmp"
            if [ -s "$ws/.beads/issues.jsonl" ]; then
                local export_count
                export_count=$(wc -l < "$ws/.beads/issues.jsonl" 2>/dev/null) || export_count=0
                echo "  exported $export_count items to JSONL"
                export_ok=true
            fi
        else
            rm -f "$ws/.beads/issues.jsonl.tmp"
        fi
    fi
    stop_dolt_server "$ws"

    # Step 3: Clear stale server metadata that causes TCP connect attempts,
    # then try candidate init.
    rm -f "$ws/.beads/metadata.json" 2>/dev/null || true
    rm -f "$ws/.beads/dolt-server.pid" 2>/dev/null || true
    rm -f "$ws/.beads/dolt-server.lock" 2>/dev/null || true

    if bd_in "$ws" "$cand_bin" init --quiet --non-interactive </dev/null >/dev/null 2>&1; then
        # Verify candidate actually has data — init may succeed but create
        # an empty database if it didn't detect the old dolt/ data. (GH#3071)
        local verify_out
        verify_out=$(bd_in "$ws" "$cand_bin" list --json -n 0 --all 2>/dev/null) || true
        if [ -n "$verify_out" ] && [ "$verify_out" != "[]" ] && [ "$verify_out" != "null" ]; then
            echo "  candidate init succeeded with data intact"
            return 0
        fi
        echo "  candidate init returned 0 but database is empty"
    fi

    # Step 4: Candidate init produced an empty DB (or failed).
    # Move old storage directories out of the way so checkExistingBeadsData
    # doesn't block --from-jsonl. The old dolt/ is server-era data; the
    # embeddeddolt/ (if any) was just created empty by step 3.
    stop_dolt_server "$ws"
    for old_dir in "$ws/.beads/dolt" "$ws/.beads/embeddeddolt"; do
        [ -d "$old_dir" ] && mv "$old_dir" "${old_dir}.pre-migration" 2>/dev/null || true
    done
    rm -f "$ws/.beads/metadata.json" 2>/dev/null || true
    rm -f "$ws/.beads/config.json" 2>/dev/null || true

    # Reimport from the JSONL export captured in step 2.
    if $export_ok && [ -s "$ws/.beads/issues.jsonl" ]; then
        echo "  reimporting from JSONL export..."
        if bd_in "$ws" "$cand_bin" init --from-jsonl --quiet --non-interactive </dev/null >/dev/null 2>&1; then
            echo "  candidate init --from-jsonl succeeded"
            return 0
        fi
        echo "  init --from-jsonl failed"
    else
        echo "  no JSONL export available for reimport"
    fi

    echo "  FAILED: could not migrate from server mode"
    return 1
}
