#!/bin/bash
set -uo pipefail

# =============================================================================
# Cross-Version Smoke Test
# =============================================================================
#
# Verifies that data created with old bd versions is readable after upgrading
# to the candidate (current worktree) binary.
#
# For each version tested:
#   1. Init a fresh workspace with the old binary
#   2. Create an epic, two issues, and a dependency
#   3. Read all data with the candidate binary
#   4. Verify all items are visible and dependency is preserved
#
# Usage:
#   ./scripts/cross-version-smoke-test.sh                       # last 30 tags
#   ./scripts/cross-version-smoke-test.sh --local               # candidate only
#   ./scripts/cross-version-smoke-test.sh --from v0.30.0        # all tags from v0.30.0
#   ./scripts/cross-version-smoke-test.sh v0.63.3 v1.0.0        # specific versions
#   CANDIDATE_BIN=./bd ./scripts/cross-version-smoke-test.sh    # prebuilt candidate
#
# Environment:
#   CANDIDATE_BIN    Path to prebuilt candidate binary (skip build)
#   BEADS_TEST_MODE  Set to 1 to suppress telemetry/prompts
#
# Exit codes:
#   0  All tested versions passed (skips don't count as failures)
#   1  One or more versions failed verification after upgrade
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

CACHE_DIR="${HOME}/.cache/beads-regression"
mkdir -p "$CACHE_DIR"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

export BEADS_TEST_MODE="${BEADS_TEST_MODE:-1}"
export GIT_CONFIG_NOSYSTEM=1

# ---------------------------------------------------------------------------
# Binary management
# ---------------------------------------------------------------------------

download_binary() {
    local version="$1"
    local ver_bare="${version#v}"
    local cached="$CACHE_DIR/bd-${ver_bare}"

    if [ -x "$cached" ]; then
        echo "$cached"
        return
    fi

    local asset="beads_${ver_bare}_${OS}_${ARCH}.tar.gz"
    local url="https://github.com/steveyegge/beads/releases/download/${version}/${asset}"

    echo -e "  ${YELLOW}downloading ${version}...${NC}" >&2
    local tmpdir
    tmpdir=$(mktemp -d)
    if ! curl -fsSL "$url" -o "$tmpdir/archive.tar.gz" 2>/dev/null; then
        rm -rf "$tmpdir"
        return 1
    fi

    tar -xzf "$tmpdir/archive.tar.gz" -C "$tmpdir"
    local bd_path
    bd_path=$(find "$tmpdir" -name bd -type f | head -1)
    if [ -z "$bd_path" ]; then
        rm -rf "$tmpdir"
        return 1
    fi

    cp -f "$bd_path" "$cached"
    chmod +x "$cached"
    rm -rf "$tmpdir"
    echo "$cached"
}

download_all_binaries() {
    local versions=("$@")
    local total=${#versions[@]}
    local downloaded=0
    local skipped=0

    echo -e "${YELLOW}Downloading ${total} binaries...${NC}"
    for version in "${versions[@]}"; do
        if download_binary "$version" >/dev/null 2>&1; then
            downloaded=$((downloaded + 1))
        else
            echo -e "  ${YELLOW}no binary for ${version} (${OS}/${ARCH})${NC}"
            skipped=$((skipped + 1))
        fi
    done
    echo -e "${GREEN}Downloaded ${downloaded}${NC}, skipped ${skipped}"
    echo ""
}

build_candidate() {
    if [ -n "${CANDIDATE_BIN:-}" ] && [ -x "${CANDIDATE_BIN}" ]; then
        echo "$(cd "$(dirname "$CANDIDATE_BIN")" && pwd)/$(basename "$CANDIDATE_BIN")"
        return
    fi

    local candidate="$CACHE_DIR/bd-candidate-$$"
    echo -e "${YELLOW}Building candidate binary...${NC}" >&2
    (cd "$PROJECT_ROOT" && go build -o "$candidate" ./cmd/bd) >&2
    echo "$candidate"
}

# ---------------------------------------------------------------------------
# Workspace and server helpers
# ---------------------------------------------------------------------------

new_workspace() {
    local dir
    dir=$(mktemp -d /tmp/bdxver-XXXXXX)
    git -C "$dir" init --quiet
    git -C "$dir" config user.name "smoke-test"
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
    (cd "$ws" && "$bin" "$@")
}

# create an issue, returning just the ID on stdout
# tries --silent first, falls back to parsing "Created issue: <id>" output
bd_create() {
    local ws="$1"
    local bin="$2"
    shift 2
    # try --silent first
    local id
    id=$(bd_in "$ws" "$bin" create --silent "$@" 2>/dev/null) && [ -n "$id" ] && echo "$id" && return 0
    # fallback: parse verbose output
    id=$(bd_in "$ws" "$bin" create "$@" 2>&1 | grep -oP 'Created issue: \K\S+' || true)
    [ -n "$id" ] && echo "$id" && return 0
    return 1
}

# kill all background processes associated with a workspace (best-effort, never fails)
stop_dolt_server() {
    local ws="$1"
    # kill by pid file — covers dolt server, monitor, and daemon
    local pid=""
    for pidfile in "$ws/.beads/dolt-server.pid" "$ws/.beads/dolt-monitor.pid" "$ws/.beads/daemon.pid"; do
        if [ -f "$pidfile" ]; then
            pid=$(cat "$pidfile" 2>/dev/null) || true
            [ -n "$pid" ] && kill -9 "$pid" 2>/dev/null || true
        fi
    done
    # kill any process with this workspace path in its command line
    pkill -9 -f "$ws" 2>/dev/null || true
    sleep 1
    rm -f "$ws/.beads/bd.sock" "$ws/.beads/dolt-server.lock" 2>/dev/null || true
}

cleanup_workspace() {
    local ws="$1"
    local bin="${2:-}"
    stop_dolt_server "$ws" "$bin"
    rm -rf "$ws"
}

# ---------------------------------------------------------------------------
# Results tracking
# ---------------------------------------------------------------------------

PASS=0
FAIL=0
SKIP=0
FAILED_VERSIONS=""

# parallel arrays for table output
declare -a RESULT_VERSIONS=()
declare -a RESULT_STATUSES=()
declare -a RESULT_DETAILS=()

record_result() {
    local version="$1"
    local status="$2"
    local detail="$3"
    RESULT_VERSIONS+=("$version")
    RESULT_STATUSES+=("$status")
    RESULT_DETAILS+=("$detail")

    case "$status" in
        PASS) PASS=$((PASS + 1)) ;;
        FAIL) FAIL=$((FAIL + 1)); FAILED_VERSIONS="${FAILED_VERSIONS} ${version}" ;;
        SKIP) SKIP=$((SKIP + 1)) ;;
    esac
}

print_results_table() {
    echo ""
    echo -e "${BOLD}Results Table${NC}"
    printf "%-12s %-6s %s\n" "Version" "Status" "Detail"
    printf "%-12s %-6s %s\n" "-------" "------" "------"

    for i in "${!RESULT_VERSIONS[@]}"; do
        local ver="${RESULT_VERSIONS[$i]}"
        local status="${RESULT_STATUSES[$i]}"
        local detail="${RESULT_DETAILS[$i]}"

        case "$status" in
            PASS) printf "%-12s ${GREEN}%-6s${NC} %s\n" "$ver" "$status" "$detail" ;;
            FAIL) printf "%-12s ${RED}%-6s${NC} %s\n" "$ver" "$status" "$detail" ;;
            SKIP) printf "%-12s ${YELLOW}%-6s${NC} %s\n" "$ver" "$status" "$detail" ;;
        esac
    done
}

# GitHub Actions compatible markdown summary
print_ci_summary() {
    if [ -z "${GITHUB_STEP_SUMMARY:-}" ]; then
        return
    fi

    {
        echo "## Cross-Version Smoke Test Results"
        echo ""
        echo "| Version | Status | Detail |"
        echo "|---------|--------|--------|"
        for i in "${!RESULT_VERSIONS[@]}"; do
            local ver="${RESULT_VERSIONS[$i]}"
            local status="${RESULT_STATUSES[$i]}"
            local detail="${RESULT_DETAILS[$i]}"
            local icon=""
            case "$status" in
                PASS) icon="✅" ;;
                FAIL) icon="❌" ;;
                SKIP) icon="⏭️" ;;
            esac
            echo "| ${ver} | ${icon} ${status} | ${detail} |"
        done
        echo ""
        echo "**${PASS} passed, ${FAIL} failed, ${SKIP} skipped**"
    } >> "$GITHUB_STEP_SUMMARY"
}

# ---------------------------------------------------------------------------
# Verification helper (sets VERIFY_DETAIL, returns error count)
# ---------------------------------------------------------------------------

VERIFY_ERRORS=0
VERIFY_DETAIL=""

verify_candidate() {
    local ws="$1"
    local cand_bin="$2"
    local epic="$3"
    local id1="$4"
    local id2="$5"
    VERIFY_ERRORS=0
    VERIFY_DETAIL=""

    local LIST_OUT
    LIST_OUT=$(bd_in "$ws" "$cand_bin" list --json -n 0 --all 2>/dev/null || echo "")

    for title in "Smoke epic" "Smoke task alpha" "Smoke task beta"; do
        if echo "$LIST_OUT" | grep -qF "$title"; then
            echo -e "  ${GREEN}✓${NC} '$title' visible"
        else
            echo -e "  ${RED}✗${NC} '$title' NOT visible"
            VERIFY_ERRORS=$((VERIFY_ERRORS + 1))
            VERIFY_DETAIL="list missing items"
        fi
    done

    for id in "$epic" "$id1" "$id2"; do
        if bd_in "$ws" "$cand_bin" show "$id" --json >/dev/null 2>&1; then
            echo -e "  ${GREEN}✓${NC} show $id"
        else
            local show_err=""
            show_err=$(bd_in "$ws" "$cand_bin" show "$id" --json 2>&1 | grep -i error | head -1 | cut -c1-120 || true)
            echo -e "  ${RED}✗${NC} show $id failed: ${show_err}"
            VERIFY_ERRORS=$((VERIFY_ERRORS + 1))
            VERIFY_DETAIL="${show_err:-show failed}"
        fi
    done

    local DEP_OUT
    DEP_OUT=$(bd_in "$ws" "$cand_bin" show "$id2" --json 2>/dev/null || echo "")
    if echo "$DEP_OUT" | grep -qF "$id1"; then
        echo -e "  ${GREEN}✓${NC} dependency preserved"
    else
        echo -e "  ${RED}✗${NC} dependency NOT preserved"
        VERIFY_ERRORS=$((VERIFY_ERRORS + 1))
        [ -z "$VERIFY_DETAIL" ] && VERIFY_DETAIL="dependency lost"
    fi

    if bd_in "$ws" "$cand_bin" doctor quick >/dev/null 2>&1; then
        echo -e "  ${GREEN}✓${NC} doctor quick"
    else
        echo -e "  ${RED}✗${NC} doctor quick failed"
        VERIFY_ERRORS=$((VERIFY_ERRORS + 1))
        [ -z "$VERIFY_DETAIL" ] && VERIFY_DETAIL="doctor failed"
    fi
}

# ---------------------------------------------------------------------------
# Test runner
# ---------------------------------------------------------------------------

test_version() {
    local version="$1"
    local prev_bin="$2"
    local cand_bin="$3"

    echo -e "● ${version} → candidate"

    local WS
    WS=$(new_workspace)

    # -- step 1: init with old binary --
    # try --non-interactive (v1.0.0+), fall back without
    local init_ok=false
    if bd_in "$WS" "$prev_bin" init --quiet --non-interactive --prefix sm-o_ke </dev/null >/dev/null 2>&1; then
        init_ok=true
    elif bd_in "$WS" "$prev_bin" init --quiet --prefix sm-o_ke </dev/null >/dev/null 2>&1; then
        init_ok=true
    fi

    if ! $init_ok; then
        # capture the actual error for the report
        local init_err=""
        init_err=$(bd_in "$WS" "$prev_bin" init --quiet --prefix sm-o_ke </dev/null 2>&1 | head -1 || true)
        cleanup_workspace "$WS" "$prev_bin"

        if echo "$init_err" | grep -qi "CGO"; then
            record_result "$version" "SKIP" "binary built without CGO (${ARCH})"
        elif echo "$init_err" | grep -qi "dolt.*server\|unreachable\|auto-start failed"; then
            record_result "$version" "SKIP" "needs dolt server (not installed)"
        else
            record_result "$version" "FAIL" "init failed: ${init_err}"
        fi
        echo -e "  ${RESULT_STATUSES[-1]}: ${RESULT_DETAILS[-1]}"
        return 0
    fi
    git -C "$WS" config beads.role maintainer 2>/dev/null || true

    # -- step 2: create data with old binary --
    local EPIC ID1 ID2
    EPIC=$(bd_create "$WS" "$prev_bin" --title "Smoke epic" --type epic --priority 2) || true
    ID1=$(bd_create "$WS" "$prev_bin" --title "Smoke task alpha" --type task --priority 2) || true
    ID2=$(bd_create "$WS" "$prev_bin" --title "Smoke task beta" --type bug --priority 1) || true

    if [ -z "${EPIC:-}" ] || [ -z "${ID1:-}" ] || [ -z "${ID2:-}" ]; then
        cleanup_workspace "$WS" "$prev_bin"
        record_result "$version" "FAIL" "create failed (epic=${EPIC:-?} id1=${ID1:-?} id2=${ID2:-?})"
        echo -e "  ${RED}FAIL: ${RESULT_DETAILS[-1]}${NC}"
        return 0
    fi

    bd_in "$WS" "$prev_bin" dep add "$ID2" "$ID1" >/dev/null 2>&1 || true
    echo -e "  created: epic=$EPIC task=$ID1 bug=$ID2"

    # stop any dolt server before handing to candidate
    stop_dolt_server "$WS" "$prev_bin"

    # -- step 3: verify with candidate (direct read) --
    verify_candidate "$WS" "$cand_bin" "$EPIC" "$ID1" "$ID2"
    # candidate may auto-start a dolt server — stop it to prevent orphaned processes
    stop_dolt_server "$WS"
    local errors=$VERIFY_ERRORS
    local error_details="$VERIFY_DETAIL"

    # -- step 4: if direct read failed and candidate suggests "bd init", follow that advice --
    if [ "$errors" -gt 0 ]; then
        local hint
        hint=$(bd_in "$WS" "$cand_bin" list 2>&1 | grep -o "run 'bd init'" || true)
        if [ -n "$hint" ]; then
            echo -e "  ${YELLOW}candidate suggests 'bd init', following that advice...${NC}"
            bd_in "$WS" "$cand_bin" init --quiet --non-interactive --prefix sm-o_ke </dev/null >/dev/null 2>&1 || true

            verify_candidate "$WS" "$cand_bin" "$EPIC" "$ID1" "$ID2"
            if [ "$VERIFY_ERRORS" -eq 0 ]; then
                record_result "${version}" "PASS" "passed after bd init"
                cleanup_workspace "$WS" "$prev_bin"
                return 0
            fi
            error_details="direct: ${error_details}; after bd init: ${VERIFY_DETAIL}"
            errors=$VERIFY_ERRORS
        fi
    fi

    cleanup_workspace "$WS" "$prev_bin"

    if [ "$errors" -eq 0 ]; then
        record_result "$version" "PASS" "all checks passed"
    else
        record_result "$version" "FAIL" "${errors} errors: ${error_details}"
    fi
}

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------

VERSIONS=()
FROM_VERSION=""

while [ $# -gt 0 ]; do
    case "$1" in
        --local)
            VERSIONS=("local")
            shift
            ;;
        --from)
            FROM_VERSION="$2"
            shift 2
            ;;
        *)
            VERSIONS+=("$1")
            shift
            ;;
    esac
done

if [ ${#VERSIONS[@]} -eq 0 ]; then
    if [ -n "$FROM_VERSION" ]; then
        from_bare="${FROM_VERSION#v}"
        while IFS= read -r tag; do
            tag_bare="${tag#v}"
            if printf '%s\n%s\n' "$from_bare" "$tag_bare" | sort -V | head -1 | grep -q "^${from_bare}$"; then
                VERSIONS+=("$tag")
            fi
        done < <(git -C "$PROJECT_ROOT" tag --sort=version:refname | grep '^v')
    else
        while IFS= read -r tag; do
            VERSIONS+=("$tag")
        done < <(git -C "$PROJECT_ROOT" tag --sort=-version:refname | grep '^v' | head -30)
    fi
fi

if [ ${#VERSIONS[@]} -eq 0 ]; then
    echo -e "${RED}No versions to test.${NC}"
    exit 1
fi

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

CAND_BIN=$(build_candidate)
echo "Candidate: $CAND_BIN"
DOLT_STATUS="not installed"
if command -v dolt >/dev/null 2>&1; then
    DOLT_STATUS="$(dolt version 2>/dev/null | head -1)"
fi
echo "Dolt: $DOLT_STATUS"
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Cross-Version Smoke Test: ${#VERSIONS[@]} version(s) → candidate"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# download all binaries upfront
if [ "${VERSIONS[0]}" != "local" ]; then
    download_all_binaries "${VERSIONS[@]}"
fi

for version in "${VERSIONS[@]}"; do
    if [ "$version" = "local" ]; then
        test_version "candidate" "$CAND_BIN" "$CAND_BIN"
    else
        prev_bin=$(download_binary "$version" 2>/dev/null) || {
            echo -e "● ${version} → candidate"
            record_result "$version" "SKIP" "no binary for ${OS}/${ARCH}"
            echo -e "  ${YELLOW}SKIP: no binary for ${OS}/${ARCH}${NC}"
            echo ""
            continue
        }
        test_version "$version" "$prev_bin" "$CAND_BIN"
    fi
    echo ""
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

print_results_table
print_ci_summary

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
TOTAL=$((PASS + FAIL + SKIP))
if [ $FAIL -eq 0 ]; then
    echo -e "  ${GREEN}${PASS} passed${NC}, ${SKIP} skipped, 0 failed (of ${TOTAL})"
else
    echo -e "  ${RED}${FAIL} FAILED${NC}, ${PASS} passed, ${SKIP} skipped (of ${TOTAL})"
    echo -e "  Failed:${FAILED_VERSIONS}"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# clean up candidate if we built it
if [ -z "${CANDIDATE_BIN:-}" ] && [ -f "$CAND_BIN" ]; then
    rm -f "$CAND_BIN"
fi

[ $FAIL -eq 0 ]
