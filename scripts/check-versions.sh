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
    local _file=$1
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

check_version "plugins/beads/.claude-plugin/plugin.json" \
    "$(jq -r '.version' plugins/beads/.claude-plugin/plugin.json 2>/dev/null)" \
    "Claude plugin.json"

check_version "plugins/beads/.codex-plugin/plugin.json" \
    "$(jq -r '.version' plugins/beads/.codex-plugin/plugin.json 2>/dev/null)" \
    "Codex plugin.json"

check_version ".claude-plugin/marketplace.json" \
    "$(jq -r '.plugins[0].version' .claude-plugin/marketplace.json 2>/dev/null)" \
    "Claude marketplace.json"

check_version "npm-package/package.json" \
    "$(jq -r '.version' npm-package/package.json 2>/dev/null)" \
    "npm package.json"

# The release workflow's MCP package gate runs `uv sync --locked`, so a stale
# lockfile fails the release only in the tag-triggered run — after the tag
# exists and can no longer be rewritten (this burned the v1.1.1 tag; v1.1.0
# lost its first run the same way). Gate it here so the release-tag pre-push
# hook and CI refuse the stale lock before a tag exists. The pinned-version
# check is dependency-free; the fuller `uv lock --check` runs when uv is
# available and also catches dependency edits made without a relock.
#
# uv.lock records the PEP 440-normalized version (1.1.0-rc.1 → 1.1.0rc1), so
# normalize the canonical form before comparing.
LOCK_EXPECTED=$(printf '%s' "$CANONICAL" | sed -E 's/-rc\.?/rc/')
LOCK_VERSION=$(awk -F '"' '/^name = "beads-mcp"$/ { found=1; next } found && /^version = / { print $2; exit }' integrations/beads-mcp/uv.lock 2>/dev/null)
if [ "$LOCK_VERSION" != "$LOCK_EXPECTED" ]; then
    echo -e "${RED}❌ MCP uv.lock (beads-mcp pin): ${LOCK_VERSION:-missing} (expected $LOCK_EXPECTED) — run: uv lock --directory integrations/beads-mcp${NC}"
    MISMATCH=1
else
    echo -e "${GREEN}✓ MCP uv.lock (beads-mcp pin): $LOCK_VERSION${NC}"
fi

if command -v uv >/dev/null 2>&1; then
    if uv lock --check --directory integrations/beads-mcp >/dev/null 2>&1; then
        echo -e "${GREEN}✓ MCP uv.lock: fresh (uv lock --check)${NC}"
    else
        echo -e "${RED}❌ MCP uv.lock: stale — run: uv lock --directory integrations/beads-mcp${NC}"
        MISMATCH=1
    fi
fi

# Hook templates are now generated dynamically by cmd/bd/hooks.go using the
# Version constant from version.go, so no separate file check is needed.
# (Previously checked cmd/bd/templates/hooks/pre-commit which no longer exists.)

echo ""


echo ""

if [ $MISMATCH -eq 1 ]; then
    echo -e "${RED}❌ Version mismatch detected!${NC}"
    echo ""
    echo "Run: scripts/update-versions.sh $CANONICAL"
    echo "Or manually update the mismatched files."
    exit 1
else
    echo -e "${GREEN}✓ Version files and released-docs policy pass for: $CANONICAL${NC}"
fi
