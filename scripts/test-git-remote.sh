#!/usr/bin/env bash
# test-git-remote.sh — E2E test: bd CLI workflow with Dolt git remotes
#
# Exercises: bd init (dolt backend), bd create, dolt remote add,
#   dolt push, dolt clone, data verification, incremental push+pull.
#
# Usage:
#   ./scripts/test-git-remote.sh              # Local bare git repo (default)
#   ./scripts/test-git-remote.sh <github-url> # Push to real GitHub repo
#
# Requirements: bd, dolt (>= 1.81.0), git
#
# The script is self-contained: creates temp dirs, cleans up on exit.

set -euo pipefail

# ── Helpers ──────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
RESET='\033[0m'

pass() { echo -e "${GREEN}PASS${RESET} $1"; }
fail() { echo -e "${RED}FAIL${RESET} $1"; FAILURES=$((FAILURES + 1)); }
info() { echo -e "${YELLOW}INFO${RESET} $1"; }
section() { echo -e "\n${BOLD}── $1 ──${RESET}"; }

FAILURES=0
CLEANUP_DIRS=()

cleanup() {
    for dir in "${CLEANUP_DIRS[@]}"; do
        rm -rf "$dir" 2>/dev/null || true
    done
}
trap cleanup EXIT

# ── Prerequisites ────────────────────────────────────────────────────

section "Prerequisites"

if ! command -v bd &>/dev/null; then
    echo "Error: bd not found in PATH" >&2; exit 1
fi
if ! command -v dolt &>/dev/null; then
    echo "Error: dolt not found in PATH" >&2; exit 1
fi
if ! command -v git &>/dev/null; then
    echo "Error: git not found in PATH" >&2; exit 1
fi

BD_VERSION=$(bd version 2>&1 | head -1)
DOLT_VERSION=$(dolt version 2>&1 | head -1)
info "bd:   $BD_VERSION"
info "dolt: $DOLT_VERSION"

# ── Setup ────────────────────────────────────────────────────────────

section "Setup"

WORKDIR=$(mktemp -d "${TMPDIR:-/tmp}/bd-git-remote-test.XXXXXX")
CLEANUP_DIRS+=("$WORKDIR")
info "Working directory: $WORKDIR"

REMOTE_URL="${1:-}"
USE_LOCAL_REMOTE=true

if [[ -n "$REMOTE_URL" ]]; then
    USE_LOCAL_REMOTE=false
    info "Using remote: $REMOTE_URL"
else
    # Create a local git repo as the remote (needs initial commit for dolt push)
    BARE_REPO="$WORKDIR/remote.git"
    INIT_REPO="$WORKDIR/init-remote"
    mkdir -p "$INIT_REPO"
    git -C "$INIT_REPO" init >/dev/null 2>&1
    git -C "$INIT_REPO" checkout -b main 2>/dev/null || true
    git -C "$INIT_REPO" commit --allow-empty -m "init" >/dev/null 2>&1
    git clone --bare "$INIT_REPO" "$BARE_REPO" >/dev/null 2>&1
    rm -rf "$INIT_REPO"
    REMOTE_URL="file://$BARE_REPO"
    info "Using local bare repo: $BARE_REPO"
fi

PREFIX="e2e"
DOLT_DB="beads_${PREFIX}"

# ── Step 1: Initialize bd workspace with dolt backend ────────────────

section "Step 1: bd init --backend dolt"

WORKSPACE_A="$WORKDIR/workspace-a"
mkdir -p "$WORKSPACE_A"
cd "$WORKSPACE_A"
git init >/dev/null 2>&1
# Ensure a main branch exists (some git versions default to master)
git checkout -b main 2>/dev/null || true

bd init --backend dolt --prefix "$PREFIX" -q --skip-hooks --skip-merge-driver --force 2>/dev/null

# Verify dolt database was created
DOLT_DIR="$WORKSPACE_A/.beads/dolt/$DOLT_DB"
if [[ -d "$DOLT_DIR/.dolt" ]]; then
    pass "Dolt database created at .beads/dolt/$DOLT_DB"
else
    fail "Dolt database NOT found at .beads/dolt/$DOLT_DB"
    echo "Contents of .beads/dolt/:" >&2
    ls -la "$WORKSPACE_A/.beads/dolt/" 2>&1 >&2 || true
    exit 1
fi

# Verify metadata.json has dolt backend
if grep -q '"dolt"' "$WORKSPACE_A/.beads/metadata.json" 2>/dev/null; then
    pass "metadata.json specifies dolt backend"
else
    fail "metadata.json does not specify dolt backend"
fi

# ── Step 2: Create issues via bd CLI ─────────────────────────────────

section "Step 2: Create issues"

ISSUE1_ID=$(bd create "First test issue" -t task -q 2>&1 | grep -oE "${PREFIX}-[a-z0-9]+")
if [[ -n "$ISSUE1_ID" ]]; then
    pass "Created issue: $ISSUE1_ID"
else
    fail "Failed to create first issue"
    exit 1
fi

ISSUE2_ID=$(bd create "Second test issue with priority" -t bug --priority 1 -q 2>&1 | grep -oE "${PREFIX}-[a-z0-9]+")
if [[ -n "$ISSUE2_ID" ]]; then
    pass "Created issue: $ISSUE2_ID"
else
    fail "Failed to create second issue"
    exit 1
fi

# Verify issues exist via bd list
ISSUE_COUNT=$(bd list --limit 0 2>/dev/null | grep -c "^" || true)
if [[ "$ISSUE_COUNT" -ge 2 ]]; then
    pass "bd list shows $ISSUE_COUNT issues"
else
    fail "bd list shows $ISSUE_COUNT issues, expected >= 2"
fi

# ── Step 3: Add dolt remote and push ────────────────────────────────

section "Step 3: Dolt remote add + push"

cd "$DOLT_DIR"

# Commit any pending changes in dolt before pushing
dolt add -A 2>/dev/null || true
dolt commit -m "Pre-push commit" --author "test <test@test.com>" 2>/dev/null || true

# Add git remote
dolt remote add origin "$REMOTE_URL" 2>/dev/null
REMOTES=$(dolt remote -v 2>&1)
if echo "$REMOTES" | grep -q "origin"; then
    pass "Dolt remote 'origin' added"
else
    fail "Dolt remote 'origin' not found"
fi

# Push to remote
if dolt push origin main 2>&1; then
    pass "dolt push origin main succeeded"
else
    fail "dolt push origin main failed"
    exit 1
fi

# ── Step 4: Verify refs in remote ───────────────────────────────────

section "Step 4: Verify remote refs"

if $USE_LOCAL_REMOTE; then
    # For local bare repos, check git refs directly
    REFS=$(git -C "$BARE_REPO" show-ref 2>/dev/null || true)
    if [[ -n "$REFS" ]]; then
        pass "Remote has refs after push"
        info "Refs: $(echo "$REFS" | head -5)"
    else
        fail "Remote has no refs after push"
    fi
else
    info "Skipping ref verification for remote URL (use git ls-remote manually)"
fi

# ── Step 5: Clone into second workspace ──────────────────────────────

section "Step 5: Dolt clone into workspace-b"

WORKSPACE_B="$WORKDIR/workspace-b"
mkdir -p "$WORKSPACE_B"
cd "$WORKSPACE_B"
git init >/dev/null 2>&1
git checkout -b main 2>/dev/null || true

# Initialize bd in workspace-b
bd init --backend dolt --prefix "$PREFIX" -q --skip-hooks --skip-merge-driver --force 2>/dev/null

DOLT_DIR_B="$WORKSPACE_B/.beads/dolt/$DOLT_DB"

# Remove the empty dolt db and clone from remote instead
rm -rf "$DOLT_DIR_B"
dolt clone "$REMOTE_URL" "$DOLT_DIR_B" 2>&1

if [[ -d "$DOLT_DIR_B/.dolt" ]]; then
    pass "Dolt clone created at workspace-b"
else
    fail "Dolt clone failed - no .dolt directory"
    exit 1
fi

# ── Step 6: Verify data in clone ────────────────────────────────────

section "Step 6: Verify cloned data"

cd "$WORKSPACE_B"

# Query the cloned dolt database directly to verify data
CLONED_ISSUES=$(cd "$DOLT_DIR_B" && dolt sql -q "SELECT id FROM issues ORDER BY id" -r csv 2>/dev/null | tail -n +2)
CLONED_COUNT=$(echo "$CLONED_ISSUES" | grep -c "^${PREFIX}-" || true)

if [[ "$CLONED_COUNT" -ge 2 ]]; then
    pass "Cloned database has $CLONED_COUNT issues"
else
    fail "Cloned database has $CLONED_COUNT issues, expected >= 2"
    info "Raw query output:"
    cd "$DOLT_DIR_B" && dolt sql -q "SELECT id, title FROM issues" 2>&1 || true
fi

# Verify specific issues exist
if echo "$CLONED_ISSUES" | grep -q "$ISSUE1_ID"; then
    pass "Issue $ISSUE1_ID found in clone"
else
    fail "Issue $ISSUE1_ID NOT found in clone"
fi

if echo "$CLONED_ISSUES" | grep -q "$ISSUE2_ID"; then
    pass "Issue $ISSUE2_ID found in clone"
else
    fail "Issue $ISSUE2_ID NOT found in clone"
fi

# ── Step 7: Incremental push from workspace-a ───────────────────────

section "Step 7: Incremental push (workspace-a)"

cd "$WORKSPACE_A"

ISSUE3_ID=$(bd create "Third issue for incremental test" -t task -q 2>&1 | grep -oE "${PREFIX}-[a-z0-9]+")
if [[ -n "$ISSUE3_ID" ]]; then
    pass "Created incremental issue: $ISSUE3_ID"
else
    fail "Failed to create third issue"
fi

# Push incremental changes
cd "$DOLT_DIR"
dolt add -A 2>/dev/null || true
dolt commit -m "Add third issue" --author "test <test@test.com>" 2>/dev/null || true

if dolt push origin main 2>&1; then
    pass "Incremental push succeeded"
else
    fail "Incremental push failed"
fi

# ── Step 8: Pull into workspace-b ────────────────────────────────────

section "Step 8: Pull into workspace-b (incremental)"

cd "$DOLT_DIR_B"

if dolt pull origin 2>&1; then
    pass "dolt pull origin succeeded"
else
    # Pull may fail if there's no tracking branch; try fetch+merge
    info "Pull failed, trying fetch + merge"
    dolt fetch origin 2>&1 || true
    dolt merge origin/main --author "test <test@test.com>" 2>&1 || true
fi

# Verify the new issue arrived
UPDATED_ISSUES=$(dolt sql -q "SELECT id FROM issues ORDER BY id" -r csv 2>/dev/null | tail -n +2)
UPDATED_COUNT=$(echo "$UPDATED_ISSUES" | grep -c "^${PREFIX}-" || true)

if [[ "$UPDATED_COUNT" -ge 3 ]]; then
    pass "After pull: $UPDATED_COUNT issues (includes incremental)"
else
    fail "After pull: $UPDATED_COUNT issues, expected >= 3"
fi

if echo "$UPDATED_ISSUES" | grep -q "$ISSUE3_ID"; then
    pass "Incremental issue $ISSUE3_ID found after pull"
else
    fail "Incremental issue $ISSUE3_ID NOT found after pull"
fi

# ── Summary ──────────────────────────────────────────────────────────

section "Summary"

if [[ "$FAILURES" -eq 0 ]]; then
    echo -e "\n${GREEN}${BOLD}ALL TESTS PASSED${RESET}\n"
    exit 0
else
    echo -e "\n${RED}${BOLD}$FAILURES TEST(S) FAILED${RESET}\n"
    exit 1
fi
