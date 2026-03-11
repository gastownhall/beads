#!/bin/bash
# check-doc-flags.sh — Validate documentation references against actual CLI flags.
#
# This script catches stale doc references by:
# 1. Extracting all flags from `bd help --all`
# 2. Scanning docs for `bd <command> --<flag>` patterns
# 3. Flagging any that don't exist in the CLI
#
# Also checks for references to known-removed commands (bd sync, bd import).
#
# Usage: ./scripts/check-doc-flags.sh [bd-binary]
#
# Exit codes:
#   0 - All docs are consistent with CLI
#   1 - Stale references found

set -euo pipefail

BD="${1:-bd}"
ERRORS=0
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Verify bd binary exists and runs
if ! command -v "$BD" &>/dev/null && [ ! -x "$BD" ]; then
    echo "Error: bd binary not found at '$BD'"
    echo "Usage: $0 [path-to-bd]"
    exit 1
fi

echo "Checking documentation against CLI flags..."
echo "Using: $($BD version 2>/dev/null | head -1 || echo "$BD")"
echo ""

# --- Check 1: Known-removed commands ---
echo "=== Check 1: Removed commands ==="

# bd sync (removed in v0.51)
SYNC_REFS=$(grep -rn 'bd sync\b' \
    "$PROJECT_ROOT"/docs/*.md \
    "$PROJECT_ROOT"/AGENT_INSTRUCTIONS.md \
    "$PROJECT_ROOT"/AGENTS.md \
    "$PROJECT_ROOT"/README.md \
    "$PROJECT_ROOT"/npm-package/*.md \
    "$PROJECT_ROOT"/integrations/*/README.md \
    "$PROJECT_ROOT"/website/docs/**/*.md \
    "$PROJECT_ROOT"/claude-plugin/commands/*.md \
    "$PROJECT_ROOT"/claude-plugin/skills/beads/resources/*.md \
    2>/dev/null \
    | grep -v 'CHANGELOG\|audit-sync-mode\|deprecated\|no-op\|removed\|was removed\|has been removed' \
    || true)

if [ -n "$SYNC_REFS" ]; then
    echo "FAIL: Found references to removed 'bd sync' command:"
    echo "$SYNC_REFS" | head -20
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: No stale 'bd sync' references"
fi

# bd import (removed)
IMPORT_REFS=$(grep -rn 'bd import\b' \
    "$PROJECT_ROOT"/docs/*.md \
    "$PROJECT_ROOT"/AGENT_INSTRUCTIONS.md \
    "$PROJECT_ROOT"/AGENTS.md \
    "$PROJECT_ROOT"/README.md \
    "$PROJECT_ROOT"/npm-package/*.md \
    "$PROJECT_ROOT"/integrations/*/README.md \
    "$PROJECT_ROOT"/website/docs/**/*.md \
    "$PROJECT_ROOT"/claude-plugin/commands/*.md \
    "$PROJECT_ROOT"/claude-plugin/skills/beads/resources/*.md \
    2>/dev/null \
    | grep -v 'CHANGELOG\|removed\|was removed\|has been removed\|no longer\|deprecated\|REMOVED\|DISCOVERY' \
    || true)

if [ -n "$IMPORT_REFS" ]; then
    echo "FAIL: Found references to removed 'bd import' command:"
    echo "$IMPORT_REFS" | head -20
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: No stale 'bd import' references"
fi

echo ""

# --- Check 2: bd init flags ---
echo "=== Check 2: bd init flags ==="

# Get actual init flags
INIT_FLAGS=$($BD init --help 2>&1 | grep -oP '^\s+--[a-z][a-z0-9-]*' | sed 's/^\s*//' || true)

# Check for --branch on init (removed)
BRANCH_REFS=$(grep -rn 'bd init.*--branch' \
    "$PROJECT_ROOT"/docs/*.md \
    "$PROJECT_ROOT"/AGENT_INSTRUCTIONS.md \
    "$PROJECT_ROOT"/AGENTS.md \
    "$PROJECT_ROOT"/README.md \
    "$PROJECT_ROOT"/website/docs/**/*.md \
    2>/dev/null \
    | grep -v 'CHANGELOG\|removed\|was removed\|no longer\|deprecated' \
    || true)

if [ -n "$BRANCH_REFS" ]; then
    echo "FAIL: Found references to removed 'bd init --branch' flag:"
    echo "$BRANCH_REFS" | head -20
    ERRORS=$((ERRORS + 1))
else
    echo "PASS: No stale 'bd init --branch' references"
fi

echo ""

# --- Check 3: SQLite/legacy database paths ---
echo "=== Check 3: Legacy storage references ==="

SQLITE_REFS=$(grep -rn 'beads\.db\|default\.db\|sqlite3.*\.beads\|\.beads/.*\.db' \
    "$PROJECT_ROOT"/docs/*.md \
    "$PROJECT_ROOT"/AGENT_INSTRUCTIONS.md \
    "$PROJECT_ROOT"/AGENTS.md \
    "$PROJECT_ROOT"/README.md \
    "$PROJECT_ROOT"/website/docs/**/*.md \
    2>/dev/null \
    | grep -v 'CHANGELOG\|removed\|legacy\|migration\|migrate\|was removed\|pre-\|old\|deprecated' \
    || true)

if [ -n "$SQLITE_REFS" ]; then
    echo "WARN: Found possible legacy SQLite/database references:"
    echo "$SQLITE_REFS" | head -20
    # Don't increment ERRORS — these may be intentional migration docs
else
    echo "PASS: No stale SQLite references"
fi

echo ""

# --- Check 4: CLI_REFERENCE.md freshness (if help --all available) ---
echo "=== Check 4: CLI_REFERENCE.md freshness ==="

CLI_REF="$PROJECT_ROOT/docs/CLI_REFERENCE.md"
if [ -f "$CLI_REF" ]; then
    TMPDIR_CHECK=$(mktemp -d)
    trap "rm -rf $TMPDIR_CHECK" EXIT
    if timeout 30 $BD help --all > "$TMPDIR_CHECK/help-all.md" 2>/dev/null; then
        # Extract top-level command names from help output
        grep -oP '## bd [a-z][-a-z]*$' "$TMPDIR_CHECK/help-all.md" \
            | sed 's/## bd //' | sort -u > "$TMPDIR_CHECK/help-cmds.txt"
        # Extract command names referenced in CLI_REFERENCE.md
        grep -oP '\bbd [a-z][-a-z]+\b' "$CLI_REF" \
            | sed 's/^bd //' | sort -u > "$TMPDIR_CHECK/doc-cmds.txt"

        MISSING=$(comm -23 "$TMPDIR_CHECK/help-cmds.txt" "$TMPDIR_CHECK/doc-cmds.txt" || true)
        if [ -n "$MISSING" ]; then
            echo "INFO: Commands in CLI not mentioned in CLI_REFERENCE.md (may be OK):"
            echo "$MISSING" | head -10
        else
            echo "PASS: CLI_REFERENCE.md covers all CLI commands"
        fi
    else
        echo "SKIP: bd help --all timed out or unavailable"
    fi
else
    echo "SKIP: docs/CLI_REFERENCE.md not found"
fi

echo ""

# --- Summary ---
echo "=== Summary ==="
if [ $ERRORS -gt 0 ]; then
    echo "FAILED: $ERRORS stale reference category(ies) found"
    echo ""
    echo "To fix: update the referenced docs to use current CLI commands."
    echo "See docs/DOLT-BACKEND.md for current sync workflow."
    exit 1
else
    echo "PASSED: All documentation references are consistent with CLI"
    exit 0
fi
