#!/bin/bash
#
# bd-version-check.sh - Automatic bd upgrade detection for AI agent sessions
#
# This script detects when bd (beads) has been upgraded and automatically shows
# what changed, helping AI agents adapt their workflows without manual intervention.
#
# FEATURES:
# - Detects bd version changes by comparing to last-seen version
# - Shows 'bd info --whats-new' output when upgrade detected
# - Auto-updates git hooks if outdated
# - Persists version in .beads/metadata.json
# - Zero bd code changes required - works today!
#
# INTEGRATION:
# Add this script to your AI environment's session startup:
#
# Claude Code:
#   Add to .claude/hooks/session-start (if supported)
#   Or manually source at beginning of work
#
# GitHub Copilot:
#   Add to your shell initialization (.bashrc, .zshrc)
#   Or manually run at session start
#
# Cursor:
#   Add to workspace settings or shell init
#
# Generic:
#   source /path/to/bd-version-check.sh
#
# USAGE:
#   # Option 1: Source it (preferred)
#   source examples/startup-hooks/bd-version-check.sh
#
#   # Option 2: Execute it
#   bash examples/startup-hooks/bd-version-check.sh
#
# REQUIREMENTS:
# - bd (beads) installed and in PATH
# - jq for JSON manipulation
# - .beads directory exists in current project
#

# Exit early if not in a beads project
if [ ! -d ".beads" ]; then
  return 0 2>/dev/null || exit 0
fi

# Check if bd is installed
if ! command -v bd &> /dev/null; then
  return 0 2>/dev/null || exit 0
fi

# Check if jq is installed (required for JSON manipulation)
if ! command -v jq &> /dev/null; then
  echo "⚠️  bd-version-check: jq not found. Install jq to enable automatic upgrade detection."
  return 0 2>/dev/null || exit 0
fi

# Get current bd version
CURRENT_VERSION=$(bd --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)

if [ -z "$CURRENT_VERSION" ]; then
  # bd command failed, skip
  return 0 2>/dev/null || exit 0
fi

# Path to metadata file
METADATA_FILE=".beads/metadata.json"

# Initialize metadata.json if it doesn't exist
if [ ! -f "$METADATA_FILE" ]; then
  echo '{"database": "beads.db", "jsonl_export": "beads.jsonl"}' > "$METADATA_FILE"
fi

# Read last-seen version from metadata.json
LAST_VERSION=$(jq -r '.last_bd_version // "unknown"' "$METADATA_FILE" 2>/dev/null)

# Detect version change
if [ "$CURRENT_VERSION" != "$LAST_VERSION" ] && [ "$LAST_VERSION" != "unknown" ]; then
  echo ""
  echo "🔄 bd upgraded: $LAST_VERSION → $CURRENT_VERSION"
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

  # Show what's new
  bd info --whats-new 2>/dev/null || echo "⚠️  Could not fetch what's new (run 'bd info --whats-new' manually)"

  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  echo "💡 Review changes above and adapt your workflow accordingly"
  echo ""
fi

# Check for outdated git hooks (works even if version didn't change)
if bd hooks list 2>&1 | grep -q "outdated"; then
  echo "🔧 Git hooks outdated. Updating to match bd v$CURRENT_VERSION..."
  if bd hooks install 2>/dev/null; then
    echo "✓ Git hooks updated successfully"
  else
    echo "⚠️  Failed to update git hooks. Run 'bd hooks install' manually."
  fi
  echo ""
fi

# Update metadata.json with current version
# Use a temp file to avoid corruption if jq fails
TEMP_FILE=$(mktemp)
if jq --arg v "$CURRENT_VERSION" '.last_bd_version = $v' "$METADATA_FILE" > "$TEMP_FILE" 2>/dev/null; then
  mv "$TEMP_FILE" "$METADATA_FILE"
else
  # jq failed, clean up temp file
  rm -f "$TEMP_FILE"
fi

# Clean exit for sourcing
return 0 2>/dev/null || exit 0
