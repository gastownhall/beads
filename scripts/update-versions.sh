#!/bin/bash
set -e

# =============================================================================
# Quick version bump utility (no git operations)
# =============================================================================
#
# Updates version numbers across all beads components without any git
# operations. Use this for local testing or when you want manual control
# over commits.
#
# For full releases with CI gates and verification, use:
#   bd mol wisp beads-release --var version=X.Y.Z
#
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

if [ $# -ne 1 ]; then
    echo "Usage: $0 <version>"
    echo ""
    echo "Updates version numbers across all components (no git operations)."
    echo ""
    echo "Example: $0 0.47.1"
    echo ""
    echo "For full releases, use: bd mol wisp beads-release --var version=X.Y.Z"
    exit 1
fi

NEW_VERSION=$1

# Validate semantic versioning
if ! [[ $NEW_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo -e "${RED}Error: Invalid version format '$NEW_VERSION'${NC}"
    echo "Expected: MAJOR.MINOR.PATCH (e.g., 0.47.1)"
    exit 1
fi

# Check we're in repo root
if [ ! -f "cmd/bd/version.go" ]; then
    echo -e "${RED}Error: Must run from repository root${NC}"
    exit 1
fi

# Get current version
CURRENT_VERSION=$(grep 'Version = ' cmd/bd/version.go | sed 's/.*"\(.*\)".*/\1/')
echo -e "${YELLOW}Bumping: $CURRENT_VERSION → $NEW_VERSION${NC}"
echo ""

# Cross-platform sed helper
update_file() {
    local file=$1
    local old=$2
    local new=$3
    if [[ "$OSTYPE" == "darwin"* ]]; then
        sed -i '' "s|$old|$new|g" "$file"
    else
        sed -i "s|$old|$new|g" "$file"
    fi
}

echo "Updating version files..."

# 1. cmd/bd/version.go
echo "  • cmd/bd/version.go"
update_file "cmd/bd/version.go" "Version = \"$CURRENT_VERSION\"" "Version = \"$NEW_VERSION\""

# 2. Plugin JSON files
echo "  • .claude-plugin/*.json"
update_file "claude-plugin/.claude-plugin/plugin.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""
update_file ".claude-plugin/marketplace.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""

# 3. MCP Python package
echo "  • integrations/beads-mcp/*"
update_file "integrations/beads-mcp/pyproject.toml" "version = \"$CURRENT_VERSION\"" "version = \"$NEW_VERSION\""
update_file "integrations/beads-mcp/src/beads_mcp/__init__.py" "__version__ = \"$CURRENT_VERSION\"" "__version__ = \"$NEW_VERSION\""

# 4. npm package
echo "  • npm-package/package.json"
update_file "npm-package/package.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""

# 5. README badge
echo "  • README.md"
update_file "README.md" "Alpha (v$CURRENT_VERSION)" "Alpha (v$NEW_VERSION)"

# 6. default.nix
echo "  • default.nix"
update_file "default.nix" "version = \"$CURRENT_VERSION\";" "version = \"$NEW_VERSION\";"

# 7. Hook templates
echo "  • cmd/bd/templates/hooks/*"
for hook in pre-commit post-merge pre-push post-checkout; do
    update_file "cmd/bd/templates/hooks/$hook" "# bd-hooks-version: $CURRENT_VERSION" "# bd-hooks-version: $NEW_VERSION"
done

echo ""
echo -e "${GREEN}✓ Versions updated to $NEW_VERSION${NC}"
echo ""
echo "Changed files:"
git diff --stat 2>/dev/null || true
echo ""
echo "Next steps:"
echo "  • Update CHANGELOG.md with release notes"
echo "  • Update cmd/bd/info.go versionChanges"
echo "  • Or use: bd mol wisp beads-release --var version=$NEW_VERSION"
