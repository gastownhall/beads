#!/bin/bash
# Recipe: Fix database names with dashes in prefix.
#
# Embedded Dolt rejects database names containing dashes. If the project
# prefix contains a dash (e.g., "sm-o_ke"), the candidate creates
# embeddeddolt/ alongside dolt/ and the database name validation fails.
#
# Strategy:
#   1. Detect if the prefix contains dashes
#   2. Replace dashes with underscores in the database name
#   3. Re-init with the sanitized prefix
#
# User-facing instructions:
#   If your project prefix contains dashes:
#     1. Check your prefix: bd config get prefix
#     2. Re-init with underscores: bd init --prefix my_project --quiet

recipe_fix_dash_prefix() {
    local ws="$1"
    local old_bin="$2"
    local cand_bin="$3"
    local version="$4"

    echo "  Trying dash-prefix fix recipe..."

    # Detect current prefix
    local prefix
    prefix=$(bd_in "$ws" "$old_bin" config get prefix 2>/dev/null || true)
    if [ -z "$prefix" ]; then
        # Try to extract from .beads config
        prefix=$(grep -oP 'prefix\s*=\s*\K\S+' "$ws/.beads/config" 2>/dev/null || true)
    fi

    if [ -z "$prefix" ]; then
        echo "  could not determine prefix"
        return 1
    fi

    # Check if prefix has dashes
    if ! echo "$prefix" | grep -q '-'; then
        echo "  prefix '$prefix' has no dashes, recipe not applicable"
        return 1
    fi

    # Sanitize: replace dashes with underscores
    local sanitized
    sanitized=$(echo "$prefix" | tr '-' '_')
    echo "  sanitizing prefix: '$prefix' → '$sanitized'"

    # Stop server, export data, re-init with sanitized prefix
    stop_dolt_server "$ws"

    # Export data with old binary first
    local list_json
    list_json=$(bd_in "$ws" "$old_bin" list --json -n 0 --all 2>/dev/null) || true
    if [ -n "$list_json" ] && [ "$list_json" != "[]" ]; then
        echo "$list_json" | jq -c '.[]' > "$ws/.beads/issues.jsonl" 2>/dev/null || true
    fi

    # Re-init with sanitized prefix
    local -a init_flags=(--quiet --non-interactive --prefix "$sanitized")
    if [ -s "$ws/.beads/issues.jsonl" ]; then
        init_flags=(--from-jsonl "${init_flags[@]}")
    fi

    if bd_in "$ws" "$cand_bin" init "${init_flags[@]}" </dev/null >/dev/null 2>&1; then
        echo "  re-init with sanitized prefix succeeded"
        return 0
    else
        echo "  FAILED: could not re-init with sanitized prefix"
        return 1
    fi
}
