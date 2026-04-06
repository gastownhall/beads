#!/bin/bash
# Snapshot capture and fidelity checking.
# Captures full JSON state of all issues, then compares field-by-field.

# Capture a full JSON snapshot of all issues in a workspace.
# Output: JSON array with one object per issue, sorted by title.
capture_snapshot() {
    local ws="$1"
    local bin="$2"

    # Get all issue IDs
    local list_json
    list_json=$(bd_in "$ws" "$bin" list --json -n 0 --all 2>/dev/null) || true

    if [ -z "$list_json" ] || [ "$list_json" = "null" ] || [ "$list_json" = "[]" ]; then
        echo "[]"
        return 1
    fi

    # Extract IDs from the list output
    local ids
    ids=$(echo "$list_json" | jq -r '.[].id // empty' 2>/dev/null) || true
    if [ -z "$ids" ]; then
        # Try alternate JSON structure
        ids=$(echo "$list_json" | jq -r '.[] | .id // .ID // empty' 2>/dev/null) || true
    fi

    if [ -z "$ids" ]; then
        echo "$list_json" | jq -S '.' 2>/dev/null || echo "[]"
        return 0
    fi

    # Collect detailed show output for each issue
    local items="[]"
    while IFS= read -r id; do
        [ -z "$id" ] && continue
        local show_json
        show_json=$(bd_in "$ws" "$bin" show "$id" --json 2>/dev/null) || true
        if [ -n "$show_json" ] && [ "$show_json" != "null" ]; then
            items=$(echo "$items" | jq --argjson item "$show_json" '. + [$item]' 2>/dev/null) || true
        fi
    done <<< "$ids"

    # Sort by title for stable comparison
    echo "$items" | jq -S 'sort_by(.title // .Title // "")' 2>/dev/null || echo "$items"
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

    # Compare critical invariants for each item (matched by title)
    local INVARIANTS=("title" "description" "priority" "type")

    local i=0
    while [ "$i" -lt "$before_count" ]; do
        local title
        title=$(jq -r ".[$i].title // .[$i].Title // \"item-$i\"" "$before" 2>/dev/null)

        # Skip probe issues
        if [ "$title" = "__probe__" ]; then
            i=$((i + 1))
            continue
        fi

        # Find matching item in after-snapshot by title
        local match
        match=$(jq --arg t "$title" '[.[] | select((.title // .Title) == $t)] | .[0]' "$after" 2>/dev/null)

        if [ -z "$match" ] || [ "$match" = "null" ]; then
            echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' missing after upgrade${NC:-}"
            violations=$((violations + 1))
            i=$((i + 1))
            continue
        fi

        # Check each invariant field
        for field in "${INVARIANTS[@]}"; do
            local before_val after_val
            before_val=$(jq -r ".[$i].${field} // .[$i].$(echo "$field" | sed 's/./\U&/') // \"\"" "$before" 2>/dev/null)
            after_val=$(echo "$match" | jq -r ".${field} // .$(echo "$field" | sed 's/./\U&/') // \"\"" 2>/dev/null)

            # Skip empty fields (feature not available in old version)
            [ -z "$before_val" ] && continue

            if [ "$before_val" != "$after_val" ]; then
                echo -e "  ${RED:-}FIDELITY VIOLATION: '$title'.${field}: '$before_val' -> '$after_val'${NC:-}"
                violations=$((violations + 1))
            fi
        done

        # Check status category (open vs closed)
        local before_status after_status
        before_status=$(jq -r ".[$i].status // .[$i].Status // \"\"" "$before" 2>/dev/null)
        after_status=$(echo "$match" | jq -r ".status // .Status // \"\"" 2>/dev/null)
        if [ -n "$before_status" ] && [ -n "$after_status" ]; then
            # Normalize: both should be open or both closed
            local before_closed after_closed
            before_closed=$(echo "$before_status" | grep -ciE "closed|done|resolved" || true)
            after_closed=$(echo "$after_status" | grep -ciE "closed|done|resolved" || true)
            if [ "$before_closed" -ne "$after_closed" ]; then
                echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' status category changed: '$before_status' -> '$after_status'${NC:-}"
                violations=$((violations + 1))
            fi
        fi

        # Check dependency preservation (if present)
        local before_deps after_deps
        before_deps=$(jq -r ".[$i].dependencies // .[$i].blocked_by // [] | [.[].id // .] | sort | join(\",\")" "$before" 2>/dev/null)
        after_deps=$(echo "$match" | jq -r ".dependencies // .blocked_by // [] | [.[].id // .] | sort | join(\",\")" 2>/dev/null)
        if [ -n "$before_deps" ] && [ "$before_deps" != "$after_deps" ]; then
            echo -e "  ${RED:-}FIDELITY VIOLATION: '$title' dependencies changed: '$before_deps' -> '$after_deps'${NC:-}"
            violations=$((violations + 1))
        fi

        # Check comment count preservation
        local before_comments after_comments
        before_comments=$(jq -r ".[$i].comments // .[$i].comment_count // 0 | if type == \"array\" then length else . end" "$before" 2>/dev/null)
        after_comments=$(echo "$match" | jq -r ".comments // .comment_count // 0 | if type == \"array\" then length else . end" 2>/dev/null)
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
