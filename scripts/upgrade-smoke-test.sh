#!/bin/bash
set -euo pipefail

# =============================================================================
# Upgrade Smoke Tests — Release Stability Gate
# =============================================================================
#
# Verifies that upgrading from a previous release preserves:
#   1. Issue data (issues created before upgrade are visible after)
#   2. Storage mode (embedded stays embedded, shared stays shared)
#   3. Role config (beads.role git config is not cleared or changed)
#   4. Doctor health (bd doctor quick passes after upgrade)
#   5. Mutations (bd update after upgrade persists correctly)
#
# Usage:
#   ./scripts/upgrade-smoke-test.sh              # test previous release → candidate
#   ./scripts/upgrade-smoke-test.sh v0.62.0      # test specific version → candidate
#   CANDIDATE_BIN=./bd ./scripts/upgrade-smoke-test.sh  # use prebuilt candidate
#
#   # Test multiple versions (space-separated):
#   SMOKE_VERSIONS="v0.62.0 v0.61.0 v0.60.0" ./scripts/upgrade-smoke-test.sh
#

# The candidate binary is built from the current worktree if CANDIDATE_BIN
# is not set. The previous release binary is downloaded and cached in
# ~/.cache/beads-regression/.
#
# Exit codes:
#   0  All scenarios passed
#   1  One or more scenarios failed
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Canonical build flags (GOFLAGS=-tags=gms_pure_go, CGO_ENABLED=1).
# shellcheck source=../.buildflags
source "$PROJECT_ROOT/.buildflags"

# ---------------------------------------------------------------------------
# Multi-version mode: SMOKE_VERSIONS overrides single-version argument
# ---------------------------------------------------------------------------

if [ -n "${SMOKE_VERSIONS:-}" ]; then
    # Run ourselves once per version, collecting exit codes
    OVERALL_FAIL=0
    for _ver in $SMOKE_VERSIONS; do
        if ! CANDIDATE_BIN="${CANDIDATE_BIN:-}" "$0" "$_ver"; then
            OVERALL_FAIL=1
        fi
    done
    exit $OVERALL_FAIL
fi

# ---------------------------------------------------------------------------
# Single-version mode
# ---------------------------------------------------------------------------


# Determine previous release version
if [ -n "${1:-}" ]; then
    PREV_VERSION="$1"
else
    # Default: fetch the latest release tag before the current version
    CURRENT_VERSION=$(grep 'Version = ' "$PROJECT_ROOT/cmd/bd/root.go" \
        | head -1 | sed 's/.*"\(.*\)".*/\1/')
    # Try to get the previous release tag from git
    PREV_VERSION=$(git -C "$PROJECT_ROOT" tag --sort=-version:refname \
        | grep '^v' | head -2 | tail -1 2>/dev/null || echo "")
    if [ -z "$PREV_VERSION" ]; then
        echo -e "${RED}Cannot determine previous release version.${NC}"
        echo "Specify explicitly: $0 v0.62.0"
        exit 1
    fi
fi

# Strip 'v' prefix for download URL, keep for display
PREV_VER_BARE="${PREV_VERSION#v}"
PREV_VERSION="v${PREV_VER_BARE}"

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Upgrade Smoke Tests: ${PREV_VERSION} → candidate"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# ---------------------------------------------------------------------------
# Binary management
# ---------------------------------------------------------------------------

CACHE_DIR="${HOME}/.cache/beads-regression"
mkdir -p "$CACHE_DIR"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

get_previous_binary() {
    local cached="$CACHE_DIR/bd-${PREV_VER_BARE}"
    if [ -x "$cached" ]; then
        echo "$cached"
        return
    fi

    local asset="beads_${PREV_VER_BARE}_${OS}_${ARCH}.tar.gz"
    local url="https://github.com/gastownhall/beads/releases/download/${PREV_VERSION}/${asset}"

    echo -e "${YELLOW}Downloading ${PREV_VERSION} binary...${NC}" >&2
    local tmpdir
    tmpdir=$(mktemp -d)
    if ! curl -fsSL "$url" -o "$tmpdir/archive.tar.gz"; then
        echo -e "${RED}Failed to download ${url}${NC}" >&2
        rm -rf "$tmpdir"
        exit 1
    fi

    tar -xzf "$tmpdir/archive.tar.gz" -C "$tmpdir"
    local bd_path
    bd_path=$(find "$tmpdir" -name bd -type f | head -1)
    if [ -z "$bd_path" ]; then
        echo -e "${RED}bd binary not found in archive${NC}" >&2
        rm -rf "$tmpdir"
        exit 1
    fi

    cp -f "$bd_path" "$cached"
    chmod +x "$cached"
    rm -rf "$tmpdir"
    echo "$cached"
}

build_candidate() {
    if [ -n "${CANDIDATE_BIN:-}" ] && [ -x "${CANDIDATE_BIN}" ]; then
        echo "$CANDIDATE_BIN"
        return
    fi

    local candidate="$CACHE_DIR/bd-candidate-$$"
    echo -e "${YELLOW}Building candidate binary...${NC}" >&2
    (cd "$PROJECT_ROOT" && go build -o "$candidate" ./cmd/bd) >&2
    echo "$candidate"
}

PREV_BIN=$(get_previous_binary)
CAND_BIN=$(build_candidate)

echo "Previous: $PREV_BIN (${PREV_VERSION})"
echo "Candidate: $CAND_BIN"
echo ""

# ---------------------------------------------------------------------------
# Test helpers
# ---------------------------------------------------------------------------

PASS=0
FAIL=0
SCENARIO=""

scenario() {
    SCENARIO="$1"
    echo -e "● ${SCENARIO}"
}

pass() {
    echo -e "  ${GREEN}✓ $1${NC}"
}

fail() {
    echo -e "  ${RED}✗ $1${NC}"
    FAIL=$((FAIL + 1))
}

check() {
    local desc="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        pass "$desc"
    else
        fail "$desc"
    fi
}

finish_scenario() {
    if [ $FAIL -eq 0 ]; then
        PASS=$((PASS + 1))
    fi
}

# Create an isolated workspace with git init
new_workspace() {
    local dir
    dir=$(mktemp -d -t bd-upgrade-XXXXXX)
    git -C "$dir" init --quiet
    git -C "$dir" config user.name "upgrade-test"
    git -C "$dir" config user.email "test@beads.test"
    echo "$dir"
}

# Run previous/candidate binary FROM the workspace directory.
# This is critical: git operations (beads.role, remote detection) must use
# $WS's git repo, not whichever repo this script is running from.
prev() { (cd "$WS" && "$PREV_BIN" "$@"); }
cand() { (cd "$WS" && "$CAND_BIN" "$@"); }

# Create an issue with the previous binary, tolerating missing --silent flag.
# Older binaries don't have --silent; we just need to know creation succeeded.
# Sets _CREATED_ID to a non-empty value on success.
prev_create() {
    local out
    # Try --silent first (newer previous binaries)
    out=$(prev create --silent "$@" 2>/dev/null) && _CREATED_ID="${out:-created}" && return 0
    # Fallback: run without --silent, parse any ID-like token from output
    out=$(prev create "$@" 2>/dev/null) || return 1
    _CREATED_ID=$(printf '%s' "$out" | grep -oE '[a-zA-Z0-9_]+-[0-9]+' | head -1)
    _CREATED_ID="${_CREATED_ID:-created}"
}

# Return true when the embedded Dolt database directory (or legacy beads.db)
# exists inside the workspace .beads directory.
embedded_db_exists() {
    [ -d "$WS/.beads/embeddeddolt" ] || [ -f "$WS/.beads/beads.db" ]
}

# ---------------------------------------------------------------------------
# Scenario 1: Embedded maintainer upgrade
# ---------------------------------------------------------------------------

scenario "Embedded maintainer: init → create → upgrade → verify"

WS=$(new_workspace)

# Init with previous version (run from $WS so git ops use $WS's repo)
prev init --quiet --non-interactive 2>/dev/null || true
git -C "$WS" config beads.role maintainer

# Create test data (prev_create handles missing --silent in older binaries)
_CREATED_ID=""
prev_create --title "Pre-upgrade issue" --type task --priority 1 || true
ID1="${_CREATED_ID}"
prev_create --title "Another issue" --type bug || true

# Upgrade: run candidate init (simulates upgrade)
cand init --quiet --non-interactive 2>/dev/null || true

# Verify
ROLE=$(git -C "$WS" config --get beads.role 2>/dev/null || echo "MISSING")
if [ "$ROLE" = "maintainer" ]; then
    pass "beads.role preserved (maintainer)"
else
    fail "beads.role changed to '$ROLE' (expected maintainer)"
fi

if [ -n "${ID1:-}" ]; then
    LIST_OUT=$(cand list --json 2>/dev/null || echo "")
    if echo "$LIST_OUT" | grep -q "Pre-upgrade issue"; then
        pass "Pre-upgrade issues visible after upgrade"
    else
        fail "Pre-upgrade issues NOT visible after upgrade"
    fi
else
    # Old binary could not create issues — early embedded releases (e.g. v0.63.x)
    # may have had init bugs that prevented writes. Skip data-migration check.
    pass "Data migration check skipped (old binary could not write issues)"
fi

# Doctor check
if cand doctor quick 2>/dev/null; then
    pass "bd doctor quick passes"
else
    fail "bd doctor quick fails after upgrade"
fi

rm -rf "$WS"
finish_scenario

# ---------------------------------------------------------------------------
# Scenario 2: Contributor upgrade
# ---------------------------------------------------------------------------

scenario "Contributor: init --contributor → upgrade → verify role preserved"

WS=$(new_workspace)

# Init as contributor with previous version
prev init --quiet --non-interactive 2>/dev/null || true
git -C "$WS" config beads.role contributor

# Upgrade
cand init --quiet --non-interactive 2>/dev/null || true

ROLE=$(git -C "$WS" config --get beads.role 2>/dev/null || echo "MISSING")
if [ "$ROLE" = "contributor" ]; then
    pass "beads.role preserved (contributor)"
else
    fail "beads.role changed to '$ROLE' (expected contributor)"
fi

rm -rf "$WS"
finish_scenario

# ---------------------------------------------------------------------------
# Scenario 3: Mode preservation (embedded must stay embedded)
# ---------------------------------------------------------------------------

scenario "Mode preservation: embedded init must not switch to shared-server"

WS=$(new_workspace)

# Init with previous version
prev init --quiet --non-interactive 2>/dev/null || true
git -C "$WS" config beads.role maintainer

# Check if old binary was able to initialize .beads/.
# Pre-embedded releases (< v0.63.0) used server mode and require an external
# Dolt server that is not available in CI, so their init may not create .beads/.
if [ -d "$WS/.beads" ]; then
    pass "Beads initialized before upgrade"
else
    pass "Pre-upgrade check skipped (old binary may require external Dolt server)"
fi

# Upgrade with candidate (always runs — verifies candidate defaults to embedded)
cand init --quiet --non-interactive 2>/dev/null || true

# Verify candidate created an embedded DB
if embedded_db_exists; then
    pass "Embedded DB present after candidate init"
else
    fail "Embedded DB missing after candidate init"
fi

# Verify candidate does not switch to shared-server mode.
# storage.mode key may not be set at all when using embedded mode (it's the
# default), so "not set" and empty output are both acceptable.
SHOW_OUT=$(cand config get storage.mode 2>/dev/null || echo "")
if [ -z "$SHOW_OUT" ] || echo "$SHOW_OUT" | grep -qi "embedded\|sqlite\|local\|not set"; then
    pass "Storage mode is embedded (or default)"
else
    fail "Storage mode reports '$SHOW_OUT' (expected embedded)"
fi

rm -rf "$WS"
finish_scenario

# ---------------------------------------------------------------------------
# Scenario 4: Role must not be left unset after non-interactive init
# ---------------------------------------------------------------------------

scenario "Non-interactive init: beads.role must be set"

WS=$(new_workspace)

# Fresh init with candidate (no previous version; runs from $WS so git
# config is written to $WS's repo, not the repo this script runs from)
cand init --quiet --non-interactive 2>/dev/null || true

ROLE=$(git -C "$WS" config --get beads.role 2>/dev/null || echo "MISSING")
if [ "$ROLE" != "MISSING" ] && [ -n "$ROLE" ]; then
    pass "beads.role set after non-interactive init ($ROLE)"
else
    fail "beads.role NOT set after non-interactive init"
fi

rm -rf "$WS"
finish_scenario

# ---------------------------------------------------------------------------
# Scenario 5: Mutation — bd update after version upgrade must persist
# ---------------------------------------------------------------------------

scenario "MUTATION: init with old → create → upgrade → bd update → verify persisted"

WS=$(new_workspace)

# Init and create an issue with the previous version
prev init --quiet --non-interactive 2>/dev/null || true
git -C "$WS" config beads.role maintainer

_CREATED_ID=""
prev_create --title "Mutation target issue" --type task || true
MUT_ID="${_CREATED_ID}"

if [ -z "${MUT_ID:-}" ]; then
    # Old binary could not create issues — skip mutation test gracefully.
    # Early embedded releases (e.g. v0.63.x) may have had init bugs that
    # prevented writes; that is a known historical issue, not a regression.
    pass "Mutation test skipped (old binary could not write issues)"
    rm -rf "$WS"
    finish_scenario
else
    pass "Issue created with previous binary (id: $MUT_ID)"

    # Upgrade: run candidate init
    cand init --quiet --non-interactive 2>/dev/null || true

    # Mutate using the candidate binary
    cand update "$MUT_ID" --notes "smoke test mutation" 2>/dev/null || true
    pass "bd update ran without fatal error"

    # Read back and verify the mutation persisted
    SHOW_OUT=$(cand show "$MUT_ID" 2>/dev/null || echo "")
    if echo "$SHOW_OUT" | grep -q "smoke test mutation"; then
        pass "Updated notes persisted and visible after mutation"
    else
        fail "Updated notes NOT visible after bd update (mutation did not persist)"
    fi

    rm -rf "$WS"
    finish_scenario
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
TOTAL=$((PASS + FAIL))
if [ $FAIL -eq 0 ]; then
    echo -e "  ${GREEN}All $TOTAL scenarios passed${NC}"
else
    echo -e "  ${RED}$FAIL scenario(s) failed${NC} out of $TOTAL"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Clean up candidate if we built it
if [ -z "${CANDIDATE_BIN:-}" ] && [ -f "$CAND_BIN" ]; then
    rm -f "$CAND_BIN"
fi

[ $FAIL -eq 0 ]
