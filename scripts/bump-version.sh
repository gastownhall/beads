#!/bin/bash
set -e

# =============================================================================
# VERSION BUMP SCRIPT FOR BEADS
# =============================================================================
#
# This script handles all version bumping and local installation for beads
# releases. It updates version numbers across all components and can install
# everything locally for testing before pushing.
#
# QUICK START (for typical release):
#
#   # 1. Update CHANGELOG.md and cmd/bd/info.go with release notes (manual)
#   # 2. Run version bump with chaos tests and all local installations:
#   ./scripts/bump-version.sh X.Y.Z --run-chaos-tests --commit --tag --push --all
#
# Or step by step:
#   ./scripts/bump-version.sh X.Y.Z --run-chaos-tests  # Run chaos tests first
#   ./scripts/bump-version.sh X.Y.Z --commit --all     # Commit and install
#   git push origin main && git push origin vX.Y.Z     # Push
#
# WHAT --all DOES:
#   --install          - Build bd and install to ~/go/bin AND ~/.local/bin
#   --mcp-local        - Install beads-mcp from local source via uv/pip
#   --restart-daemons  - Restart all bd daemons to pick up new version
#
# MOLECULE WORKFLOW (Alternative):
#   For guided, resumable releases with multiple agents:
#   bd template instantiate bd-6s61 --var version=X.Y.Z --assignee <identity>
#
# IMPORTANT: In multi-clone setups, run from the main clone to avoid git conflicts
# =============================================================================
#
# Multi-clone setups share a beads database at a central location. The bd sync
# command commits from that clone. Running version bumps from a different
# clone causes push conflicts when bd sync tries to push.
#
# Always run releases from the main clone (the one that owns the beads database).
#
# =============================================================================

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Usage message
usage() {
    echo "Usage: $0 <version> [--commit] [--tag] [--push] [--install] [--upgrade-mcp] [--mcp-local] [--restart-daemons] [--run-chaos-tests] [--publish-npm] [--publish-pypi] [--publish-all] [--all] [--allow-staged]"
    echo ""
    echo "Bump version across all beads components."
    echo ""
    echo "Arguments:"
    echo "  <version>        Semantic version (e.g., 0.9.3, 1.0.0)"
    echo "  --commit         Automatically create a git commit (optional)"
    echo "  --tag            Create annotated git tag after commit (requires --commit)"
    echo "  --push           Push commit and tag to origin (requires --commit and --tag)"
    echo "  --install        Rebuild and install bd binary to GOPATH/bin AND ~/.local/bin"
    echo "  --upgrade-mcp    Upgrade local beads-mcp installation via pip after version bump"
    echo "  --mcp-local      Install beads-mcp from local source (for pre-PyPI testing)"
    echo "  --restart-daemons  Restart all bd daemons to pick up new version"
    echo "  --run-chaos-tests  Run chaos/corruption recovery tests before tagging"
    echo "  --publish-npm    Publish npm package to registry (requires npm login)"
    echo "  --publish-pypi   Publish beads-mcp to PyPI (requires TWINE credentials)"
    echo "  --publish-all    Shorthand for --publish-npm --publish-pypi"
    echo "  --all            Shorthand for --install --mcp-local --restart-daemons"
    echo "  --allow-staged   Allow pre-staged release files (CHANGELOG.md, info.go)"
    echo ""
    echo "Examples:"
    echo "  $0 0.9.3                            # Update versions and show diff"
    echo "  $0 0.9.3 --install                  # Update versions and rebuild/install bd"
    echo "  $0 0.9.3 --upgrade-mcp              # Update versions and upgrade beads-mcp from PyPI"
    echo "  $0 0.9.3 --mcp-local                # Update versions and install beads-mcp from local source"
    echo "  $0 0.9.3 --commit                   # Update versions and commit"
    echo "  $0 0.9.3 --commit --tag             # Update, commit, and tag"
    echo "  $0 0.9.3 --commit --tag --push      # Full release preparation"
    echo "  $0 0.9.3 --all                      # Install bd, local MCP, and restart daemons"
    echo "  $0 0.9.3 --commit --all             # Commit and install everything locally"
    echo "  $0 0.9.3 --run-chaos-tests          # Run chaos tests before proceeding"
    echo "  $0 0.9.3 --publish-all              # Publish to npm and PyPI"
    echo "  $0 0.9.3 --allow-staged --commit    # With pre-staged CHANGELOG.md/info.go"
    echo ""
    echo "Pre-staged release notes workflow:"
    echo "  # 1. Edit CHANGELOG.md and cmd/bd/info.go with release notes"
    echo "  # 2. Stage them: git add CHANGELOG.md cmd/bd/info.go"
    echo "  # 3. Run: $0 X.Y.Z --allow-staged --commit --tag --push --all"
    echo ""
    echo "Recommended release command (includes chaos testing):"
    echo "  $0 X.Y.Z --run-chaos-tests --commit --tag --push --all"
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

# Check if all uncommitted changes are to release-related files
# Returns 0 if all changes are release files, 1 otherwise
check_release_files_only() {
    local changed_files
    changed_files=$(git diff --name-only HEAD 2>/dev/null)

    if [ -z "$changed_files" ]; then
        return 0  # No changes
    fi

    # List of expected release files
    local release_files="CHANGELOG.md cmd/bd/info.go"

    for file in $changed_files; do
        local is_release_file=false
        for rf in $release_files; do
            if [ "$file" = "$rf" ]; then
                is_release_file=true
                break
            fi
        done
        if [ "$is_release_file" = false ]; then
            return 1  # Found a non-release file
        fi
    done

    return 0  # All files are release files
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
    AUTO_MCP_LOCAL=false
    AUTO_RESTART_DAEMONS=false
    AUTO_PUBLISH_NPM=false
    AUTO_PUBLISH_PYPI=false
    AUTO_RUN_CHAOS_TESTS=false
    ALLOW_STAGED=false

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
            --mcp-local)
                AUTO_MCP_LOCAL=true
                ;;
            --restart-daemons)
                AUTO_RESTART_DAEMONS=true
                ;;
            --publish-npm)
                AUTO_PUBLISH_NPM=true
                ;;
            --publish-pypi)
                AUTO_PUBLISH_PYPI=true
                ;;
            --publish-all)
                AUTO_PUBLISH_NPM=true
                AUTO_PUBLISH_PYPI=true
                ;;
            --run-chaos-tests)
                AUTO_RUN_CHAOS_TESTS=true
                ;;
            --allow-staged)
                ALLOW_STAGED=true
                ;;
            --all)
                AUTO_INSTALL=true
                AUTO_MCP_LOCAL=true
                AUTO_RESTART_DAEMONS=true
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
    PRE_STAGED_RELEASE_FILES=false
    if ! git diff-index --quiet HEAD --; then
        if [ "$ALLOW_STAGED" = true ] && check_release_files_only; then
            # Pre-staged release files (CHANGELOG.md, info.go) - this is expected workflow
            echo -e "${GREEN}✓ Detected pre-staged release files (CHANGELOG.md and/or info.go)${NC}"
            PRE_STAGED_RELEASE_FILES=true
        else
            echo -e "${YELLOW}Warning: You have uncommitted changes${NC}"
            if [ "$AUTO_COMMIT" = true ]; then
                if [ "$ALLOW_STAGED" = true ]; then
                    echo -e "${RED}Error: --allow-staged only permits CHANGELOG.md and info.go${NC}"
                    echo "Changed files:"
                    git diff --name-only HEAD
                else
                    echo -e "${RED}Error: Cannot auto-commit with existing uncommitted changes${NC}"
                    echo -e "${YELLOW}Tip: Use --allow-staged if changes are to CHANGELOG.md/info.go${NC}"
                fi
                exit 1
            fi
            read -p "Continue anyway? (y/N) " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                exit 1
            fi
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
        GOPATH_BIN="$(go env GOPATH)/bin"
        LOCAL_BIN="$HOME/.local/bin"

        # Build the binary
        if ! go build -o /tmp/bd-new ./cmd/bd; then
            echo -e "${RED}✗ go build failed${NC}"
            exit 1
        fi

        # Codesign the binary on macOS (required to avoid "Killed: 9")
        if [[ "$OSTYPE" == "darwin"* ]]; then
            xattr -cr /tmp/bd-new 2>/dev/null
            codesign -f -s - /tmp/bd-new 2>/dev/null
            echo -e "${GREEN}✓ bd codesigned for macOS${NC}"
        fi

        # Install to GOPATH/bin (typically ~/go/bin)
        cp /tmp/bd-new "$GOPATH_BIN/bd"
        if [[ "$OSTYPE" == "darwin"* ]]; then
            codesign -f -s - "$GOPATH_BIN/bd" 2>/dev/null
        fi
        echo -e "${GREEN}✓ bd installed to $GOPATH_BIN/bd${NC}"

        # Install to ~/.local/bin if it exists or we can create it
        if [ -d "$LOCAL_BIN" ] || mkdir -p "$LOCAL_BIN" 2>/dev/null; then
            cp /tmp/bd-new "$LOCAL_BIN/bd"
            if [[ "$OSTYPE" == "darwin"* ]]; then
                codesign -f -s - "$LOCAL_BIN/bd" 2>/dev/null
            fi
            echo -e "${GREEN}✓ bd installed to $LOCAL_BIN/bd${NC}"
        else
            echo -e "${YELLOW}⚠ Could not install to $LOCAL_BIN (directory doesn't exist)${NC}"
        fi

        # Clean up temp file
        rm -f /tmp/bd-new

        # Verify installation
        echo ""
        echo "Verifying installed versions..."
        if [ -f "$GOPATH_BIN/bd" ]; then
            GOPATH_VERSION=$("$GOPATH_BIN/bd" --version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
            if [ "$GOPATH_VERSION" = "$NEW_VERSION" ]; then
                echo -e "${GREEN}✓ $GOPATH_BIN/bd reports $GOPATH_VERSION${NC}"
            else
                echo -e "${YELLOW}⚠ $GOPATH_BIN/bd reports $GOPATH_VERSION (expected $NEW_VERSION)${NC}"
            fi
        fi
        if [ -f "$LOCAL_BIN/bd" ]; then
            LOCAL_VERSION=$("$LOCAL_BIN/bd" --version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
            if [ "$LOCAL_VERSION" = "$NEW_VERSION" ]; then
                echo -e "${GREEN}✓ $LOCAL_BIN/bd reports $LOCAL_VERSION${NC}"
            else
                echo -e "${YELLOW}⚠ $LOCAL_BIN/bd reports $LOCAL_VERSION (expected $NEW_VERSION)${NC}"
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

    # Install MCP from local source if requested
    if [ "$AUTO_MCP_LOCAL" = true ]; then
        echo "Installing beads-mcp from local source..."
        MCP_DIR="integrations/beads-mcp"

        if [ ! -d "$MCP_DIR" ]; then
            echo -e "${RED}✗ MCP directory not found: $MCP_DIR${NC}"
            exit 1
        fi

        # Use uv tool for installation (preferred for CLI tools)
        if command -v uv &> /dev/null; then
            if uv tool install --reinstall "./$MCP_DIR"; then
                echo -e "${GREEN}✓ beads-mcp installed from local source via uv${NC}"

                # Verify the installed version
                if command -v beads-mcp &> /dev/null; then
                    LOCAL_MCP_VERSION=$(python -c "import beads_mcp; print(beads_mcp.__version__)" 2>/dev/null || echo "unknown")
                    if [ "$LOCAL_MCP_VERSION" = "$NEW_VERSION" ]; then
                        echo -e "${GREEN}✓ Verified: beads-mcp version is $LOCAL_MCP_VERSION${NC}"
                    else
                        echo -e "${YELLOW}⚠ beads-mcp version is $LOCAL_MCP_VERSION (expected $NEW_VERSION)${NC}"
                    fi
                fi
            else
                echo -e "${RED}✗ uv tool install failed${NC}"
                echo -e "${YELLOW}  Try manually: uv tool install --reinstall ./$MCP_DIR${NC}"
            fi
        # Fallback to pip
        elif command -v pip &> /dev/null; then
            if pip install -e "./$MCP_DIR"; then
                echo -e "${GREEN}✓ beads-mcp installed from local source via pip${NC}"
                INSTALLED_MCP_VERSION=$(pip show beads-mcp 2>/dev/null | grep Version | awk '{print $2}')
                if [ "$INSTALLED_MCP_VERSION" = "$NEW_VERSION" ]; then
                    echo -e "${GREEN}✓ Verified: beads-mcp version is $INSTALLED_MCP_VERSION${NC}"
                else
                    echo -e "${YELLOW}⚠ beads-mcp version is $INSTALLED_MCP_VERSION (expected $NEW_VERSION)${NC}"
                fi
            else
                echo -e "${RED}✗ pip install failed${NC}"
            fi
        else
            echo -e "${YELLOW}⚠ Neither uv nor pip found${NC}"
            echo -e "${YELLOW}  Install uv: curl -LsSf https://astral.sh/uv/install.sh | sh${NC}"
        fi
        echo ""
    fi

    # Restart daemons if requested
    if [ "$AUTO_RESTART_DAEMONS" = true ]; then
        echo "Restarting bd daemons..."

        # Use the bd that was just installed (prefer GOPATH/bin which should be in PATH)
        BD_CMD="bd"
        if [ "$AUTO_INSTALL" = true ]; then
            # Use the freshly installed binary
            BD_CMD="$(go env GOPATH)/bin/bd"
        fi

        if command -v "$BD_CMD" &> /dev/null || [ -x "$BD_CMD" ]; then
            if "$BD_CMD" daemons killall --json 2>/dev/null; then
                echo -e "${GREEN}✓ All bd daemons killed (will auto-restart on next bd command)${NC}"
            else
                echo -e "${YELLOW}⚠ No daemons running or daemon killall failed${NC}"
            fi
        else
            echo -e "${YELLOW}⚠ bd command not found, cannot restart daemons${NC}"
        fi
        echo ""
    fi

    # Publish to npm if requested
    if [ "$AUTO_PUBLISH_NPM" = true ]; then
        echo "Publishing npm package..."
        NPM_DIR="npm-package"

        if [ ! -d "$NPM_DIR" ]; then
            echo -e "${RED}✗ npm package directory not found: $NPM_DIR${NC}"
            exit 1
        fi

        cd "$NPM_DIR"

        # Check if logged in
        if ! npm whoami &>/dev/null; then
            echo -e "${YELLOW}⚠ Not logged into npm. Run 'npm adduser' first.${NC}"
            cd ..
            exit 1
        fi

        if npm publish --access public; then
            echo -e "${GREEN}✓ Published @beads/bd@$NEW_VERSION to npm${NC}"
        else
            echo -e "${RED}✗ npm publish failed${NC}"
            cd ..
            exit 1
        fi

        cd ..
        echo ""
    fi

    # Publish to PyPI if requested
    if [ "$AUTO_PUBLISH_PYPI" = true ]; then
        echo "Publishing beads-mcp to PyPI..."
        MCP_DIR="integrations/beads-mcp"

        if [ ! -d "$MCP_DIR" ]; then
            echo -e "${RED}✗ MCP directory not found: $MCP_DIR${NC}"
            exit 1
        fi

        cd "$MCP_DIR"

        # Clean previous builds
        rm -rf dist/ build/ *.egg-info

        # Build the package
        echo "  Building package..."
        if command -v uv &> /dev/null; then
            if ! uv build; then
                echo -e "${RED}✗ uv build failed${NC}"
                cd ../..
                exit 1
            fi
        elif command -v python3 &> /dev/null; then
            if ! python3 -m build; then
                echo -e "${RED}✗ python build failed${NC}"
                cd ../..
                exit 1
            fi
        else
            echo -e "${RED}✗ Neither uv nor python3 found${NC}"
            cd ../..
            exit 1
        fi

        # Upload to PyPI
        echo "  Uploading to PyPI..."
        if command -v uv &> /dev/null; then
            if uv tool run twine upload dist/*; then
                echo -e "${GREEN}✓ Published beads-mcp@$NEW_VERSION to PyPI${NC}"
            else
                echo -e "${RED}✗ PyPI upload failed${NC}"
                echo -e "${YELLOW}  Ensure TWINE_USERNAME and TWINE_PASSWORD are set${NC}"
                echo -e "${YELLOW}  Or configure ~/.pypirc with credentials${NC}"
                cd ../..
                exit 1
            fi
        elif command -v twine &> /dev/null; then
            if twine upload dist/*; then
                echo -e "${GREEN}✓ Published beads-mcp@$NEW_VERSION to PyPI${NC}"
            else
                echo -e "${RED}✗ PyPI upload failed${NC}"
                cd ../..
                exit 1
            fi
        else
            echo -e "${RED}✗ twine not found. Install with: pip install twine${NC}"
            cd ../..
            exit 1
        fi

        cd ../..
        echo ""
    fi

    # Run chaos tests if requested (before commit/tag to catch issues early)
    if [ "$AUTO_RUN_CHAOS_TESTS" = true ]; then
        echo "Running chaos/corruption recovery tests..."
        echo "  (This tests database corruption recovery, may take a few minutes)"
        echo ""

        # Run chaos tests with the chaos build tag
        if go test -tags=chaos -timeout=10m ./cmd/bd/...; then
            echo -e "${GREEN}✓ Chaos tests passed${NC}"
            echo ""
        else
            echo -e "${RED}✗ Chaos tests failed${NC}"
            echo -e "${YELLOW}  Fix the failures before releasing.${NC}"
            exit 1
        fi

        # Also run E2E tests if available
        echo "Running E2E tests..."
        if go test -tags=e2e -timeout=10m ./cmd/bd/...; then
            echo -e "${GREEN}✓ E2E tests passed${NC}"
            echo ""
        else
            echo -e "${RED}✗ E2E tests failed${NC}"
            echo -e "${YELLOW}  Fix the failures before releasing.${NC}"
            exit 1
        fi
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

        # Add pre-staged release files (info.go with release notes)
        if [ "$PRE_STAGED_RELEASE_FILES" = true ]; then
            echo -e "${GREEN}  Including pre-staged release files in commit${NC}"
            git add cmd/bd/info.go 2>/dev/null || true
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
                echo -e "  ${YELLOW}--install${NC}  # Install bd to ~/go/bin AND ~/.local/bin"
            fi
            if [ "$AUTO_MCP_LOCAL" = false ]; then
                echo -e "  ${YELLOW}--mcp-local${NC}  # Install beads-mcp from local source"
            fi
            if [ "$AUTO_RESTART_DAEMONS" = false ]; then
                echo -e "  ${YELLOW}--restart-daemons${NC}  # Restart daemons to pick up new version"
            fi
            echo "  git push origin main"
            echo "  git push origin v$NEW_VERSION"
        else
            echo "Next steps:"
            if [ "$AUTO_INSTALL" = false ]; then
                echo -e "  ${YELLOW}--install${NC}  # Install bd to ~/go/bin AND ~/.local/bin"
            fi
            if [ "$AUTO_MCP_LOCAL" = false ]; then
                echo -e "  ${YELLOW}--mcp-local${NC}  # Install beads-mcp from local source"
            fi
            if [ "$AUTO_RESTART_DAEMONS" = false ]; then
                echo -e "  ${YELLOW}--restart-daemons${NC}  # Restart daemons to pick up new version"
            fi
            echo "  git push origin main"
            echo "  git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION'"
            echo "  git push origin v$NEW_VERSION"
        fi
    else
        echo "Review the changes above."
        echo ""
        echo "Quick local setup (use --all for all local steps):"
        echo -e "  $0 $NEW_VERSION ${YELLOW}--all${NC}"
        echo ""
        echo "Or step by step:"
        if [ "$AUTO_INSTALL" = false ]; then
            echo -e "  ${YELLOW}--install${NC}         # Install bd to ~/go/bin AND ~/.local/bin"
        fi
        if [ "$AUTO_MCP_LOCAL" = false ]; then
            echo -e "  ${YELLOW}--mcp-local${NC}       # Install beads-mcp from local source"
        fi
        if [ "$AUTO_RESTART_DAEMONS" = false ]; then
            echo -e "  ${YELLOW}--restart-daemons${NC} # Restart daemons to pick up new version"
        fi
        echo ""
        echo "Publishing (after tag is pushed, CI handles this automatically):"
        echo -e "  ${YELLOW}--publish-npm${NC}     # Publish @beads/bd to npm"
        echo -e "  ${YELLOW}--publish-pypi${NC}    # Publish beads-mcp to PyPI"
        echo -e "  ${YELLOW}--publish-all${NC}     # Publish to both npm and PyPI"
        echo ""
        echo "Full release (with git commit/tag/push):"
        echo "  $0 $NEW_VERSION --commit --tag --push --all"
        echo ""
        echo "Note: npm/PyPI publishing happens automatically via GitHub Actions"
        echo "when a tag is pushed. Use --publish-* only for manual releases."
    fi
}

main "$@"
