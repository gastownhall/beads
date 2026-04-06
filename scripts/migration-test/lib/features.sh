#!/bin/bash
# Version-aware feature registry with empirical discovery.
#
# Features have approximate min_version gates. When a command fails at runtime,
# the feature is silently skipped for that version — no test failure.
# The gates file is updated with discovered actual boundaries.

GATES_CACHE="${CACHE_DIR:-${HOME}/.cache/beads-regression}/feature-gates.cache"

# Empirical feature gate: try a command, record whether it worked.
# Returns 0 if the feature is available, 1 if not.
try_feature() {
    local feature="$1"
    local version="$2"
    local ws="$3"
    local bin="$4"
    shift 4

    # Check cache first
    local cache_key="${version}:${feature}"
    if [ -f "$GATES_CACHE" ]; then
        local cached
        cached=$(grep "^${cache_key}=" "$GATES_CACHE" 2>/dev/null | cut -d= -f2)
        if [ "$cached" = "yes" ]; then return 0; fi
        if [ "$cached" = "no" ]; then return 1; fi
    fi

    # Try the command
    if "$@" >/dev/null 2>&1; then
        echo "${cache_key}=yes" >> "$GATES_CACHE"
        return 0
    else
        echo "${cache_key}=no" >> "$GATES_CACHE"
        return 1
    fi
}

# Create the canonical dataset for a given version.
# Populates associative array DATASET_IDS with feature_name -> issue_id.
# Returns 0 if at least the core items were created.
create_dataset() {
    local ws="$1"
    local bin="$2"
    local version="$3"

    # Reset dataset tracking
    declare -gA DATASET_IDS=()
    declare -ga DATASET_FEATURES=()
    local errors=0

    # --- Core items (all versions) ---

    # 1. Epic
    local epic_id
    epic_id=$(bd_create "$ws" "$bin" --title "Migration epic" --type epic --priority 2 --description "Epic for migration testing") || true
    if [ -z "${epic_id:-}" ]; then
        echo "  FATAL: could not create epic" >&2
        return 1
    fi
    DATASET_IDS[epic]="$epic_id"
    DATASET_FEATURES+=("epic")

    # 2. Task (will be child of epic if supported)
    local task_args=(--title "Migration task alpha" --type task --priority 1)
    if try_feature "parent_child" "$version" "$ws" "$bin" create --silent --title "__probe__" --parent "$epic_id" --type task; then
        task_args+=(--parent "$epic_id")
        # clean up probe issue — best effort
        bd_in "$ws" "$bin" close "$(__last_created_id "$ws" "$bin")" 2>/dev/null || true
    fi
    local task_id
    task_id=$(bd_create "$ws" "$bin" "${task_args[@]}") || true
    if [ -z "${task_id:-}" ]; then
        echo "  ERROR: could not create task" >&2
        errors=$((errors + 1))
    else
        DATASET_IDS[task]="$task_id"
        DATASET_FEATURES+=("task")
    fi

    # 3. Bug with dependency on task
    local bug_id
    bug_id=$(bd_create "$ws" "$bin" --title "Migration bug beta" --type bug --priority 3) || true
    if [ -z "${bug_id:-}" ]; then
        echo "  ERROR: could not create bug" >&2
        errors=$((errors + 1))
    else
        DATASET_IDS[bug]="$bug_id"
        DATASET_FEATURES+=("bug")
    fi

    # 4. Dependency: bug blocks-on task
    if [ -n "${task_id:-}" ] && [ -n "${bug_id:-}" ]; then
        if bd_in "$ws" "$bin" dep add "$bug_id" "$task_id" >/dev/null 2>&1; then
            DATASET_FEATURES+=("dependency")
        fi
    fi

    # 5. Standalone task with description
    local standalone_id
    standalone_id=$(bd_create "$ws" "$bin" --title "Standalone detailed task" --type task --priority 2 --description "This task has a detailed description for fidelity testing.") || true
    if [ -n "${standalone_id:-}" ]; then
        DATASET_IDS[standalone]="$standalone_id"
        DATASET_FEATURES+=("standalone")
    fi

    # 6. Closed issue (status preservation)
    local closed_id
    closed_id=$(bd_create "$ws" "$bin" --title "Already closed issue" --type task --priority 3) || true
    if [ -n "${closed_id:-}" ]; then
        bd_in "$ws" "$bin" close "$closed_id" >/dev/null 2>&1 || true
        DATASET_IDS[closed]="$closed_id"
        DATASET_FEATURES+=("closed")
    fi

    # --- Optional features (version-gated, empirical) ---

    # Labels
    if [ -n "${task_id:-}" ]; then
        if bd_in "$ws" "$bin" label add "$task_id" "urgent" >/dev/null 2>&1; then
            DATASET_FEATURES+=("label")
        fi
    fi

    # Comments
    if [ -n "${task_id:-}" ]; then
        if bd_in "$ws" "$bin" comment add "$task_id" "Test comment for fidelity checking" >/dev/null 2>&1; then
            DATASET_FEATURES+=("comment")
        fi
    fi

    # Commit data (triggers JSONL export in pre-embeddeddolt versions)
    git -C "$ws" add -A 2>/dev/null || true
    git -C "$ws" commit --quiet -m "migration test data" 2>/dev/null || true

    echo "  created ${#DATASET_FEATURES[@]} features: ${DATASET_FEATURES[*]}"
    return $errors
}

# Helper: get last created issue ID (for probe cleanup)
__last_created_id() {
    local ws="$1" bin="$2"
    bd_in "$ws" "$bin" list --json -n 1 2>/dev/null | grep -oP '"id"\s*:\s*"\K[^"]+' | head -1
}
