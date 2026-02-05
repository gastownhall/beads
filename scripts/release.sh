#!/bin/bash
set -euo pipefail

# =============================================================================
# Beads Release - Gateway to the Release Molecule
# =============================================================================
#
# WHY A MOLECULE INSTEAD OF A SCRIPT?
#
# Gas Town uses molecules (not scripts) for multi-step workflows because:
#
#   1. LEDGER: Each step is a timestamped entry in your work history.
#      Your CV accumulates from completed work. Batch scripts are invisible.
#
#   2. RESUMABILITY: If a release fails at step 17, you resume at step 17.
#      Scripts restart from scratch or require manual state tracking.
#
#   3. OBSERVABILITY: `bd activity --follow` shows real-time progress.
#      Scripts are opaque until they finish (or fail).
#
#   4. GATES: The release molecule waits for CI via gates, not polling.
#      Clean phase handoffs without busy-waiting.
#
# Read more:
#   - ~/gt/docs/PRIMING.md (see "The Batch-Closure Heresy")
#   - ~/hop/docs/CONTEXT.md (see "The Mission" and "MEOW Stack")
#
# THE RELEASE MOLECULE
#
# The `beads-release` formula has 29 steps across 3 phases:
#   Phase 1: Preflight → version bumps → git push (polecat work)
#   Gate:    Await CI completion (async, no polling)
#   Phase 2: Verify GitHub/npm/PyPI releases (parallel)
#   Phase 3: Local install → daemon restart
#
# View the full formula:
#   bd formula show beads-release
#
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

usage() {
    cat << 'EOF'
Usage: release.sh <version> [--dry-run]

Creates a release molecule (wisp) for the specified version.

Arguments:
  version    Version number (e.g., 0.50.0)
  --dry-run  Show what would happen without creating the molecule

This script is a GATEWAY to the beads-release molecule, not a replacement.
It creates the wisp and shows you how to execute it.

Examples:
  ./scripts/release.sh 0.50.0           # Create release molecule
  ./scripts/release.sh 0.50.0 --dry-run # Preview only

After running this script:
  1. The release molecule (wisp) is created with all 29 steps
  2. Hook it to start working: gt hook <mol-id>
  3. Or sling to a polecat: gt sling beads/polecats/p1 <mol-id>
  4. Watch progress: bd activity --follow

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
            ;;
        --help|-h)
            usage
            ;;
        *)
            if [ -z "$VERSION" ]; then
                VERSION="$arg"
            fi
            ;;
    esac
done

if [ -z "$VERSION" ]; then
    usage
fi

# Strip 'v' prefix if present
VERSION="${VERSION#v}"

echo -e "${BLUE}╔═══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║${NC}   ${GREEN}Beads Release v${VERSION}${NC}                                       ${BLUE}║${NC}"
echo -e "${BLUE}║${NC}   Creating release molecule (not running a batch script)      ${BLUE}║${NC}"
echo -e "${BLUE}╚═══════════════════════════════════════════════════════════════╝${NC}"
echo ""

if [ "$DRY_RUN" = true ]; then
    echo -e "${YELLOW}DRY RUN - showing what would happen${NC}"
    echo ""
    echo "Would create release molecule with:"
    echo "  bd mol wisp beads-release --var version=${VERSION}"
    echo ""
    echo "The molecule has 29 steps:"
    bd formula show beads-release 2>/dev/null | grep -E "^   [├└]" | head -15
    echo "   ... (14 more steps)"
    echo ""
    echo -e "${BLUE}Why a molecule instead of a script?${NC}"
    echo "  • Each step is a ledger entry (your work history)"
    echo "  • Resumable if interrupted (no restart from scratch)"
    echo "  • Observable via bd activity --follow"
    echo "  • Gates wait for CI without polling"
    echo ""
    echo "Read: ~/gt/docs/PRIMING.md → 'The Batch-Closure Heresy'"
    exit 0
fi

# Create the release molecule (wisp)
echo -e "${YELLOW}Creating release molecule...${NC}"
echo ""

# Create the wisp and capture the output
OUTPUT=$(bd mol wisp beads-release --var version="${VERSION}" 2>&1)
MOL_ID=$(echo "$OUTPUT" | grep -oE 'bd-wisp-[a-z0-9]+' | head -1)

if [ -z "$MOL_ID" ]; then
    echo -e "${RED}Failed to create release molecule${NC}"
    echo "$OUTPUT"
    exit 1
fi

echo -e "${GREEN}✓ Created release molecule: ${MOL_ID}${NC}"
echo ""

# Show the molecule
bd show "$MOL_ID" 2>/dev/null | head -20
echo ""

echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}Release molecule ready!${NC}"
echo ""
echo "Next steps:"
echo ""
echo "  ${YELLOW}Option 1: Work on it yourself${NC}"
echo "    gt hook ${MOL_ID}"
echo "    # Then follow the steps in bd show ${MOL_ID}"
echo ""
echo "  ${YELLOW}Option 2: Assign to a polecat${NC}"
echo "    gt sling beads/polecats/p1 ${MOL_ID}"
echo ""
echo "  ${YELLOW}Watch progress:${NC}"
echo "    bd activity --follow"
echo ""
echo "  ${YELLOW}See all steps:${NC}"
echo "    bd mol steps ${MOL_ID}"
echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
