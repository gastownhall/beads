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

# Back up to a private per-run scratch directory, never to a fixed name in
# the repo root: `default.nix.backup` there is also the conventional name an
# operator gives their own manual backup, and overwriting (then later
# deleting) that file on every run can destroy one on sight. The scratch
# dir is removed on every exit path, handled or not; a handled failure
# restores from it first via restore_original.
SCRATCH_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nix-vendorhash.XXXXXX")
BACKUP="$SCRATCH_DIR/default.nix.orig"
# -p preserves the exact original bytes and mode; a handled failure must
# restore both, not just the content.
cp -p default.nix "$BACKUP"

restore_original() {
    cp -p "$BACKUP" default.nix
}

# A SIGINT/SIGTERM during the long steps (the nix builds) reaches this process
# and its nix child as one group; the child dies, and this script exits
# through the EXIT trap with default.nix still holding the placeholder hash
# (probe build) or an unverified candidate hash (step 5). Plain
# `rm -rf "$SCRATCH_DIR"` there would delete the only backup while leaving
# either in place -- worse than every handled failure, since recovery becomes
# `git checkout default.nix`, which also discards any uncommitted operator
# edits. Neither is a valid end state: VERIFIED is set only once a build has
# verified default.nix as it stands, so restore the original whenever the run
# ends before that. Every handled exit below has already restored or verified
# default.nix by the time this runs, so it is a no-op on every path except an
# interrupted one.
VERIFIED=false
cleanup() {
    if [ "$VERIFIED" != true ] && [ -f "$BACKUP" ] && ! cmp -s "$BACKUP" default.nix; then
        restore_original
        log_info "Restored original default.nix (run interrupted)"
    fi
    rm -rf "$SCRATCH_DIR"
}
trap cleanup EXIT

# Detect sed version (GNU vs BSD). BSD sed requires an explicit backup-suffix
# argument to -i; GNU sed must NOT be given one.
#
# This is a function rather than a SED_INPLACE string because a string cannot
# carry an EMPTY argument. After `SED_INPLACE="sed -i ''"` is word-split the two
# quote characters survive as a literal argument, so BSD sed reads '' as the
# backup SUFFIX and writes `default.nix''` beside the real file on every macOS
# run. Nothing ignores that stray, and it holds the placeholder
# sha256-AAAA... hash, so a `git add -A` can commit a vendorHash that never
# validates.
sed_inplace() {
    if sed --version 2>/dev/null | grep -q "GNU sed"; then
        sed -i "$@"
    else
        sed -i '' "$@"
    fi
}

# Update to fake hash
sed_inplace "s|vendorHash = \".*\";|vendorHash = \"$FAKE_HASH\";|" default.nix

# Step 2: Run nix build and capture the error
log_info "Building to get actual hash (this will fail intentionally)..."
# Per-run log names under ${TMPDIR:-/tmp}, like the scratch dir: a fixed name
# in a shared /tmp collides with another run's (or another user's) log. They
# live outside the scratch dir because a failure keeps the logs it names.
BUILD_LOG="${TMPDIR:-/tmp}/nix-vendorhash-build-$$.log"
VERIFY_LOG="${TMPDIR:-/tmp}/nix-vendorhash-verify-$$.log"
# This build is expected to fail (the fake hash above is designed to trigger
# a hash-mismatch error), so capture its real exit status explicitly instead
# of `|| true`, which would discard it. The exit status (not a "hash
# mismatch" string grep, which can't tell a build that failed for an
# unrelated reason from one that actually succeeded) is what step 3 below
# uses to tell "no update needed" apart from "the build is broken".
set +e
run_nix build > "$BUILD_LOG" 2>&1
BUILD_EXIT=$?
set -e

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
    restore_original
    log_info "Restored original default.nix"

    # Tell "the build genuinely succeeded with the fake hash rejected, so
    # the real hash is already correct" apart from "the build failed for an
    # unrelated reason and never got far enough to report a hash mismatch"
    # by the build's own exit status, not by whether "hash mismatch" happens
    # to appear in the log — a toolchain error or any other failure mode
    # has no such string and was previously misreported as success.
    if [ "$BUILD_EXIT" -eq 0 ]; then
        # The probe build's success only means the FAKE hash didn't trigger
        # a mismatch — it was never a build of the real, restored file. A
        # success exit must actually build that file, not just infer it
        # from the probe.
        log_info "No hash mismatch in the probe build - verifying the restored file builds as-is..."
        RESTORE_BUILD_LOG="${TMPDIR:-/tmp}/nix-vendorhash-restore-build-$$.log"
        set +e
        run_nix build > "$RESTORE_BUILD_LOG" 2>&1
        RESTORE_BUILD_EXIT=$?
        set -e
        if [ "$RESTORE_BUILD_EXIT" -eq 0 ]; then
            VERIFIED=true
            log_success "No hash mismatch - vendorHash is already correct!"
            rm -f "$BUILD_LOG" "$RESTORE_BUILD_LOG"
            exit 0
        else
            log_error "The probe build reported no hash mismatch, but the restored file doesn't build either (exit $RESTORE_BUILD_EXIT) — investigate before retrying"
            log_error "Check the restored-file build log: $RESTORE_BUILD_LOG"
            log_error "Probe build log: $BUILD_LOG"
            exit 1
        fi
    elif grep -q "hash mismatch" "$BUILD_LOG"; then
        log_error "Build reported a hash mismatch (exit $BUILD_EXIT) but the got: line didn't match the extraction regex — Nix's output format may have changed"
        log_error "Check the full log: $BUILD_LOG"
        exit 1
    else
        log_error "Build failed for a reason other than a hash mismatch (exit $BUILD_EXIT)"
        log_error "Check the full log: $BUILD_LOG"
        exit 1
    fi
fi

log_success "Extracted actual hash: $ACTUAL_HASH"

# Step 4: Update default.nix with the correct hash
log_info "Updating default.nix with correct hash..."
sed_inplace "s|vendorHash = \".*\";|vendorHash = \"$ACTUAL_HASH\";|" default.nix

# $BACKUP is kept until step 5 verifies: restore_original below needs it if
# the verification build fails, and the EXIT trap if that build is
# interrupted. The trap removes the scratch dir either way.

# Step 5: Verify the update by rebuilding
log_info "Verifying update with a clean build..."
# Without pipefail, this if-condition's status is tee's (which basically
# always succeeds), not run_nix build's, so a real build failure here was
# reported as "Build successful!". Scoped to just this if (turned back off
# right after) rather than set for the rest of the script: the ACTUAL_HASH
# extraction above relies on `grep -o` legitimately returning non-zero on no
# match, which pipefail would turn into a script abort under `set -e`.
set -o pipefail
if run_nix build 2>&1 | tee "$VERIFY_LOG"; then
    set +o pipefail
    VERIFIED=true
    log_success "Build successful! vendorHash updated correctly."
    echo ""
    log_info "Summary:"
    echo "  Old hash: $CURRENT_HASH"
    echo "  New hash: $ACTUAL_HASH"
    echo ""
    log_success "default.nix has been updated"

    # Clean up build logs
    rm -f "$BUILD_LOG" "$VERIFY_LOG"
else
    set +o pipefail
    log_error "Build failed after updating hash"
    log_error "See $VERIFY_LOG for details"
    log_error "Initial build log: $BUILD_LOG"
    restore_original
    log_info "Restored original default.nix (candidate hash did not verify)"
    exit 1
fi
