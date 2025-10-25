#!/bin/bash
set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
TAP_REPO="https://github.com/steveyegge/homebrew-beads"
TAP_DIR="${TAP_DIR:-/tmp/homebrew-beads}"
FORMULA_FILE="Formula/bd.rb"

usage() {
    cat << EOF
Usage: $0 <version>

Automate Homebrew formula update for beads release.

Arguments:
  version    Version number (e.g., 0.9.3 or v0.9.3)

Environment Variables:
  TAP_DIR    Directory for homebrew-beads repo (default: /tmp/homebrew-beads)

Examples:
  $0 0.9.3
  TAP_DIR=~/homebrew-beads $0 v0.9.3

This script:
1. Fetches the tarball SHA256 from GitHub
2. Clones/updates the homebrew-beads tap repository
3. Updates the formula with new version and SHA256
4. Commits and pushes the changes

IMPORTANT: Run this AFTER pushing the git tag to GitHub.
EOF
    exit 1
}

# Parse arguments
if [ $# -ne 1 ]; then
    usage
fi

VERSION="$1"
# Strip 'v' prefix if present
VERSION="${VERSION#v}"

echo -e "${GREEN}=== Homebrew Formula Update for beads v${VERSION} ===${NC}\n"

# Step 1: Fetch SHA256
echo -e "${YELLOW}Step 1: Fetching tarball SHA256...${NC}"
TARBALL_URL="https://github.com/steveyegge/beads/archive/refs/tags/v${VERSION}.tar.gz"
echo "URL: $TARBALL_URL"

# Retry logic for SHA256 fetch (GitHub might need a few seconds to make tarball available)
MAX_RETRIES=5
RETRY_DELAY=3
SHA256=""

for i in $(seq 1 $MAX_RETRIES); do
    if SHA256=$(curl -sL "$TARBALL_URL" | shasum -a 256 | cut -d' ' -f1); then
        if [ -n "$SHA256" ] && [ "${#SHA256}" -eq 64 ]; then
            echo -e "${GREEN}✓ SHA256: $SHA256${NC}\n"
            break
        fi
    fi
    
    if [ $i -lt $MAX_RETRIES ]; then
        echo -e "${YELLOW}Tarball not ready, retrying in ${RETRY_DELAY}s... (attempt $i/$MAX_RETRIES)${NC}"
        sleep $RETRY_DELAY
    else
        echo -e "${RED}✗ Failed to fetch tarball after $MAX_RETRIES attempts${NC}"
        echo "The git tag might not be pushed yet, or GitHub needs more time to generate the tarball."
        echo "Try: git push origin v${VERSION}"
        exit 1
    fi
done

# Step 2: Clone/update tap repository
echo -e "${YELLOW}Step 2: Preparing tap repository...${NC}"
if [ -d "$TAP_DIR" ]; then
    echo "Updating existing repository at $TAP_DIR"
    cd "$TAP_DIR"
    git fetch origin
    git reset --hard origin/main
else
    echo "Cloning repository to $TAP_DIR"
    git clone "$TAP_REPO" "$TAP_DIR"
    cd "$TAP_DIR"
fi
echo -e "${GREEN}✓ Repository ready${NC}\n"

# Step 3: Update formula
echo -e "${YELLOW}Step 3: Updating formula...${NC}"
if [ ! -f "$FORMULA_FILE" ]; then
    echo -e "${RED}✗ Formula file not found: $FORMULA_FILE${NC}"
    exit 1
fi

# Create backup
cp "$FORMULA_FILE" "${FORMULA_FILE}.bak"

# Update version and SHA256 in formula
# The formula has lines like:
#   url "https://github.com/steveyegge/beads/archive/refs/tags/v0.9.2.tar.gz"
#   sha256 "abc123..."

sed -i.tmp "s|archive/refs/tags/v[0-9.]*\.tar\.gz|archive/refs/tags/v${VERSION}.tar.gz|" "$FORMULA_FILE"
sed -i.tmp "s|sha256 \"[a-f0-9]*\"|sha256 \"${SHA256}\"|" "$FORMULA_FILE"
rm -f "${FORMULA_FILE}.tmp"

# Show diff
echo "Changes to $FORMULA_FILE:"
git diff "$FORMULA_FILE" || true
echo -e "${GREEN}✓ Formula updated${NC}\n"

# Step 4: Commit and push
echo -e "${YELLOW}Step 4: Committing changes...${NC}"
git add "$FORMULA_FILE"

if git diff --staged --quiet; then
    echo -e "${YELLOW}⚠ No changes detected - formula might already be up to date${NC}"
    rm -f "${FORMULA_FILE}.bak"
    exit 0
fi

git commit -m "Update bd formula to v${VERSION}"
echo -e "${GREEN}✓ Changes committed${NC}\n"

echo -e "${YELLOW}Step 5: Pushing to GitHub...${NC}"
if git push origin main; then
    echo -e "${GREEN}✓ Formula pushed successfully${NC}\n"
    rm -f "${FORMULA_FILE}.bak"
else
    echo -e "${RED}✗ Failed to push changes${NC}"
    echo "Restoring backup..."
    mv "${FORMULA_FILE}.bak" "$FORMULA_FILE"
    exit 1
fi

# Success message
echo -e "${GREEN}=== Homebrew Formula Update Complete ===${NC}\n"
echo "Next steps:"
echo "  1. Verify the formula update at: https://github.com/steveyegge/homebrew-beads"
echo "  2. Test locally:"
echo "     brew update"
echo "     brew upgrade bd"
echo "     bd version  # Should show v${VERSION}"
echo ""
echo -e "${GREEN}Done!${NC}"
