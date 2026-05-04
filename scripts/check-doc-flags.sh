#!/bin/bash
# check-doc-flags.sh — Validate documentation references against actual CLI flags.
#
# This script catches stale doc references by:
# 1. Extracting all flags from `bd help --all`
# 2. Scanning docs for `bd <command> --<flag>` patterns
# 3. Flagging any that don't exist in the CLI
#
# Also checks for references to known-removed commands.
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
    "$PROJECT_ROOT"/plugins/beads/skills/beads/commands/*.md \
    "$PROJECT_ROOT"/plugins/beads/skills/beads/resources/*.md \
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

# --- Check 4: CLI command docs coverage and freshness ---
echo "=== Check 4: CLI command docs coverage and freshness ==="

CLI_REF="$PROJECT_ROOT/docs/CLI_REFERENCE.md"
if [ -f "$CLI_REF" ]; then
    TMPDIR_CHECK=$(mktemp -d)
    trap "rm -rf $TMPDIR_CHECK" EXIT
    if timeout 30 "$BD" help --list > "$TMPDIR_CHECK/help-cmds.txt" 2>/dev/null; then
        sort -u "$TMPDIR_CHECK/help-cmds.txt" -o "$TMPDIR_CHECK/help-cmds.txt"

        grep -oE '\bbd [a-z][a-z0-9-]*\b' "$CLI_REF" \
            | sed 's/^bd //' | sort -u > "$TMPDIR_CHECK/cli-reference-cmds.txt" || true

        MISSING_CLI_REF=$(comm -23 "$TMPDIR_CHECK/help-cmds.txt" "$TMPDIR_CHECK/cli-reference-cmds.txt" || true)
        if [ -n "$MISSING_CLI_REF" ]; then
            echo "FAIL: Live CLI commands missing from docs/CLI_REFERENCE.md:"
            echo "$MISSING_CLI_REF" | sed 's/^/  bd /' | head -50
            ERRORS=$((ERRORS + 1))
        else
            echo "PASS: docs/CLI_REFERENCE.md covers all live top-level CLI commands"
        fi

        WEBSITE_DIRS=(
            "$PROJECT_ROOT/website/docs/cli-reference"
            "$PROJECT_ROOT/website/versioned_docs/version-1.0.0/cli-reference"
        )
        for dir in "${WEBSITE_DIRS[@]}"; do
            if [ ! -d "$dir" ]; then
                continue
            fi
            grep -rhoE '\bbd [a-z][a-z0-9-]*\b' "$dir"/*.md \
                | sed 's/^bd //' | sort -u > "$TMPDIR_CHECK/website-cmds.txt" || true

            MISSING_WEBSITE=$(comm -23 "$TMPDIR_CHECK/help-cmds.txt" "$TMPDIR_CHECK/website-cmds.txt" || true)
            if [ -n "$MISSING_WEBSITE" ]; then
                echo "FAIL: Live CLI commands missing from ${dir#$PROJECT_ROOT/}:"
                echo "$MISSING_WEBSITE" | sed 's/^/  bd /' | head -50
                ERRORS=$((ERRORS + 1))
            else
                echo "PASS: ${dir#$PROJECT_ROOT/} covers all live top-level CLI commands"
            fi
        done

        if [ -x "$PROJECT_ROOT/scripts/generate-cli-docs.sh" ]; then
            if "$PROJECT_ROOT/scripts/generate-cli-docs.sh" --check "$BD"; then
                echo "PASS: Generated CLI docs are fresh"
            else
                ERRORS=$((ERRORS + 1))
            fi
        else
            echo "FAIL: scripts/generate-cli-docs.sh is missing"
            ERRORS=$((ERRORS + 1))
        fi
    else
        echo "FAIL: bd help --list timed out or unavailable"
        ERRORS=$((ERRORS + 1))
    fi
else
    echo "FAIL: docs/CLI_REFERENCE.md not found"
    ERRORS=$((ERRORS + 1))
fi

echo ""

# --- Summary ---
echo "=== Summary ==="
if [ $ERRORS -gt 0 ]; then
    echo "FAILED: $ERRORS stale reference category(ies) found"
    echo ""
    echo "To fix:"
    echo "  1. Update stale prose references to use current CLI commands."
    echo "  2. Regenerate CLI docs with: ./scripts/generate-cli-docs.sh $BD"
    echo "  3. Re-run: ./scripts/check-doc-flags.sh $BD"
    exit 1
else
    echo "PASSED: All documentation references are consistent with CLI"
    exit 0
fi
