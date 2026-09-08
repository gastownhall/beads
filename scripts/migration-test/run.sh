#!/bin/bash
set -uo pipefail

# =============================================================================
# Migration Test Harness
# =============================================================================
#
# Tests upgrade paths from old beads versions to the candidate binary,
# verifying data fidelity field-by-field and discovering working migration
# recipes when direct upgrades fail.
#
# For each upgrade path tested:
#   1. Init workspace with source version, create rich canonical dataset
#   2. Capture full JSON snapshot of all data (before)
#   3. Upgrade to candidate (or next stepping-stone version)
#   4. Capture full JSON snapshot (after)
#   5. Compare field-by-field for fidelity violations
#   6. If direct upgrade fails, try applicable migration recipes
#   7. Report: AUTO / MANUAL(recipe) / BLOCKED per path
#
# Usage:
#   ./scripts/migration-test/run.sh                    # all direct + stepping-stone paths
#   ./scripts/migration-test/run.sh --direct-only      # only direct paths
#   ./scripts/migration-test/run.sh --stepping-only    # only stepping-stone paths
#   ./scripts/migration-test/run.sh --self-test        # candidate → candidate (harness validation)
#   ./scripts/migration-test/run.sh v0.49.6            # single version
#   CANDIDATE_BIN=./bd ./scripts/migration-test/run.sh # prebuilt candidate
#
# Environment:
#   CANDIDATE_BIN      Path to prebuilt candidate binary (skip build)
#   BEADS_TEST_MODE    Set to 1 to disable Dolt auto-start (default: 0)
#   GIT_CONFIG_NOSYSTEM  Set to 1 to ignore system git config (default: 1)
#   BD_OP_TIMEOUT      Timeout in seconds for bd operations (default: 30)
#   DOWNLOAD_TIMEOUT   Timeout in seconds for binary downloads (default: 60)
#
# Exit codes:
#   0  No BLOCKED paths (AUTO and MANUAL are both acceptable)
#   1  One or more paths are BLOCKED (data loss risk)
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source library modules
source "$SCRIPT_DIR/lib/report.sh"      # colors + result tracking (must be first for color vars)
source "$SCRIPT_DIR/lib/binary.sh"      # download_binary, build_candidate
source "$SCRIPT_DIR/lib/workspace.sh"   # new_workspace, bd_in, bd_create, cleanup_workspace
source "$SCRIPT_DIR/lib/versions.sh"    # ERAS, DIRECT_PATHS, version_lte, get_era
source "$SCRIPT_DIR/lib/features.sh"    # create_dataset, try_feature
source "$SCRIPT_DIR/lib/snapshot.sh"    # capture_snapshot, check_fidelity

# Source recipe scripts
source "$SCRIPT_DIR/recipes/sqlite_to_current.sh"
source "$SCRIPT_DIR/recipes/server_to_embedded.sh"
source "$SCRIPT_DIR/recipes/fix_dash_prefix.sh"

# Ensure jq is available
if ! command -v jq >/dev/null 2>&1; then
    echo -e "${RED}ERROR: jq is required but not installed.${NC}"
    echo "Install with: sudo apt install jq / brew install jq"
    exit 1
fi

# ---------------------------------------------------------------------------
# Test a direct upgrade path: source_version → candidate
# ---------------------------------------------------------------------------

test_direct_path() {
    local version="$1"
    local cand_bin="$2"
    local path_label="${version} → candidate"

    echo ""
    echo -e "${BOLD}● Direct: ${path_label}${NC}"

    # Download source binary
    local src_bin
    src_bin=$(download_binary "$version" 2>/dev/null) || {
        record_result "$path_label" "SKIP" "no binary for ${OS}/${ARCH}"
        echo -e "  ${YELLOW}SKIP: no binary for ${OS}/${ARCH}${NC}"
        return 0
    }

    local WS
    WS=$(new_workspace)
    local SNAPSHOTS_DIR
    SNAPSHOTS_DIR=$(mktemp -d /tmp/bd-snapshots-XXXXXX)

    # Step 1: Init with source binary
    local init_ok=false
    if bd_in "$WS" "$src_bin" init --quiet --non-interactive --prefix smoke </dev/null >/dev/null 2>&1; then
        init_ok=true
    elif bd_in "$WS" "$src_bin" init --quiet --prefix smoke </dev/null >/dev/null 2>&1; then
        init_ok=true
    fi

    if ! $init_ok; then
        local init_err
        init_err=$(bd_in "$WS" "$src_bin" init --quiet --prefix smoke </dev/null 2>&1 | head -1 || true)
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"

        if echo "$init_err" | grep -qi "CGO"; then
            record_result "$path_label" "SKIP" "binary built without CGO"
        elif echo "$init_err" | grep -qi "dolt.*server\|unreachable\|auto-start"; then
            record_result "$path_label" "SKIP" "needs dolt server"
        else
            record_result "$path_label" "BLOCKED" "init failed: ${init_err}"
        fi
        echo -e "  ${RESULT_STATUSES[-1]}: ${RESULT_DETAILS[-1]}"
        return 0
    fi
    git -C "$WS" config beads.role maintainer 2>/dev/null || true

    # Step 2: Create rich dataset with source binary
    if ! create_dataset "$WS" "$src_bin" "$version"; then
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        record_result "$path_label" "BLOCKED" "could not create test data"
        echo -e "  ${RED}BLOCKED: could not create test data${NC}"
        return 0
    fi

    # Step 3: Capture before-snapshot
    capture_snapshot "$WS" "$src_bin" > "$SNAPSHOTS_DIR/before.json"
    local before_count
    before_count=$(jq 'length' "$SNAPSHOTS_DIR/before.json" 2>/dev/null) || before_count=0
    echo "  before-snapshot: $before_count items"

    # Stop source server before upgrade
    stop_dolt_server "$WS"

    # Step 4: Try direct upgrade with candidate
    echo "  upgrading to candidate..."
    local upgrade_ok=false

    # First try: just use candidate directly (auto-detect + migrate)
    local list_out
    list_out=$(bd_in "$WS" "$cand_bin" list --json -n 0 --all 2>/dev/null) || true
    if [ -n "$list_out" ] && [ "$list_out" != "[]" ] && [ "$list_out" != "null" ]; then
        upgrade_ok=true
    fi

    # Second try: run candidate init
    if ! $upgrade_ok; then
        bd_in "$WS" "$cand_bin" init --quiet --non-interactive --prefix smoke </dev/null >/dev/null 2>&1 || true
        list_out=$(bd_in "$WS" "$cand_bin" list --json -n 0 --all 2>/dev/null) || true
        if [ -n "$list_out" ] && [ "$list_out" != "[]" ] && [ "$list_out" != "null" ]; then
            upgrade_ok=true
        fi
    fi

    if $upgrade_ok; then
        # Capture after-snapshot and check fidelity
        capture_snapshot "$WS" "$cand_bin" > "$SNAPSHOTS_DIR/after.json"
        local after_count
        after_count=$(jq 'length' "$SNAPSHOTS_DIR/after.json" 2>/dev/null) || after_count=0
        echo "  after-snapshot: $after_count items"

        local violations=0
        check_fidelity "$version" "$SNAPSHOTS_DIR/before.json" "$SNAPSHOTS_DIR/after.json" || violations=$?
        stop_dolt_server "$WS"

        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        record_result "$path_label" "AUTO" "direct upgrade, $violations fidelity violations" "" "$violations"
        return 0
    fi

    # Step 5: Direct upgrade failed — try recipes
    stop_dolt_server "$WS"
    echo "  direct upgrade failed, trying recipes..."

    local era
    era=$(get_era "$version")
    local recipe_worked=false
    local recipe_name=""

    case "$era" in
        sqlite)
            if recipe_sqlite_to_current "$WS" "$src_bin" "$cand_bin" "$version"; then
                recipe_worked=true
                recipe_name="sqlite_to_current"
            fi
            ;;
        dolt_server)
            if recipe_server_to_embedded "$WS" "$src_bin" "$cand_bin" "$version"; then
                recipe_worked=true
                recipe_name="server_to_embedded"
            fi
            ;;
        embedded_old)
            if recipe_fix_dash_prefix "$WS" "$src_bin" "$cand_bin" "$version"; then
                recipe_worked=true
                recipe_name="fix_dash_prefix"
            fi
            # Also try server recipe if prefix fix didn't help
            if ! $recipe_worked; then
                if recipe_server_to_embedded "$WS" "$src_bin" "$cand_bin" "$version"; then
                    recipe_worked=true
                    recipe_name="server_to_embedded"
                fi
            fi
            ;;
    esac

    if $recipe_worked; then
        # Re-capture and check fidelity after recipe
        capture_snapshot "$WS" "$cand_bin" > "$SNAPSHOTS_DIR/after.json"
        local after_count
        after_count=$(jq 'length' "$SNAPSHOTS_DIR/after.json" 2>/dev/null) || after_count=0
        echo "  after-recipe snapshot: $after_count items"

        local violations=0
        check_fidelity "$version" "$SNAPSHOTS_DIR/before.json" "$SNAPSHOTS_DIR/after.json" || violations=$?
        stop_dolt_server "$WS"

        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        record_result "$path_label" "MANUAL" "recipe: $recipe_name, $violations fidelity violations" "$recipe_name" "$violations"
        return 0
    fi

    # All recipes failed
    stop_dolt_server "$WS"
    cleanup_workspace "$WS"
    rm -rf "$SNAPSHOTS_DIR"
    record_result "$path_label" "BLOCKED" "no working upgrade path found"
    echo -e "  ${RED}BLOCKED: no working upgrade path${NC}"
}

# ---------------------------------------------------------------------------
# Test a stepping-stone path: v1 → v2 → ... → candidate
# ---------------------------------------------------------------------------

test_stepping_stone_path() {
    local path_spec="$1"    # comma-separated versions, e.g. "v0.49.6,v0.57.0"
    local cand_bin="$2"

    IFS=',' read -ra VERSIONS <<< "$path_spec"
    local path_label
    path_label=$(printf '%s → ' "${VERSIONS[@]}")
    path_label="${path_label}candidate"

    echo ""
    echo -e "${BOLD}● Stepping-stone: ${path_label}${NC}"

    # Download all required binaries
    local -a bins=()
    for v in "${VERSIONS[@]}"; do
        local b
        b=$(download_binary "$v" 2>/dev/null) || {
            record_result "$path_label" "SKIP" "no binary for $v (${OS}/${ARCH})"
            echo -e "  ${YELLOW}SKIP: no binary for $v${NC}"
            return 0
        }
        bins+=("$b")
    done

    local WS
    WS=$(new_workspace)
    local SNAPSHOTS_DIR
    SNAPSHOTS_DIR=$(mktemp -d /tmp/bd-snapshots-XXXXXX)

    # Init with first version
    local first_bin="${bins[0]}"
    local first_ver="${VERSIONS[0]}"

    local init_ok=false
    if bd_in "$WS" "$first_bin" init --quiet --non-interactive --prefix smoke </dev/null >/dev/null 2>&1; then
        init_ok=true
    elif bd_in "$WS" "$first_bin" init --quiet --prefix smoke </dev/null >/dev/null 2>&1; then
        init_ok=true
    fi

    if ! $init_ok; then
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        record_result "$path_label" "SKIP" "could not init with $first_ver"
        echo -e "  ${YELLOW}SKIP: could not init with $first_ver${NC}"
        return 0
    fi
    git -C "$WS" config beads.role maintainer 2>/dev/null || true

    # Create dataset with first version
    if ! create_dataset "$WS" "$first_bin" "$first_ver"; then
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        record_result "$path_label" "BLOCKED" "could not create test data with $first_ver"
        return 0
    fi

    # Capture initial snapshot
    capture_snapshot "$WS" "$first_bin" > "$SNAPSHOTS_DIR/before.json"
    stop_dolt_server "$WS"

    # Step through intermediate versions
    local step_failed=false
    local failed_at=""
    for i in $(seq 1 $((${#VERSIONS[@]} - 1))); do
        local step_ver="${VERSIONS[$i]}"
        local step_bin="${bins[$i]}"
        echo "  stepping to $step_ver..."

        # Try the step
        local step_ok=false
        local list_out
        list_out=$(bd_in "$WS" "$step_bin" list --json -n 0 --all 2>/dev/null) || true
        if [ -n "$list_out" ] && [ "$list_out" != "[]" ] && [ "$list_out" != "null" ]; then
            step_ok=true
        fi
        if ! $step_ok; then
            bd_in "$WS" "$step_bin" init --quiet --non-interactive --prefix smoke </dev/null >/dev/null 2>&1 || true
            list_out=$(bd_in "$WS" "$step_bin" list --json -n 0 --all 2>/dev/null) || true
            if [ -n "$list_out" ] && [ "$list_out" != "[]" ] && [ "$list_out" != "null" ]; then
                step_ok=true
            fi
        fi

        stop_dolt_server "$WS"

        if ! $step_ok; then
            step_failed=true
            failed_at="$step_ver"
            break
        fi
        echo -e "  ${GREEN}step to $step_ver OK${NC}"
    done

    if $step_failed; then
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        record_result "$path_label" "BLOCKED" "failed at step $failed_at"
        echo -e "  ${RED}BLOCKED at $failed_at${NC}"
        return 0
    fi

    # Final step: candidate
    echo "  stepping to candidate..."
    local upgrade_ok=false
    local list_out
    list_out=$(bd_in "$WS" "$cand_bin" list --json -n 0 --all 2>/dev/null) || true
    if [ -n "$list_out" ] && [ "$list_out" != "[]" ] && [ "$list_out" != "null" ]; then
        upgrade_ok=true
    fi
    if ! $upgrade_ok; then
        bd_in "$WS" "$cand_bin" init --quiet --non-interactive --prefix smoke </dev/null >/dev/null 2>&1 || true
        list_out=$(bd_in "$WS" "$cand_bin" list --json -n 0 --all 2>/dev/null) || true
        if [ -n "$list_out" ] && [ "$list_out" != "[]" ] && [ "$list_out" != "null" ]; then
            upgrade_ok=true
        fi
    fi

    if ! $upgrade_ok; then
        stop_dolt_server "$WS"
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        record_result "$path_label" "BLOCKED" "final step to candidate failed"
        echo -e "  ${RED}BLOCKED at final step${NC}"
        return 0
    fi

    # Capture after-snapshot and check fidelity
    capture_snapshot "$WS" "$cand_bin" > "$SNAPSHOTS_DIR/after.json"
    local violations=0
    check_fidelity "${first_ver}" "$SNAPSHOTS_DIR/before.json" "$SNAPSHOTS_DIR/after.json" || violations=$?

    stop_dolt_server "$WS"
    cleanup_workspace "$WS"
    rm -rf "$SNAPSHOTS_DIR"
    record_result "$path_label" "AUTO" "all steps passed, $violations fidelity violations" "" "$violations"
}

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------

RUN_DIRECT=true
RUN_STEPPING=true
SELF_TEST=false
SPECIFIC_VERSIONS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --direct-only)
            RUN_STEPPING=false
            shift
            ;;
        --stepping-only)
            RUN_DIRECT=false
            shift
            ;;
        --self-test)
            SELF_TEST=true
            RUN_DIRECT=false
            RUN_STEPPING=false
            shift
            ;;
        --help|-h)
            head -30 "$0" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *)
            SPECIFIC_VERSIONS+=("$1")
            RUN_STEPPING=false  # specific versions = direct only
            shift
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

CAND_BIN=$(build_candidate)
echo "Candidate: $CAND_BIN"
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Migration Test Harness"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Self-test: candidate as both source and target (validates the harness itself)
if $SELF_TEST; then
    echo ""
    echo -e "${BOLD}● Self-test: candidate → candidate${NC}"

    WS=$(new_workspace)
    SNAPSHOTS_DIR=$(mktemp -d /tmp/bd-snapshots-XXXXXX)

    # Init with candidate
    if ! bd_in "$WS" "$CAND_BIN" init --quiet --non-interactive --prefix smoke </dev/null >/dev/null 2>&1; then
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
        echo -e "  ${RED}BLOCKED: candidate init failed${NC}"
        record_result "candidate → candidate" "BLOCKED" "init failed"
    else
        git -C "$WS" config beads.role maintainer 2>/dev/null || true

        # Create dataset
        if create_dataset "$WS" "$CAND_BIN" "candidate"; then
            capture_snapshot "$WS" "$CAND_BIN" > "$SNAPSHOTS_DIR/before.json"
            before_count=$(jq 'length' "$SNAPSHOTS_DIR/before.json" 2>/dev/null) || before_count=0
            echo "  snapshot: $before_count items"

            # "Upgrade" is a no-op — just re-read with the same binary
            capture_snapshot "$WS" "$CAND_BIN" > "$SNAPSHOTS_DIR/after.json"

            violations=0
            check_fidelity "candidate" "$SNAPSHOTS_DIR/before.json" "$SNAPSHOTS_DIR/after.json" || violations=$?

            stop_dolt_server "$WS"
            record_result "candidate → candidate" "AUTO" "self-test, $violations fidelity violations" "" "$violations"
        else
            record_result "candidate → candidate" "BLOCKED" "could not create test data"
        fi
        cleanup_workspace "$WS"
        rm -rf "$SNAPSHOTS_DIR"
    fi
fi

# Direct paths
if $RUN_DIRECT; then
    local_paths=("${SPECIFIC_VERSIONS[@]:-${DIRECT_PATHS[@]}}")

    # Pre-download all binaries
    echo ""
    echo -e "${YELLOW}Downloading binaries for direct paths...${NC}"
    for v in "${local_paths[@]}"; do
        download_binary "$v" >/dev/null 2>&1 || echo -e "  ${YELLOW}no binary for $v${NC}"
    done

    for version in "${local_paths[@]}"; do
        test_direct_path "$version" "$CAND_BIN"
    done
fi

# Stepping-stone paths
if $RUN_STEPPING; then
    echo ""
    echo -e "${YELLOW}Downloading binaries for stepping-stone paths...${NC}"
    for path_spec in "${STEPPING_STONE_PATHS[@]}"; do
        IFS=',' read -ra vers <<< "$path_spec"
        for v in "${vers[@]}"; do
            download_binary "$v" >/dev/null 2>&1 || true
        done
    done

    for path_spec in "${STEPPING_STONE_PATHS[@]}"; do
        test_stepping_stone_path "$path_spec" "$CAND_BIN"
    done
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

print_results_table
print_upgrade_instructions
print_ci_summary
print_summary_line

# Clean up candidate if we built it
if [ -z "${CANDIDATE_BIN:-}" ] && [ -f "$CAND_BIN" ]; then
    rm -f "$CAND_BIN"
fi

# Exit with failure only if any path is BLOCKED
for status in "${RESULT_STATUSES[@]}"; do
    if [ "$status" = "BLOCKED" ]; then
        exit 1
    fi
done
exit 0
