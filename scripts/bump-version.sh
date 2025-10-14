#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Usage message
usage() {
    echo "Usage: $0 <version> [--commit]"
    echo ""
    echo "Bump version across all beads components."
    echo ""
    echo "Arguments:"
    echo "  <version>    Semantic version (e.g., 0.9.3, 1.0.0)"
    echo "  --commit     Automatically create a git commit (optional)"
    echo ""
    echo "Examples:"
    echo "  $0 0.9.3           # Update versions and show diff"
    echo "  $0 0.9.3 --commit  # Update versions and commit"
    exit 1
}

# Validate semantic versioning
validate_version() {
    local version=$1
    if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${RED}Error: Invalid version format '$version'${NC}"
        echo "Expected semantic version format: MAJOR.MINOR.PATCH (e.g., 0.9.3)"
        exit 1
    fi
}

# Get current version from version.go
get_current_version() {
    grep 'Version = ' cmd/bd/version.go | sed 's/.*"\(.*\)".*/\1/'
}

# Update a file with sed (cross-platform compatible)
update_file() {
    local file=$1
    local old_pattern=$2
    local new_text=$3

    if [[ "$OSTYPE" == "darwin"* ]]; then
        # macOS requires -i ''
        sed -i '' "s|$old_pattern|$new_text|g" "$file"
    else
        # Linux
        sed -i "s|$old_pattern|$new_text|g" "$file"
    fi
}

# Main script
main() {
    # Check arguments
    if [ $# -lt 1 ]; then
        usage
    fi

    NEW_VERSION=$1
    AUTO_COMMIT=false

    if [ "$2" == "--commit" ]; then
        AUTO_COMMIT=true
    fi

    # Validate version format
    validate_version "$NEW_VERSION"

    # Get current version
    CURRENT_VERSION=$(get_current_version)

    echo -e "${YELLOW}Bumping version: $CURRENT_VERSION → $NEW_VERSION${NC}"
    echo ""

    # Check if we're in the repo root
    if [ ! -f "cmd/bd/version.go" ]; then
        echo -e "${RED}Error: Must run from repository root${NC}"
        exit 1
    fi

    # Check for uncommitted changes
    if ! git diff-index --quiet HEAD --; then
        echo -e "${YELLOW}Warning: You have uncommitted changes${NC}"
        if [ "$AUTO_COMMIT" = true ]; then
            echo -e "${RED}Error: Cannot auto-commit with existing uncommitted changes${NC}"
            exit 1
        fi
        read -p "Continue anyway? (y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi

    echo "Updating version files..."

    # 1. Update cmd/bd/version.go
    echo "  • cmd/bd/version.go"
    update_file "cmd/bd/version.go" \
        "Version = \"$CURRENT_VERSION\"" \
        "Version = \"$NEW_VERSION\""

    # 2. Update .claude-plugin/plugin.json
    echo "  • .claude-plugin/plugin.json"
    update_file ".claude-plugin/plugin.json" \
        "\"version\": \"$CURRENT_VERSION\"" \
        "\"version\": \"$NEW_VERSION\""

    # 3. Update .claude-plugin/marketplace.json
    echo "  • .claude-plugin/marketplace.json"
    update_file ".claude-plugin/marketplace.json" \
        "\"version\": \"$CURRENT_VERSION\"" \
        "\"version\": \"$NEW_VERSION\""

    # 4. Update integrations/beads-mcp/pyproject.toml
    echo "  • integrations/beads-mcp/pyproject.toml"
    update_file "integrations/beads-mcp/pyproject.toml" \
        "version = \"$CURRENT_VERSION\"" \
        "version = \"$NEW_VERSION\""

    # 5. Update README.md
    echo "  • README.md"
    update_file "README.md" \
        "Alpha (v$CURRENT_VERSION)" \
        "Alpha (v$NEW_VERSION)"

    # 6. Update PLUGIN.md version requirements
    echo "  • PLUGIN.md"
    update_file "PLUGIN.md" \
        "Plugin $CURRENT_VERSION requires bd CLI $CURRENT_VERSION+" \
        "Plugin $NEW_VERSION requires bd CLI $NEW_VERSION+"

    echo ""
    echo -e "${GREEN}✓ Version updated to $NEW_VERSION${NC}"
    echo ""

    # Show diff
    echo "Changed files:"
    git diff --stat
    echo ""

    # Verify all versions match
    echo "Verifying version consistency..."
    VERSIONS=(
        "$(grep 'Version = ' cmd/bd/version.go | sed 's/.*"\(.*\)".*/\1/')"
        "$(jq -r '.version' .claude-plugin/plugin.json)"
        "$(jq -r '.plugins[0].version' .claude-plugin/marketplace.json)"
        "$(grep 'version = ' integrations/beads-mcp/pyproject.toml | head -1 | sed 's/.*"\(.*\)".*/\1/')"
    )

    ALL_MATCH=true
    for v in "${VERSIONS[@]}"; do
        if [ "$v" != "$NEW_VERSION" ]; then
            ALL_MATCH=false
            echo -e "${RED}✗ Version mismatch found: $v${NC}"
        fi
    done

    if [ "$ALL_MATCH" = true ]; then
        echo -e "${GREEN}✓ All versions match: $NEW_VERSION${NC}"
    else
        echo -e "${RED}✗ Version mismatch detected!${NC}"
        exit 1
    fi

    echo ""

    # Auto-commit if requested
    if [ "$AUTO_COMMIT" = true ]; then
        echo "Creating git commit..."

        git add cmd/bd/version.go \
                .claude-plugin/plugin.json \
                .claude-plugin/marketplace.json \
                integrations/beads-mcp/pyproject.toml \
                README.md \
                PLUGIN.md

        git commit -m "chore: Bump version to $NEW_VERSION

Updated all component versions:
- bd CLI: $CURRENT_VERSION → $NEW_VERSION
- Plugin: $CURRENT_VERSION → $NEW_VERSION
- MCP server: $CURRENT_VERSION → $NEW_VERSION
- Documentation: $CURRENT_VERSION → $NEW_VERSION

Generated by scripts/bump-version.sh"

        echo -e "${GREEN}✓ Commit created${NC}"
        echo ""
        echo "Next steps:"
        echo "  git push origin main"
    else
        echo "Review the changes above. To commit:"
        echo "  git add -A"
        echo "  git commit -m 'chore: Bump version to $NEW_VERSION'"
        echo "  git push origin main"
        echo ""
        echo "Or run with --commit to auto-commit:"
        echo "  $0 $NEW_VERSION --commit"
    fi
}

main "$@"
