#!/bin/bash
# Check that all version files are in sync
# Run this before committing version bumps

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

# Get the canonical version from version.go
CANONICAL=$(grep 'Version = ' cmd/bd/version.go | sed 's/.*"\(.*\)".*/\1/')

if [ -z "$CANONICAL" ]; then
    echo -e "${RED}❌ Could not read version from cmd/bd/version.go${NC}"
    exit 1
fi

echo "Canonical version (from version.go): $CANONICAL"
echo ""

MISMATCH=0

check_version() {
    local file=$1
    local version=$2
    local description=$3

    if [ "$version" != "$CANONICAL" ]; then
        echo -e "${RED}❌ $description: $version (expected $CANONICAL)${NC}"
        MISMATCH=1
    else
        echo -e "${GREEN}✓ $description: $version${NC}"
    fi
}

# Check all version files
check_version "integrations/beads-mcp/pyproject.toml" \
    "$(grep '^version = ' integrations/beads-mcp/pyproject.toml 2>/dev/null | sed 's/.*"\(.*\)".*/\1/')" \
    "MCP pyproject.toml"

check_version "integrations/beads-mcp/src/beads_mcp/__init__.py" \
    "$(grep '__version__ = ' integrations/beads-mcp/src/beads_mcp/__init__.py 2>/dev/null | sed 's/.*"\(.*\)".*/\1/')" \
    "MCP __init__.py"

check_version ".claude-plugin/plugin.json" \
    "$(jq -r '.version' .claude-plugin/plugin.json 2>/dev/null)" \
    "Claude plugin.json"

check_version ".claude-plugin/marketplace.json" \
    "$(jq -r '.plugins[0].version' .claude-plugin/marketplace.json 2>/dev/null)" \
    "Claude marketplace.json"

check_version "npm-package/package.json" \
    "$(jq -r '.version' npm-package/package.json 2>/dev/null)" \
    "npm package.json"

check_version "cmd/bd/templates/hooks/pre-commit" \
    "$(grep '# bd-hooks-version:' cmd/bd/templates/hooks/pre-commit 2>/dev/null | sed 's/.*: //')" \
    "Hook templates"

echo ""

if [ $MISMATCH -eq 1 ]; then
    echo -e "${RED}❌ Version mismatch detected!${NC}"
    echo ""
    echo "Run: scripts/bump-version.sh $CANONICAL"
    echo "Or manually update the mismatched files."
    exit 1
else
    echo -e "${GREEN}✓ All versions match: $CANONICAL${NC}"
fi
