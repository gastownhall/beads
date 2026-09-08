#!/bin/bash
# Snapshot capture and fidelity checking.
# Captures full JSON state of all issues, then compares field-by-field.

# Capture a full JSON snapshot of all issues in a workspace.
# Output: JSON array with one object per issue, sorted by title.
#
# Uses `bd list --json` which includes all fields we need for fidelity
# checking (id, title, status, priority, issue_type, comment_count, etc.)
# For richer data (labels, dependencies), also calls `bd show` per issue.
capture_snapshot() {
    local ws="$1"
    local bin="$2"

    # bd list --json returns a flat array of issue objects
    local list_json
    list_json=$(bd_in "$ws" "$bin" list --json -n 0 --all 2>/dev/null) || true

    if [ -z "$list_json" ] || [ "$list_json" = "null" ] || [ "$list_json" = "[]" ]; then
        echo "[]"
        return 1
    fi

    # Extract IDs for detailed show queries
    local ids
    ids=$(echo "$list_json" | jq -r '.[].id // empty' 2>/dev/null) || true

    if [ -z "$ids" ]; then
        # No IDs extractable, return list output as-is
        echo "$list_json" | jq -S 'sort_by(.title // "")' 2>/dev/null || echo "$list_json"
        return 0
    fi

    # Collect detailed show output for each issue.
    # bd show --json returns an ARRAY (even for one item), so we flatten.
    local items="[]"
    while IFS= read -r id; do
        [ -z "$id" ] && continue
        local show_json
        show_json=$(bd_in "$ws" "$bin" show "$id" --json 2>/dev/null) || true
        if [ -n "$show_json" ] && [ "$show_json" != "null" ]; then
            # show returns an array — concatenate it
            items=$(echo "$items" | jq --argjson arr "$show_json" \
                'if ($arr | type) == "array" then . + $arr else . + [$arr] end' 2>/dev/null) || true
        fi
    done <<< "$ids"

    # Sort by title for stable comparison
    echo "$items" | jq -S 'sort_by(.title // "")' 2>/dev/null || echo "$items"
}

# Compare two snapshots and report fidelity.
# Returns the number of fidelity violations found.
check_fidelity() {
    local version="$1"
    local before="$2"
    local after="$3"
    local violations=0

    # Check we have data in both snapshots
    local before_count after_count
    before_count=$(jq 'length' "$before" 2>/dev/null) || before_count=0
    after_count=$(jq 'length' "$after" 2>/dev/null) || after_count=0

    if [ "$before_count" -eq 0 ]; then
        echo "  FIDELITY: no items in before-snapshot (nothing to compare)"
        return 0
    fi

    if [ "$after_count" -eq 0 ]; then
        echo -e "  ${RED:-}FIDELITY VIOLATION: all $before_count items lost after upgrade${NC:-}"
        return "$before_count"
    fi

    if [ "$after_count" -lt "$before_count" ]; then
        echo -e "  ${RED:-}FIDELITY VIOLATION: item count dropped from $before_count to $after_count${NC:-}"
        violations=$(( before_count - after_count ))
    fi

    # Critical invariant fields to check.
    # bd uses "issue_type" not "type" in its JSON output.
    local INVARIANTS=("title" "description" "priority" "issue_type")

    local i=0
    while [ "$i" -lt "$before_count" ]; do
        local title
        title=$(jq -r ".[$i].title // \"\"" "$before" 2>/dev/null)

        # Skip items with no title (probe issues, etc.)
        if [ -z "$title" ] || [ "$title" = "__probe__" ]; then
            i=$((i + 1))
            continue
        fi

        # Find matching item in after-snapshot by title
        local match
        match=$(jq --arg t "$title" '[.[] | select(.title == $t)] | .[0]' "$after" 2>/dev/null)

        if [ -z "$match" ] || [ "$match" = "null" ]; then
            echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' missing after upgrade${NC:-}"
            violations=$((violations + 1))
            i=$((i + 1))
            continue
        fi

        # Check each invariant field
        for field in "${INVARIANTS[@]}"; do
            local before_val after_val
            before_val=$(jq -r ".[$i].${field} // \"\"" "$before" 2>/dev/null)
            after_val=$(echo "$match" | jq -r ".${field} // \"\"" 2>/dev/null)

            # Skip empty/null fields (feature not available in old version)
            [ -z "$before_val" ] && continue
            [ "$before_val" = "null" ] && continue

            if [ "$before_val" != "$after_val" ]; then
                echo -e "  ${RED:-}FIDELITY VIOLATION: '$title'.${field}: '$before_val' -> '$after_val'${NC:-}"
                violations=$((violations + 1))
            fi
        done

        # Check status category (open vs closed)
        local before_status after_status
        before_status=$(jq -r ".[$i].status // \"\"" "$before" 2>/dev/null)
        after_status=$(echo "$match" | jq -r ".status // \"\"" 2>/dev/null)
        if [ -n "$before_status" ] && [ -n "$after_status" ]; then
            local before_closed after_closed
            before_closed=$(echo "$before_status" | grep -ciE "closed|done|resolved" || true)
            after_closed=$(echo "$after_status" | grep -ciE "closed|done|resolved" || true)
            if [ "$before_closed" -ne "$after_closed" ]; then
                echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' status category changed: '$before_status' -> '$after_status'${NC:-}"
                violations=$((violations + 1))
            fi
        fi

        # Check dependency preservation
        local before_deps after_deps
        before_deps=$(jq -r ".[$i].dependencies // [] | [.[].id // .] | sort | join(\",\")" "$before" 2>/dev/null)
        after_deps=$(echo "$match" | jq -r ".dependencies // [] | [.[].id // .] | sort | join(\",\")" 2>/dev/null)
        if [ -n "$before_deps" ] && [ "$before_deps" != "$after_deps" ]; then
            echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' dependencies changed: '$before_deps' -> '$after_deps'${NC:-}"
            violations=$((violations + 1))
        fi

        # Check comment count preservation
        local before_comments after_comments
        before_comments=$(jq -r ".[$i].comment_count // (.comments // [] | length) // 0" "$before" 2>/dev/null)
        after_comments=$(echo "$match" | jq -r ".comment_count // (.comments // [] | length) // 0" 2>/dev/null)
        # Normalize: treat empty/null as 0
        [ -z "$before_comments" ] && before_comments=0
        [ -z "$after_comments" ] && after_comments=0
        if [ "$before_comments" != "0" ] && [ "$before_comments" != "$after_comments" ]; then
            echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' comment count: $before_comments -> $after_comments${NC:-}"
            violations=$((violations + 1))
        fi

        # Check label preservation
        local before_labels after_labels
        before_labels=$(jq -r ".[$i].labels // [] | sort | join(\",\")" "$before" 2>/dev/null)
        after_labels=$(echo "$match" | jq -r ".labels // [] | sort | join(\",\")" 2>/dev/null)
        if [ -n "$before_labels" ] && [ "$before_labels" != "$after_labels" ]; then
            echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' labels changed: '$before_labels' -> '$after_labels'${NC:-}"
            violations=$((violations + 1))
        fi

        i=$((i + 1))
    done

    if [ "$violations" -eq 0 ]; then
        echo -e "  ${GREEN:-}FIDELITY: all $before_count items verified, no violations${NC:-}"
    fi

    return "$violations"
}
