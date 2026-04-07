#!/bin/bash
# Recipe: SQLite-era (v0.30.0–v0.50.3) → current embedded Dolt.
#
# SQLite-era versions store data in .beads/beads.db. The current binary
# only looks for embeddeddolt/ or dolt/ directories and ignores beads.db.
# The auto-import logic (auto_import_upgrade.go) only fires when
# issues.jsonl has data, but SQLite-era versions keep it empty.
#
# Strategy:
#   1. Use the OLD binary to export issues to JSONL (if it has an export command)
#   2. If no export command, use sqlite3 to dump issues directly
#   3. Init with candidate using --from-jsonl
#
# User-facing instructions:
#   If upgrading from a SQLite-era version (v0.30–v0.50):
#     1. Keep your old bd binary (or download it from releases)
#     2. Run: old-bd export --format jsonl > .beads/issues.jsonl
#        (or: old-bd list --json -n 0 --all > .beads/issues.jsonl)
#     3. Run: bd init --from-jsonl --quiet
#     4. Verify: bd list --all

recipe_sqlite_to_current() {
    local ws="$1"
    local old_bin="$2"
    local cand_bin="$3"
    local version="$4"

    echo "  Trying SQLite→current recipe..."

    # Strategy 1: Use old binary's export command
    if bd_in "$ws" "$old_bin" export --format jsonl > "$ws/.beads/issues.jsonl.tmp" 2>/dev/null; then
        if [ -s "$ws/.beads/issues.jsonl.tmp" ]; then
            mv "$ws/.beads/issues.jsonl.tmp" "$ws/.beads/issues.jsonl"
            echo "  exported via 'bd export --format jsonl'"
        else
            rm -f "$ws/.beads/issues.jsonl.tmp"
        fi
    fi

    # Strategy 2: Use old binary's list --json as fallback JSONL source
    if [ ! -s "$ws/.beads/issues.jsonl" ]; then
        local list_json
        list_json=$(bd_in "$ws" "$old_bin" list --json -n 0 --all 2>/dev/null) || true
        if [ -n "$list_json" ] && [ "$list_json" != "[]" ] && [ "$list_json" != "null" ]; then
            # Convert JSON array to JSONL (one object per line)
            echo "$list_json" | jq -c '.[]' > "$ws/.beads/issues.jsonl" 2>/dev/null || true
            if [ -s "$ws/.beads/issues.jsonl" ]; then
                echo "  exported via 'bd list --json' → JSONL conversion"
            fi
        fi
    fi

    # Strategy 3: Direct sqlite3 extraction (last resort)
    if [ ! -s "$ws/.beads/issues.jsonl" ] && command -v sqlite3 >/dev/null 2>&1; then
        local db_path=""
        for candidate_db in "$ws/.beads/beads.db" "$ws/beads.db"; do
            [ -f "$candidate_db" ] && db_path="$candidate_db" && break
        done
        if [ -n "$db_path" ]; then
            # Use .mode json (broadly compatible) instead of -json flag
            # which may not exist in older sqlite3 builds.
            sqlite3 "$db_path" ".mode json" "SELECT * FROM issues;" \
                > "$ws/.beads/issues.jsonl.tmp" 2>/dev/null || true
            if [ -s "$ws/.beads/issues.jsonl.tmp" ]; then
                # Convert JSON array to JSONL
                jq -c '.[]' "$ws/.beads/issues.jsonl.tmp" > "$ws/.beads/issues.jsonl" 2>/dev/null || true
                rm -f "$ws/.beads/issues.jsonl.tmp"
                echo "  exported via direct sqlite3 extraction"
            else
                rm -f "$ws/.beads/issues.jsonl.tmp"
            fi

            # Extract dependencies and labels from SQLite while db is still accessible.
            # Tables may not exist in very old versions — failures are silently ignored.
            sqlite3 "$db_path" ".mode json" \
                "SELECT issue_id, depends_on_id FROM dependencies;" \
                > "$ws/.beads/deps.json" 2>/dev/null || true
            sqlite3 "$db_path" ".mode json" \
                "SELECT issue_id, label FROM labels;" \
                > "$ws/.beads/labels.json" 2>/dev/null || true
        fi
    fi

    if [ ! -s "$ws/.beads/issues.jsonl" ]; then
        echo "  FAILED: could not export data from SQLite-era binary"
        return 1
    fi

    # Extract dependencies and labels from SQLite if not already done (Strategies 1/2).
    # Strategy 3 extracts these inline; this covers the case where issues came from
    # the old binary but deps/labels were not included in its output.
    if [ ! -s "$ws/.beads/deps.json" ] || [ ! -s "$ws/.beads/labels.json" ]; then
        if command -v sqlite3 >/dev/null 2>&1; then
            local db_path=""
            for candidate_db in "$ws/.beads/beads.db" "$ws/beads.db"; do
                [ -f "$candidate_db" ] && db_path="$candidate_db" && break
            done
            if [ -n "$db_path" ]; then
                [ ! -s "$ws/.beads/deps.json" ] && \
                    sqlite3 "$db_path" ".mode json" \
                        "SELECT issue_id, depends_on_id FROM dependencies;" \
                        > "$ws/.beads/deps.json" 2>/dev/null || true
                [ ! -s "$ws/.beads/labels.json" ] && \
                    sqlite3 "$db_path" ".mode json" \
                        "SELECT issue_id, label FROM labels;" \
                        > "$ws/.beads/labels.json" 2>/dev/null || true
            fi
        fi
    fi

    # Normalize SQLite type mismatches. SQLite has no native bool, array, or
    # JSON types, so exports produce integer booleans ("ephemeral":0), empty
    # strings for arrays ("waiters":""), and empty strings for JSON objects
    # ("metadata":""). The Go JSONL importer expects real types for these.
    local bool_fields="ephemeral|pinned|is_template|crystallizes|no_history"
    local array_fields="waiters"
    local json_fields="metadata"
    if jq -c "
        walk(if type == \"object\" then
            with_entries(
                if (.key | test(\"^(${bool_fields})$\")) and (.value | type == \"number\")
                then .value = (.value != 0)
                elif (.key | test(\"^(${array_fields})$\")) and (.value | type == \"string\")
                then .value = (if .value == \"\" then [] else (.value | split(\",\") | map(select(. != \"\"))) end)
                elif (.key | test(\"^(${json_fields})$\")) and (.value | type == \"string\")
                then .value = (if .value == \"\" then {} else (.value | fromjson? // {}) end)
                else . end
            )
        else . end)
    " "$ws/.beads/issues.jsonl" > "$ws/.beads/issues.jsonl.normalized" 2>/dev/null; then
        mv "$ws/.beads/issues.jsonl.normalized" "$ws/.beads/issues.jsonl"
        echo "  normalized SQLite types to JSON types"
    else
        rm -f "$ws/.beads/issues.jsonl.normalized"
    fi

    # Stop any old dolt server
    stop_dolt_server "$ws"

    # Move old artifacts out of the way so the candidate's
    # checkExistingBeadsData guard does not reject init as "already initialized".
    # Includes embeddeddolt/ which may have been created empty by a failed
    # direct upgrade attempt in the harness.
    for old_file in "$ws/.beads/beads.db" "$ws/.beads/beads.db-wal" "$ws/.beads/beads.db-shm" \
                     "$ws/.beads/metadata.json" "$ws/.beads/config.json"; do
        [ -f "$old_file" ] && mv "$old_file" "${old_file}.pre-migration" 2>/dev/null || true
    done
    for old_dir in "$ws/.beads/embeddeddolt" "$ws/.beads/dolt"; do
        [ -d "$old_dir" ] && mv "$old_dir" "${old_dir}.pre-migration" 2>/dev/null || true
    done

    # Init with candidate, importing from JSONL
    if bd_in "$ws" "$cand_bin" init --from-jsonl --quiet --non-interactive </dev/null >/dev/null 2>&1; then
        echo "  candidate init --from-jsonl succeeded"

        # Replay dependencies extracted from SQLite
        if [ -s "$ws/.beads/deps.json" ]; then
            local dep_count=0
            while IFS= read -r line; do
                local dep_issue dep_on
                dep_issue=$(echo "$line" | jq -r '.issue_id') || continue
                dep_on=$(echo "$line" | jq -r '.depends_on_id') || continue
                [ -z "$dep_issue" ] || [ -z "$dep_on" ] && continue
                if bd_in "$ws" "$cand_bin" dep add "$dep_issue" "$dep_on" >/dev/null 2>&1; then
                    dep_count=$((dep_count + 1))
                fi
            done < <(jq -c '.[]' "$ws/.beads/deps.json" 2>/dev/null)
            [ "$dep_count" -gt 0 ] && echo "  replayed $dep_count dependencies"
        fi

        # Replay labels extracted from SQLite
        if [ -s "$ws/.beads/labels.json" ]; then
            local label_count=0
            while IFS= read -r line; do
                local lbl_issue lbl_name
                lbl_issue=$(echo "$line" | jq -r '.issue_id') || continue
                lbl_name=$(echo "$line" | jq -r '.label') || continue
                [ -z "$lbl_issue" ] || [ -z "$lbl_name" ] && continue
                if bd_in "$ws" "$cand_bin" label add "$lbl_issue" "$lbl_name" >/dev/null 2>&1; then
                    label_count=$((label_count + 1))
                fi
            done < <(jq -c '.[]' "$ws/.beads/labels.json" 2>/dev/null)
            [ "$label_count" -gt 0 ] && echo "  replayed $label_count labels"
        fi

        # Clean up temporary extraction files
        rm -f "$ws/.beads/deps.json" "$ws/.beads/labels.json"

        return 0
    else
        local init_err
        init_err=$(bd_in "$ws" "$cand_bin" init --from-jsonl --quiet --non-interactive </dev/null 2>&1 | head -1)
        echo "  FAILED: candidate init --from-jsonl: $init_err"
        return 1
    fi
}
