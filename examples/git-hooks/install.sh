#!/bin/bash
#
# Install bd git hooks
#
# This script copies the bd git hooks to your .git/hooks directory
# and makes them executable.
#
# Usage:
#   ./examples/git-hooks/install.sh

set -e

# Check if we're in a git repository
if [ ! -d .git ]; then
    echo "Error: Not in a git repository root" >&2
    echo "Run this script from the root of your git repository" >&2
    exit 1
fi

# Check if we're in a bd workspace
if [ ! -d .beads ]; then
    echo "Error: Not in a bd workspace" >&2
    echo "Run 'bd init' first" >&2
    exit 1
fi

# Find the script directory (handles being called from anywhere)
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Hooks to install
HOOKS="pre-commit post-merge pre-push"

echo "Installing bd git hooks..."

for hook in $HOOKS; do
    src="$SCRIPT_DIR/$hook"
    dst=".git/hooks/$hook"
    
    if [ ! -f "$src" ]; then
        echo "Warning: Hook $hook not found at $src" >&2
        continue
    fi
    
    # Backup existing hook if present
    if [ -f "$dst" ]; then
        backup="$dst.backup-$(date +%Y%m%d-%H%M%S)"
        echo "  Backing up existing $hook to $backup"
        mv "$dst" "$backup"
    fi
    
    # Copy and make executable
    cp "$src" "$dst"
    chmod +x "$dst"
    echo "  Installed $hook"
done

echo ""
echo "✓ Git hooks installed successfully"
echo ""
echo "Hooks installed:"
echo "  pre-commit  - Flushes pending bd changes to JSONL before commit"
echo "  pre-push    - Blocks push if JSONL has uncommitted changes (bd-my64)"
echo "  post-merge  - Imports updated JSONL after git pull/merge"
echo ""
echo "To uninstall, remove the hooks from .git/hooks/"
