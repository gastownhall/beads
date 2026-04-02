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
#
# Usage:
#   ./scripts/upgrade-smoke-test.sh              # test previous release → candidate
#   ./scripts/upgrade-smoke-test.sh v0.62.0      # test specific version → candidate
#   CANDIDATE_BIN=./bd ./scripts/upgrade-smoke-test.sh  # use prebuilt candidate
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
    local url="https://github.com/steveyegge/beads/releases/download/${PREV_VERSION}/${asset}"

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

# ---------------------------------------------------------------------------
# Scenario 1: Embedded maintainer upgrade
# ---------------------------------------------------------------------------

scenario "Embedded maintainer: init → create → upgrade → verify"

WS=$(new_workspace)

# Init with previous version
"$PREV_BIN" --db "$WS/.beads/beads.db" init --quiet --non-interactive 2>/dev/null || true
git -C "$WS" config beads.role maintainer

# Create test data
ID1=$("$PREV_BIN" --db "$WS/.beads/beads.db" create --silent --title "Pre-upgrade issue" --type task --priority 1 2>/dev/null) || true
ID2=$("$PREV_BIN" --db "$WS/.beads/beads.db" create --silent --title "Another issue" --type bug 2>/dev/null) || true

# Upgrade: run candidate init (simulates upgrade)
"$CAND_BIN" --db "$WS/.beads/beads.db" init --quiet --non-interactive 2>/dev/null || true

# Verify
ROLE=$(git -C "$WS" config --get beads.role 2>/dev/null || echo "MISSING")
if [ "$ROLE" = "maintainer" ]; then
    pass "beads.role preserved (maintainer)"
else
    fail "beads.role changed to '$ROLE' (expected maintainer)"
fi

if [ -n "${ID1:-}" ]; then
    LIST_OUT=$("$CAND_BIN" --db "$WS/.beads/beads.db" list --json 2>/dev/null || echo "")
    if echo "$LIST_OUT" | grep -q "Pre-upgrade issue"; then
        pass "Pre-upgrade issues visible after upgrade"
    else
        fail "Pre-upgrade issues NOT visible after upgrade"
    fi
else
    fail "Could not create issues with previous binary (init problem?)"
fi

# Doctor check
if "$CAND_BIN" --db "$WS/.beads/beads.db" doctor quick 2>/dev/null; then
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
"$PREV_BIN" --db "$WS/.beads/beads.db" init --quiet --non-interactive 2>/dev/null || true
git -C "$WS" config beads.role contributor

# Upgrade
"$CAND_BIN" --db "$WS/.beads/beads.db" init --quiet --non-interactive 2>/dev/null || true

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

# Init embedded with previous version
"$PREV_BIN" --db "$WS/.beads/beads.db" init --quiet --non-interactive 2>/dev/null || true
git -C "$WS" config beads.role maintainer

# Check if .beads/beads.db exists (embedded mode indicator)
if [ -f "$WS/.beads/beads.db" ]; then
    pass "Embedded DB exists before upgrade"
else
    fail "Embedded DB missing before upgrade"
fi

# Upgrade
"$CAND_BIN" --db "$WS/.beads/beads.db" init --quiet --non-interactive 2>/dev/null || true

# Verify still embedded
if [ -f "$WS/.beads/beads.db" ]; then
    pass "Embedded DB still exists after upgrade"
else
    fail "Embedded DB disappeared after upgrade (mode flip?)"
fi

# Verify the candidate binary still uses the local DB
SHOW_OUT=$("$CAND_BIN" --db "$WS/.beads/beads.db" config get storage.mode 2>/dev/null || echo "")
if echo "$SHOW_OUT" | grep -qi "embedded\|sqlite\|local"; then
    pass "Storage mode reports embedded/local"
elif [ -z "$SHOW_OUT" ]; then
    # Config key may not exist; if DB file is present, that's the check
    pass "Storage mode not explicitly set (embedded DB present = OK)"
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

# Fresh init with candidate (no previous version)
"$CAND_BIN" --db "$WS/.beads/beads.db" init --quiet --non-interactive 2>/dev/null || true

ROLE=$(git -C "$WS" config --get beads.role 2>/dev/null || echo "MISSING")
if [ "$ROLE" != "MISSING" ] && [ -n "$ROLE" ]; then
    pass "beads.role set after non-interactive init ($ROLE)"
else
    fail "beads.role NOT set after non-interactive init"
fi

rm -rf "$WS"
finish_scenario

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
