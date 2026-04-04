#!/bin/bash
set -euo pipefail

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
#   3. Verify all data is readable with the candidate binary
#
# Versions before embedded Dolt (< v0.63.0) require a running Dolt server
# and will be skipped automatically.
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

    echo -e "${YELLOW}  downloading ${version}...${NC}" >&2
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
        # resolve to absolute path
        echo "$(cd "$(dirname "$CANDIDATE_BIN")" && pwd)/$(basename "$CANDIDATE_BIN")"
        return
    fi

    local candidate="$CACHE_DIR/bd-candidate-$$"
    echo -e "${YELLOW}Building candidate binary...${NC}" >&2
    (cd "$PROJECT_ROOT" && go build -o "$candidate" ./cmd/bd) >&2
    echo "$candidate"
}

# ---------------------------------------------------------------------------
# Workspace helpers
# ---------------------------------------------------------------------------

# creates a temp workspace with git init and a clean directory name (no dots)
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

# run bd in a workspace directory (all commands run from cwd for compatibility)
bd_in() {
    local ws="$1"
    local bin="$2"
    shift 2
    (cd "$ws" && "$bin" "$@")
}

# ---------------------------------------------------------------------------
# Test runner
# ---------------------------------------------------------------------------

PASS=0
FAIL=0
SKIP=0
FAILED_VERSIONS=""

test_version() {
    local version="$1"
    local prev_bin="$2"
    local cand_bin="$3"
    local errors=0

    echo -e "● ${version} → candidate"

    local WS
    WS=$(new_workspace)

    # init with old binary; try --non-interactive (v1.0.0+), fall back without
    if ! bd_in "$WS" "$prev_bin" init --quiet --non-interactive --prefix smoke </dev/null >/dev/null 2>&1; then
        if ! bd_in "$WS" "$prev_bin" init --quiet --prefix smoke </dev/null >/dev/null 2>&1; then
            echo -e "  ${YELLOW}⊘ init failed (needs Dolt server?), skipping${NC}"
            SKIP=$((SKIP + 1))
            rm -rf "$WS"
            return 0
        fi
    fi
    git -C "$WS" config beads.role maintainer 2>/dev/null || true

    # create epic
    local EPIC
    EPIC=$(bd_in "$WS" "$prev_bin" create --silent --title "Smoke epic" --type epic 2>/dev/null) || true
    if [ -z "${EPIC:-}" ]; then
        echo -e "  ${YELLOW}⊘ create epic failed, skipping${NC}"
        SKIP=$((SKIP + 1))
        rm -rf "$WS"
        return 0
    fi

    # create two issues
    local ID1 ID2
    ID1=$(bd_in "$WS" "$prev_bin" create --silent --title "Smoke task alpha" --type task --priority 2 2>/dev/null) || true
    ID2=$(bd_in "$WS" "$prev_bin" create --silent --title "Smoke task beta" --type bug --priority 1 2>/dev/null) || true

    if [ -z "${ID1:-}" ] || [ -z "${ID2:-}" ]; then
        echo -e "  ${YELLOW}⊘ create issues failed, skipping${NC}"
        SKIP=$((SKIP + 1))
        rm -rf "$WS"
        return 0
    fi

    # add dependency: ID2 depends on ID1
    if ! bd_in "$WS" "$prev_bin" dep add "$ID2" "$ID1" >/dev/null 2>&1; then
        echo -e "  ${YELLOW}⊘ dep add failed, skipping${NC}"
        SKIP=$((SKIP + 1))
        rm -rf "$WS"
        return 0
    fi

    echo -e "  created: epic=$EPIC task=$ID1 bug=$ID2 dep=${ID2}→${ID1}"

    # ---- verify with candidate (no re-init needed, just read) ----

    local LIST_OUT
    LIST_OUT=$(bd_in "$WS" "$cand_bin" list --json -n 0 --all 2>/dev/null || echo "")

    for title in "Smoke epic" "Smoke task alpha" "Smoke task beta"; do
        if echo "$LIST_OUT" | grep -q "$title"; then
            echo -e "  ${GREEN}✓${NC} '$title' visible"
        else
            echo -e "  ${RED}✗${NC} '$title' NOT visible"
            errors=$((errors + 1))
        fi
    done

    for id in "$EPIC" "$ID1" "$ID2"; do
        if bd_in "$WS" "$cand_bin" show "$id" --json >/dev/null 2>&1; then
            echo -e "  ${GREEN}✓${NC} show $id"
        else
            echo -e "  ${RED}✗${NC} show $id failed"
            errors=$((errors + 1))
        fi
    done

    # dependency preserved
    local DEP_OUT
    DEP_OUT=$(bd_in "$WS" "$cand_bin" show "$ID2" --json 2>/dev/null || echo "")
    if echo "$DEP_OUT" | grep -q "$ID1"; then
        echo -e "  ${GREEN}✓${NC} dependency preserved"
    else
        echo -e "  ${RED}✗${NC} dependency NOT preserved"
        errors=$((errors + 1))
    fi

    # doctor check
    if bd_in "$WS" "$cand_bin" doctor quick >/dev/null 2>&1; then
        echo -e "  ${GREEN}✓${NC} doctor quick"
    else
        echo -e "  ${RED}✗${NC} doctor quick failed"
        errors=$((errors + 1))
    fi

    rm -rf "$WS"

    if [ $errors -eq 0 ]; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        FAILED_VERSIONS="${FAILED_VERSIONS} ${version}"
    fi
}

# ---------------------------------------------------------------------------
# Parse arguments and determine versions to test
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
        # last 30 tags (default)
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
echo ""

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Cross-Version Smoke Test: ${#VERSIONS[@]} version(s) → candidate"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# download all binaries upfront (skip for --local mode)
if [ "${VERSIONS[0]}" != "local" ]; then
    download_all_binaries "${VERSIONS[@]}"
fi

for version in "${VERSIONS[@]}"; do
    if [ "$version" = "local" ]; then
        test_version "candidate" "$CAND_BIN" "$CAND_BIN"
    else
        prev_bin=$(download_binary "$version" 2>/dev/null) || {
            echo -e "● ${version} → candidate"
            echo -e "  ${YELLOW}⊘ no binary available, skipping${NC}"
            SKIP=$((SKIP + 1))
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
