#!/bin/bash
# Install git hooks from scripts/hooks/ to .git/hooks/

set -e

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOOKS_DIR="$REPO_ROOT/scripts/hooks"
GIT_HOOKS_DIR="$REPO_ROOT/.git/hooks"

if [ ! -d "$GIT_HOOKS_DIR" ]; then
    echo "Error: Not in a git repository"
    exit 1
fi

echo "Installing git hooks..."

for hook in "$HOOKS_DIR"/*; do
    if [ -f "$hook" ]; then
        hook_name=$(basename "$hook")
        echo "  Installing $hook_name"
        cp "$hook" "$GIT_HOOKS_DIR/$hook_name"
        chmod +x "$GIT_HOOKS_DIR/$hook_name"
    fi
done

echo "✓ Git hooks installed successfully"
echo ""
echo "Installed hooks:"
ls -1 "$GIT_HOOKS_DIR" | grep -v ".sample" || true
