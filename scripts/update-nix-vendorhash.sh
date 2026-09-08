#!/bin/bash
set -e

# =============================================================================
# UPDATE NIX VENDORHASH SCRIPT
# =============================================================================
#
# Automatically updates the vendorHash in default.nix after go.mod changes.
# This script:
#   1. Sets vendorHash to a known-bad hash to trigger an error
#   2. Runs nix build to get the actual hash from the error message
#   3. Extracts the correct hash from the error
#   4. Updates default.nix with the correct hash
#   5. Verifies the update by rebuilding
#
# Requirements:
#   - Either nix installed locally, OR
#   - Docker (will automatically use nixos/nix Docker image)
#
# Usage:
#   ./scripts/update-nix-vendorhash.sh
#
# =============================================================================

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}==>${NC} $1"
}

log_success() {
    echo -e "${GREEN}✓${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

log_error() {
    echo -e "${RED}✗${NC} $1" >&2
}

# Check if we're in the repo root
if [ ! -f "default.nix" ]; then
    log_error "Must run from repository root (default.nix not found)"
    exit 1
fi

# Determine if we should use Docker for Nix
USE_DOCKER=false
NIX_CMD="nix"

if ! command -v nix &> /dev/null; then
    log_warning "nix command not found locally"

    # Check if Docker is available
    if command -v docker &> /dev/null; then
        log_info "Docker detected - will use nixos/nix Docker image"
        USE_DOCKER=true

        # Pull the Nix Docker image if not present
        if ! docker image inspect nixos/nix:latest &> /dev/null; then
            log_info "Pulling nixos/nix:latest Docker image..."
            docker pull nixos/nix:latest
        fi
    else
        log_error "Neither nix nor docker found."
        log_error "Please install one of:"
        log_error "  - Nix: https://nixos.org/download.html"
        log_error "  - Docker: https://docs.docker.com/get-docker/"
        exit 1
    fi
fi

# Function to run nix commands (either locally or via Docker)
run_nix() {
    if [ "$USE_DOCKER" = true ]; then
        # Get absolute path of current directory
        ABS_PWD=$(cd "$(pwd)" && pwd)

        # Run nix in Docker container with current directory mounted
        # Mount at both /workspace and the original path (for git worktree compatibility)
        DOCKER_VOLUMES="-v $ABS_PWD:/workspace -v $ABS_PWD:$ABS_PWD"

        # If this is a git worktree, mount the main git directory too
        if [ -f .git ] && grep -q "^gitdir: " .git; then
            GIT_COMMON_DIR=$(git rev-parse --git-common-dir 2>/dev/null || echo "")
            if [ -n "$GIT_COMMON_DIR" ] && [ -d "$GIT_COMMON_DIR" ]; then
                # Mount the main git directory at the same path
                DOCKER_VOLUMES="$DOCKER_VOLUMES -v $GIT_COMMON_DIR:$GIT_COMMON_DIR"
            fi
        fi

        docker run --rm \
            $DOCKER_VOLUMES \
            -w /workspace \
            nixos/nix:latest \
            nix "$@" --extra-experimental-features "nix-command flakes"
    else
        nix "$@"
    fi
}

log_info "Updating vendorHash in default.nix..."
echo ""

# Get current vendorHash
CURRENT_HASH=$(grep 'vendorHash = ' default.nix | sed 's/.*"\(.*\)".*/\1/')
log_info "Current vendorHash: $CURRENT_HASH"

# Step 1: Set to a known-bad hash to trigger an error
log_info "Setting temporary bad hash to trigger error..."
FAKE_HASH="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

# Backup the file
cp default.nix default.nix.backup

# Detect sed version (GNU vs BSD)
if sed --version 2>/dev/null | grep -q "GNU sed"; then
    SED_INPLACE="sed -i"
else
    SED_INPLACE="sed -i ''"
fi

# Update to fake hash
$SED_INPLACE "s|vendorHash = \".*\";|vendorHash = \"$FAKE_HASH\";|" default.nix

# Step 2: Run nix build and capture the error
log_info "Building to get actual hash (this will fail intentionally)..."
BUILD_LOG="/tmp/nix-vendorhash-build-$$.log"
run_nix build > "$BUILD_LOG" 2>&1 || true

# Step 3: Extract the actual hash from the error message
# Error format: "error: hash mismatch ... got:    sha256-ACTUAL_HASH" (note: multiple spaces)
# Use portable grep + sed instead of grep -P (which requires GNU grep)
ACTUAL_HASH=$(grep -o 'got:[[:space:]]*sha256-[A-Za-z0-9+/=]*' "$BUILD_LOG" | head -1 | sed 's/got:[[:space:]]*//')

if [ -z "$ACTUAL_HASH" ]; then
    log_error "Failed to extract hash from nix build output"
    log_error "This might mean:"
    log_error "  - Go dependencies haven't changed (no update needed)"
    log_error "  - There's a different build error"
    echo ""
    log_info "Build output (last 30 lines):"
    tail -30 "$BUILD_LOG"
    echo ""
    log_info "Full build log saved to: $BUILD_LOG"

    # Restore backup
    mv default.nix.backup default.nix
    log_info "Restored original default.nix"

    # Check if hash is actually the same
    if grep -q "hash mismatch" "$BUILD_LOG"; then
        log_error "Hash mismatch detected but couldn't extract hash"
        log_error "Check the full log: $BUILD_LOG"
        exit 1
    else
        log_success "No hash mismatch - vendorHash is already correct!"
        rm -f "$BUILD_LOG"
        exit 0
    fi
fi

log_success "Extracted actual hash: $ACTUAL_HASH"

# Step 4: Update default.nix with the correct hash
log_info "Updating default.nix with correct hash..."
$SED_INPLACE "s|vendorHash = \".*\";|vendorHash = \"$ACTUAL_HASH\";|" default.nix

# Remove backup
rm default.nix.backup

# Step 5: Verify the update by rebuilding
log_info "Verifying update with a clean build..."
if run_nix build 2>&1 | tee /tmp/nix-build-verify.log; then
    log_success "Build successful! vendorHash updated correctly."
    echo ""
    log_info "Summary:"
    echo "  Old hash: $CURRENT_HASH"
    echo "  New hash: $ACTUAL_HASH"
    echo ""
    log_success "default.nix has been updated"

    # Clean up build logs
    rm -f "$BUILD_LOG" /tmp/nix-build-verify.log
else
    log_error "Build failed after updating hash"
    log_error "See /tmp/nix-build-verify.log for details"
    log_error "Initial build log: $BUILD_LOG"
    exit 1
fi
