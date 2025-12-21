#!/bin/bash
set -e

# =============================================================================
# MOLECULE WORKFLOW (Recommended)
# =============================================================================
#
# This script handles the mechanical version updates, but the full release
# workflow is captured as a molecule. For guided, resumable releases:
#
#   bd template instantiate bd-6s61 --var version=X.Y.Z --assignee <identity>
#
# This creates child beads for each release step. The molecule survives
# session restarts - any agent can pick up where another left off.
#
# The molecule (bd-6s61 "Version Bump: {{version}}") includes:
#   1. Update CHANGELOG.md and info.go
#   2. Run this script (bump-version.sh)
#   3. Run tests and linting
#   4. Commit, tag, push
#   5. Update Homebrew formula
#   6. Verify installation
#
# Until bd mol bond is implemented (bd-usro), use bd molecule instantiate.
# =============================================================================

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Usage message
usage() {
    echo "Usage: $0 <version> [--commit] [--tag] [--push] [--install] [--upgrade-mcp]"
    echo ""
    echo "Bump version across all beads components."
    echo ""
    echo "Arguments:"
    echo "  <version>      Semantic version (e.g., 0.9.3, 1.0.0)"
    echo "  --commit       Automatically create a git commit (optional)"
    echo "  --tag          Create annotated git tag after commit (requires --commit)"
    echo "  --push         Push commit and tag to origin (requires --commit and --tag)"
    echo "  --install      Rebuild and install bd binary to GOPATH/bin after version bump"
    echo "  --upgrade-mcp  Upgrade local beads-mcp installation via pip after version bump"
    echo ""
    echo "Examples:"
    echo "  $0 0.9.3                            # Update versions and show diff"
    echo "  $0 0.9.3 --install                  # Update versions and rebuild/install bd"
    echo "  $0 0.9.3 --upgrade-mcp              # Update versions and upgrade beads-mcp"
    echo "  $0 0.9.3 --commit                   # Update versions and commit"
    echo "  $0 0.9.3 --commit --tag             # Update, commit, and tag"
    echo "  $0 0.9.3 --commit --tag --push      # Full release preparation"
    echo "  $0 0.9.3 --commit --install --upgrade-mcp  # Update, commit, and install all"
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

# Update CHANGELOG.md: move [Unreleased] to [version] and create new [Unreleased]
update_changelog() {
    local version=$1
    local date=$(date +%Y-%m-%d)

    if [ ! -f "CHANGELOG.md" ]; then
        echo -e "${YELLOW}Warning: CHANGELOG.md not found, skipping${NC}"
        return
    fi

    # Check if there's an [Unreleased] section
    if ! grep -q "## \[Unreleased\]" CHANGELOG.md; then
        echo -e "${YELLOW}Warning: No [Unreleased] section in CHANGELOG.md${NC}"
        echo -e "${YELLOW}You may need to manually update CHANGELOG.md${NC}"
        return
    fi

    # Create a temporary file with the updated changelog
    if [[ "$OSTYPE" == "darwin"* ]]; then
        # macOS
        sed -i '' "s/## \[Unreleased\]/## [Unreleased]\n\n## [$version] - $date/" CHANGELOG.md
    else
        # Linux
        sed -i "s/## \[Unreleased\]/## [Unreleased]\n\n## [$version] - $date/" CHANGELOG.md
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
    AUTO_TAG=false
    AUTO_PUSH=false
    AUTO_INSTALL=false
    AUTO_UPGRADE_MCP=false

    # Parse flags
    shift  # Remove version argument
    while [ $# -gt 0 ]; do
        case "$1" in
            --commit)
                AUTO_COMMIT=true
                ;;
            --tag)
                AUTO_TAG=true
                ;;
            --push)
                AUTO_PUSH=true
                ;;
            --install)
                AUTO_INSTALL=true
                ;;
            --upgrade-mcp)
                AUTO_UPGRADE_MCP=true
                ;;
            *)
                echo -e "${RED}Error: Unknown option '$1'${NC}"
                usage
                ;;
        esac
        shift
    done

    # Validate flag dependencies
    if [ "$AUTO_TAG" = true ] && [ "$AUTO_COMMIT" = false ]; then
        echo -e "${RED}Error: --tag requires --commit${NC}"
        exit 1
    fi
    if [ "$AUTO_PUSH" = true ] && [ "$AUTO_TAG" = false ]; then
        echo -e "${RED}Error: --push requires --tag${NC}"
        exit 1
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

    # 5. Update integrations/beads-mcp/src/beads_mcp/__init__.py
    echo "  • integrations/beads-mcp/src/beads_mcp/__init__.py"
    update_file "integrations/beads-mcp/src/beads_mcp/__init__.py" \
        "__version__ = \"$CURRENT_VERSION\"" \
        "__version__ = \"$NEW_VERSION\""

    # 6. Update README.md
    echo "  • README.md"
    update_file "README.md" \
        "Alpha (v$CURRENT_VERSION)" \
        "Alpha (v$NEW_VERSION)"

    # 7. Update PLUGIN.md version requirements (if exists)
    if [ -f "PLUGIN.md" ]; then
        echo "  • PLUGIN.md"
        update_file "PLUGIN.md" \
            "Plugin $CURRENT_VERSION requires bd CLI $CURRENT_VERSION+" \
            "Plugin $NEW_VERSION requires bd CLI $NEW_VERSION+"
    fi

    # 8. Update npm-package/package.json
    echo "  • npm-package/package.json"
    update_file "npm-package/package.json" \
        "\"version\": \"$CURRENT_VERSION\"" \
        "\"version\": \"$NEW_VERSION\""

    # 9. Update hook templates
    echo "  • cmd/bd/templates/hooks/*"
    HOOK_FILES=("pre-commit" "post-merge" "pre-push" "post-checkout")
    for hook in "${HOOK_FILES[@]}"; do
        update_file "cmd/bd/templates/hooks/$hook" \
            "# bd-hooks-version: $CURRENT_VERSION" \
            "# bd-hooks-version: $NEW_VERSION"
    done

    # 10. Update CHANGELOG.md
    echo "  • CHANGELOG.md"
    update_changelog "$NEW_VERSION"

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
        "$(grep '__version__ = ' integrations/beads-mcp/src/beads_mcp/__init__.py | sed 's/.*"\(.*\)".*/\1/')"
        "$(jq -r '.version' npm-package/package.json)"
        "$(grep '# bd-hooks-version: ' cmd/bd/templates/hooks/pre-commit | sed 's/.*: \(.*\)/\1/')"
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

    # Auto-install if requested
    if [ "$AUTO_INSTALL" = true ]; then
        echo "Rebuilding and installing bd..."
        if command -v make &> /dev/null; then
            if make install; then
                echo -e "${GREEN}✓ bd installed to $(go env GOPATH)/bin/bd${NC}"
                echo ""
                INSTALLED_VERSION=$($(go env GOPATH)/bin/bd --version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
                if [ "$INSTALLED_VERSION" = "$NEW_VERSION" ]; then
                    echo -e "${GREEN}✓ Verified: bd --version reports $INSTALLED_VERSION${NC}"
                else
                    echo -e "${YELLOW}⚠ Warning: bd --version reports $INSTALLED_VERSION (expected $NEW_VERSION)${NC}"
                    echo -e "${YELLOW}  You may need to restart your shell or check your PATH${NC}"
                fi
            else
                echo -e "${RED}✗ make install failed${NC}"
                exit 1
            fi
        else
            if go install ./cmd/bd; then
                echo -e "${GREEN}✓ bd installed to $(go env GOPATH)/bin/bd${NC}"
                echo ""
                INSTALLED_VERSION=$($(go env GOPATH)/bin/bd --version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
                if [ "$INSTALLED_VERSION" = "$NEW_VERSION" ]; then
                    echo -e "${GREEN}✓ Verified: bd --version reports $INSTALLED_VERSION${NC}"
                else
                    echo -e "${YELLOW}⚠ Warning: bd --version reports $INSTALLED_VERSION (expected $NEW_VERSION)${NC}"
                    echo -e "${YELLOW}  You may need to restart your shell or check your PATH${NC}"
                fi
            else
                echo -e "${RED}✗ go install failed${NC}"
                exit 1
            fi
        fi
        echo ""
    fi

    # Auto-upgrade MCP if requested
    if [ "$AUTO_UPGRADE_MCP" = true ]; then
        echo "Upgrading local beads-mcp installation..."

        # Try pip first (most common)
        if command -v pip &> /dev/null; then
            if pip install --upgrade beads-mcp; then
                echo -e "${GREEN}✓ beads-mcp upgraded via pip${NC}"
                echo ""
                INSTALLED_MCP_VERSION=$(pip show beads-mcp 2>/dev/null | grep Version | awk '{print $2}')
                if [ "$INSTALLED_MCP_VERSION" = "$NEW_VERSION" ]; then
                    echo -e "${GREEN}✓ Verified: beads-mcp version is $INSTALLED_MCP_VERSION${NC}"
                    echo -e "${YELLOW}  Note: Restart Claude Code or MCP session to use the new version${NC}"
                else
                    echo -e "${YELLOW}⚠ Warning: beads-mcp version is $INSTALLED_MCP_VERSION (expected $NEW_VERSION)${NC}"
                    echo -e "${YELLOW}  This is normal - PyPI package may not be published yet${NC}"
                    echo -e "${YELLOW}  The local source version in pyproject.toml is $NEW_VERSION${NC}"
                fi
            else
                echo -e "${YELLOW}⚠ pip upgrade failed or beads-mcp not installed via pip${NC}"
                echo -e "${YELLOW}  You may need to upgrade manually after publishing to PyPI${NC}"
            fi
        # Try uv tool as fallback
        elif command -v uv &> /dev/null; then
            if uv tool list | grep -q beads-mcp; then
                if uv tool upgrade beads-mcp; then
                    echo -e "${GREEN}✓ beads-mcp upgraded via uv tool${NC}"
                    echo -e "${YELLOW}  Note: Restart Claude Code or MCP session to use the new version${NC}"
                else
                    echo -e "${RED}✗ uv tool upgrade failed${NC}"
                fi
            else
                echo -e "${YELLOW}⚠ beads-mcp not installed via uv tool${NC}"
                echo -e "${YELLOW}  Install with: uv tool install beads-mcp${NC}"
            fi
        else
            echo -e "${YELLOW}⚠ Neither pip nor uv found${NC}"
            echo -e "${YELLOW}  Install beads-mcp with: pip install beads-mcp${NC}"
        fi
        echo ""
    fi

    # Check if cmd/bd/info.go has been updated with the new version
    if ! grep -q "\"$NEW_VERSION\"" cmd/bd/info.go; then
        echo -e "${YELLOW}Warning: cmd/bd/info.go does not contain an entry for $NEW_VERSION${NC}"
        echo -e "${YELLOW}  Please update versionChanges in cmd/bd/info.go with release notes${NC}"
        if [ "$AUTO_COMMIT" = true ]; then
             echo -e "${RED}Error: Cannot auto-commit without updating cmd/bd/info.go${NC}"
             exit 1
        fi
    fi

    # Auto-commit if requested
    if [ "$AUTO_COMMIT" = true ]; then
        echo "Creating git commit..."

        git add cmd/bd/version.go \
                .claude-plugin/plugin.json \
                .claude-plugin/marketplace.json \
                integrations/beads-mcp/pyproject.toml \
                integrations/beads-mcp/src/beads_mcp/__init__.py \
                npm-package/package.json \
                README.md \
                cmd/bd/templates/hooks/*

        # Add PLUGIN.md if it exists
        if [ -f "PLUGIN.md" ]; then
            git add PLUGIN.md
        fi

        # Add CHANGELOG.md if it exists
        if [ -f "CHANGELOG.md" ]; then
            git add CHANGELOG.md
        fi

        git commit -m "chore: Bump version to $NEW_VERSION

Updated all component versions:
- bd CLI: $CURRENT_VERSION → $NEW_VERSION
- Plugin: $CURRENT_VERSION → $NEW_VERSION
- MCP server: $CURRENT_VERSION → $NEW_VERSION
- npm package: $CURRENT_VERSION → $NEW_VERSION
- Documentation: $CURRENT_VERSION → $NEW_VERSION

Generated by scripts/bump-version.sh"

        echo -e "${GREEN}✓ Commit created${NC}"
        echo ""

        # Auto-tag if requested
        if [ "$AUTO_TAG" = true ]; then
            echo "Creating git tag v$NEW_VERSION..."
            git tag -a "v$NEW_VERSION" -m "Release v$NEW_VERSION"
            echo -e "${GREEN}✓ Tag created${NC}"
            echo ""
        fi

        # Auto-push if requested
        if [ "$AUTO_PUSH" = true ]; then
            echo "Pushing to origin..."
            git push origin main
            git push origin "v$NEW_VERSION"
            echo -e "${GREEN}✓ Pushed to origin${NC}"
            echo ""
            echo -e "${GREEN}Release v$NEW_VERSION initiated!${NC}"
            echo "GitHub Actions will build artifacts in ~5-10 minutes."
            echo "Monitor: https://github.com/steveyegge/beads/actions"
        elif [ "$AUTO_TAG" = true ]; then
            echo "Next steps:"
            if [ "$AUTO_INSTALL" = false ]; then
                echo -e "  ${YELLOW}make install${NC}  # ⚠️  IMPORTANT: Rebuild and install bd binary!"
            fi
            if [ "$AUTO_UPGRADE_MCP" = false ]; then
                echo -e "  ${YELLOW}pip install --upgrade beads-mcp${NC}  # ⚠️  After publishing to PyPI"
            fi
            echo "  git push origin main"
            echo "  git push origin v$NEW_VERSION"
        else
            echo "Next steps:"
            if [ "$AUTO_INSTALL" = false ]; then
                echo -e "  ${YELLOW}make install${NC}  # ⚠️  IMPORTANT: Rebuild and install bd binary!"
            fi
            if [ "$AUTO_UPGRADE_MCP" = false ]; then
                echo -e "  ${YELLOW}pip install --upgrade beads-mcp${NC}  # ⚠️  After publishing to PyPI"
            fi
            echo "  git push origin main"
            echo "  git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION'"
            echo "  git push origin v$NEW_VERSION"
        fi
    else
        echo "Review the changes above. To commit:"
        if [ "$AUTO_INSTALL" = false ]; then
            echo -e "  ${YELLOW}make install${NC}  # ⚠️  IMPORTANT: Rebuild and install bd binary first!"
            echo ""
        fi
        if [ "$AUTO_UPGRADE_MCP" = false ]; then
            echo -e "  ${YELLOW}pip install --upgrade beads-mcp${NC}  # ⚠️  After publishing to PyPI"
            echo ""
        fi
        echo "  git add -A"
        echo "  git commit -m 'chore: Bump version to $NEW_VERSION'"
        echo "  git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION'"
        echo "  git push origin main"
        echo "  git push origin v$NEW_VERSION"
        echo ""
        echo "Or run with flags to automate:"
        echo "  $0 $NEW_VERSION --commit --tag --push --install --upgrade-mcp"
    fi
}

main "$@"
