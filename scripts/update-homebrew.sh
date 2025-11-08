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
1. Waits for GitHub Actions release build to complete (~5 minutes)
2. Fetches checksums for all platform-specific release artifacts
3. Clones/updates the homebrew-beads tap repository
4. Updates the formula with new version and all platform SHA256s
5. Commits and pushes the changes

IMPORTANT: Run this AFTER pushing the git tag to GitHub.
The script will automatically wait for GitHub Actions to finish building.
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

# Step 1: Wait for GitHub Actions and fetch release checksums
echo -e "${YELLOW}Step 1: Waiting for GitHub Actions release to complete...${NC}"
echo "This typically takes ~5 minutes. Checking every 30 seconds..."
echo ""

CHECKSUMS_URL="https://github.com/steveyegge/beads/releases/download/v${VERSION}/checksums.txt"
MAX_RETRIES=15  # 15 attempts * 30s = 7.5 minutes max wait
RETRY_DELAY=30
CHECKSUMS=""

for i in $(seq 1 $MAX_RETRIES); do
    echo -n "Attempt $i/$MAX_RETRIES: Checking for release artifacts... "
    
    if CHECKSUMS=$(curl -sL "$CHECKSUMS_URL" 2>/dev/null); then
        if echo "$CHECKSUMS" | grep -q "darwin_arm64"; then
            echo -e "${GREEN}✓ Found!${NC}"
            break
        fi
    fi
    
    if [ $i -lt $MAX_RETRIES ]; then
        echo -e "${YELLOW}Not ready yet, waiting ${RETRY_DELAY}s...${NC}"
        sleep $RETRY_DELAY
    else
        echo -e "${RED}✗ Failed to fetch checksums after waiting $(($MAX_RETRIES * $RETRY_DELAY))s${NC}"
        echo ""
        echo "Possible issues:"
        echo "  • GitHub Actions release workflow is still running"
        echo "  • Git tag was not pushed: git push origin v${VERSION}"
        echo "  • Release workflow failed (check: https://github.com/steveyegge/beads/actions)"
        echo ""
        exit 1
    fi
done

echo ""
echo -e "${GREEN}✓ Release artifacts ready${NC}"
echo ""
echo -e "${YELLOW}Extracting platform SHA256s...${NC}"

# Extract SHA256s for each platform
SHA256_DARWIN_ARM64=$(echo "$CHECKSUMS" | grep "darwin_arm64.tar.gz" | cut -d' ' -f1)
SHA256_DARWIN_AMD64=$(echo "$CHECKSUMS" | grep "darwin_amd64.tar.gz" | cut -d' ' -f1)
SHA256_LINUX_AMD64=$(echo "$CHECKSUMS" | grep "linux_amd64.tar.gz" | cut -d' ' -f1)
SHA256_LINUX_ARM64=$(echo "$CHECKSUMS" | grep "linux_arm64.tar.gz" | cut -d' ' -f1)

# Validate we got all required checksums
if [ -z "$SHA256_DARWIN_ARM64" ] || [ -z "$SHA256_DARWIN_AMD64" ] || [ -z "$SHA256_LINUX_AMD64" ]; then
    echo -e "${RED}✗ Failed to extract all required SHA256s${NC}"
    echo "darwin_arm64: $SHA256_DARWIN_ARM64"
    echo "darwin_amd64: $SHA256_DARWIN_AMD64"
    echo "linux_amd64: $SHA256_LINUX_AMD64"
    exit 1
fi

echo "  darwin_arm64: $SHA256_DARWIN_ARM64"
echo "  darwin_amd64: $SHA256_DARWIN_AMD64"
echo "  linux_amd64:  $SHA256_LINUX_AMD64"
if [ -n "$SHA256_LINUX_ARM64" ]; then
    echo "  linux_arm64:  $SHA256_LINUX_ARM64"
fi
echo ""

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

# Update version number (line 4)
sed -i.tmp "s/version \"[0-9.]*\"/version \"${VERSION}\"/" "$FORMULA_FILE"

# Update SHA256s - need to handle the multi-platform structure
# We'll use awk for more precise control since the formula has multiple sha256 lines

awk -v version="$VERSION" \
    -v sha_darwin_arm64="$SHA256_DARWIN_ARM64" \
    -v sha_darwin_amd64="$SHA256_DARWIN_AMD64" \
    -v sha_linux_amd64="$SHA256_LINUX_AMD64" \
    -v sha_linux_arm64="$SHA256_LINUX_ARM64" '
BEGIN { in_macos_arm=0; in_macos_amd=0; in_linux_arm=0; in_linux_amd=0 }
/on_macos do/ { in_macos=1; next }
/on_linux do/ { in_macos=0; in_linux=1; next }
/if Hardware::CPU.arm\?/ { 
    if (in_macos) in_macos_arm=1
    if (in_linux) in_linux_arm=1
    next
}
/else/ {
    if (in_macos) { in_macos_arm=0; in_macos_amd=1 }
    if (in_linux) { in_linux_arm=0; in_linux_amd=1 }
    next
}
/end/ { in_macos_arm=0; in_macos_amd=0; in_linux_arm=0; in_linux_amd=0; in_macos=0; in_linux=0 }
/sha256/ {
    if (in_macos_arm) { print "      sha256 \"" sha_darwin_arm64 "\""; next }
    if (in_macos_amd) { print "      sha256 \"" sha_darwin_amd64 "\""; next }
    if (in_linux_arm) { print "      sha256 \"" sha_linux_arm64 "\""; next }
    if (in_linux_amd) { print "      sha256 \"" sha_linux_amd64 "\""; next }
}
{ print }
' "$FORMULA_FILE" > "${FORMULA_FILE}.new"

mv "${FORMULA_FILE}.new" "$FORMULA_FILE"
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
