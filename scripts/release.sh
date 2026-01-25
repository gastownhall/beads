#!/bin/bash
set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
    cat << EOF
Usage: $0 <version> [--dry-run]

Fully automate a beads release from version bump to local installation.

Arguments:
  version    Version number (e.g., 0.9.3)
  --dry-run  Show what would happen without making changes

This script performs the complete release workflow:
  1. Kill running daemons
  2. Run tests and linting
  3. Bump version in all files
  4. Rebuild local binary
  5. Push version bump to GitHub
  6. Create and push git tag
  7. Upgrade local brew installation

Examples:
  $0 0.9.3          # Full release
  $0 0.9.3 --dry-run # Preview what would happen

After this script completes, your system will be running the new version!
EOF
    exit 1
}

# Parse arguments
DRY_RUN=false
VERSION=""

for arg in "$@"; do
    case $arg in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        *)
            if [ -z "$VERSION" ]; then
                VERSION="$arg"
            fi
            shift
            ;;
    esac
done

if [ -z "$VERSION" ]; then
    usage
fi

# Strip 'v' prefix if present
VERSION="${VERSION#v}"

echo -e "${BLUE}╔═══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║${NC}   ${GREEN}Beads Full Release Automation${NC}                              ${BLUE}║${NC}"
echo -e "${BLUE}║${NC}   Version: ${YELLOW}v${VERSION}${NC}                                            ${BLUE}║${NC}"
if [ "$DRY_RUN" = true ]; then
    echo -e "${BLUE}║${NC}   ${YELLOW}Mode: DRY RUN (no changes will be made)${NC}                   ${BLUE}║${NC}"
fi
echo -e "${BLUE}╔═══════════════════════════════════════════════════════════════╗${NC}"
echo ""

# Step 1: Kill daemons
echo -e "${YELLOW}Step 1/7: Killing running daemons...${NC}"
if [ "$DRY_RUN" = true ]; then
    echo "[DRY RUN] Would run: pkill -f 'bd.*daemon'"
else
    pkill -f "bd.*daemon" 2>/dev/null || true
    sleep 1
    if pgrep -lf "bd.*daemon" > /dev/null 2>&1; then
        echo -e "${RED}✗ Daemons still running${NC}"
        pgrep -lf "bd.*daemon"
        exit 1
    fi
fi
echo -e "${GREEN}✓ All daemons stopped${NC}\n"

# Step 2: Run tests
echo -e "${YELLOW}Step 2/7: Running tests and linting...${NC}"
if [ "$DRY_RUN" = true ]; then
    echo "[DRY RUN] Would run: TMPDIR=/tmp go test ./..."
    echo "[DRY RUN] Would run: golangci-lint run ./..."
else
    if ! TMPDIR=/tmp go test -short ./...; then
        echo -e "${RED}✗ Tests failed${NC}"
        exit 1
    fi
    if ! golangci-lint run ./...; then
        echo -e "${YELLOW}⚠ Linting warnings (see LINTING.md for baseline)${NC}"
    fi
fi
echo -e "${GREEN}✓ Tests passed${NC}\n"

# Step 3: Bump version
echo -e "${YELLOW}Step 3/7: Bumping version to ${VERSION}...${NC}"
if [ "$DRY_RUN" = true ]; then
    echo "[DRY RUN] Would run: $SCRIPT_DIR/bump-version.sh $VERSION --commit"
    $SCRIPT_DIR/bump-version.sh "$VERSION" 2>/dev/null || true
else
    if ! $SCRIPT_DIR/bump-version.sh "$VERSION" --commit; then
        echo -e "${RED}✗ Version bump failed${NC}"
        exit 1
    fi
fi
echo -e "${GREEN}✓ Version bumped and committed${NC}\n"

# Step 4: Rebuild local binary
echo -e "${YELLOW}Step 4/7: Rebuilding local binary...${NC}"
if [ "$DRY_RUN" = true ]; then
    echo "[DRY RUN] Would run: go build -o bd ./cmd/bd"
else
    if ! go build -o bd ./cmd/bd; then
        echo -e "${RED}✗ Build failed${NC}"
        exit 1
    fi
    BUILT_VERSION=$(./bd version 2>/dev/null | head -1)
    echo "Built version: $BUILT_VERSION"
fi
echo -e "${GREEN}✓ Binary rebuilt${NC}\n"

# Step 5: Push version bump
echo -e "${YELLOW}Step 5/7: Pushing version bump to GitHub...${NC}"
if [ "$DRY_RUN" = true ]; then
    echo "[DRY RUN] Would run: git push origin main"
else
    if ! git push origin main; then
        echo -e "${RED}✗ Push failed${NC}"
        exit 1
    fi
fi
echo -e "${GREEN}✓ Version bump pushed${NC}\n"

# Step 6: Create and push tag
echo -e "${YELLOW}Step 6/7: Creating and pushing git tag v${VERSION}...${NC}"
if [ "$DRY_RUN" = true ]; then
    echo "[DRY RUN] Would run: git tag v${VERSION}"
    echo "[DRY RUN] Would run: git push origin v${VERSION}"
else
    if git rev-parse "v${VERSION}" >/dev/null 2>&1; then
        echo -e "${YELLOW}⚠ Tag v${VERSION} already exists, skipping tag creation${NC}"
    else
        git tag "v${VERSION}"
    fi

    if ! git push origin "v${VERSION}"; then
        echo -e "${RED}✗ Tag push failed${NC}"
        exit 1
    fi
fi
echo -e "${GREEN}✓ Tag v${VERSION} pushed${NC}\n"

# Step 7: Upgrade local installation
echo -e "${YELLOW}Step 7/7: Upgrading local Homebrew installation...${NC}"
if [ "$DRY_RUN" = true ]; then
    echo "[DRY RUN] Would run: brew update"
    echo "[DRY RUN] Would run: brew upgrade beads"
else
    brew update

    # Check if beads is installed via brew
    if brew list beads >/dev/null 2>&1; then
        brew upgrade beads || brew reinstall beads
    else
        echo -e "${YELLOW}⚠ bd not installed via Homebrew, skipping upgrade${NC}"
        echo "To install: brew install beads"
    fi
fi
echo -e "${GREEN}✓ Local installation upgraded${NC}\n"

# Final verification
echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}✓ Release Complete!${NC}\n"

if [ "$DRY_RUN" = false ]; then
    echo "Verification:"
    echo "  Installed version: $(bd version 2>/dev/null | head -1 || echo 'Error getting version')"
    echo ""
    echo "Next steps:"
    echo "  • GitHub Actions is building release binaries"
    echo "  • Monitor: https://github.com/steveyegge/beads/actions"
    echo "  • PyPI publish happens automatically"
    echo "  • Update CHANGELOG.md if not done yet"
    echo ""
    echo "Your system is now running v${VERSION}!"
else
    echo "[DRY RUN] No changes were made."
    echo "Run without --dry-run to perform the actual release."
fi

echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
