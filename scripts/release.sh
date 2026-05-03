#!/bin/bash
set -euo pipefail

# =============================================================================
# Beads Release - Gateway to the Release Molecule
# =============================================================================
#
# WHY A MOLECULE INSTEAD OF A SCRIPT?
#
# Orchestrators use molecules (not scripts) for multi-step workflows because:
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
#   - Orchestrator docs: PRIMING.md (see "The Batch-Closure Heresy")
#   - HOP docs: CONTEXT.md (see "The Mission" and "MEOW Stack")
#
# THE RELEASE MOLECULE
#
# The `beads-release` formula defines the full release flow:
#   Phase 1: Preflight → version bumps → git push (agent work)
#   Gate:    Await CI completion (async, no polling)
#   Phase 2: Verify GitHub/npm/PyPI releases (parallel)
#   Phase 3: Local install → stale Dolt orphan cleanup
#
# View the full formula:
#   bd formula show beads-release
#
# =============================================================================
#
# ⚠️  CRITICAL: GITHUB API RATE LIMIT WARNING ⚠️
#
# The GitHub API allows 5000 requests/hour. Multiple past releases have burned
# through the ENTIRE rate limit by polling CI status in loops, blocking ALL
# crew members for up to an hour.
#
# DO NOT:
#   - Use `gh run watch` (polls every 3s = 1200 req/hr per invocation)
#   - Run background monitors that poll CI status
#   - Loop on `gh run view` or `gh run list`
#
# INSTEAD:
#   - Let the molecule gate system handle CI waiting
#   - If checking manually: sleep 10-15min, then check ONCE
#   - Budget: 3-5 total API calls for CI monitoring
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
  1. The release molecule (wisp) is created with the configured formula steps
  2. Hook it to start working: bd hook <mol-id>
  3. Or assign to an agent: bd sling <agent> --mol <mol-id>
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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "$REPO_ROOT"

FORMULA_PATH="$REPO_ROOT/.beads/formulas/beads-release.formula.toml"

bd_resolves_repo_formula() {
    local -a candidate=("$@")
    local formula_json
    local formula_source

    if ! formula_json=$("${candidate[@]}" formula show beads-release --json 2>/dev/null); then
        return 1
    fi
    formula_source=$(printf "%s\n" "$formula_json" | sed -n 's/.*"source"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
    [ "$formula_source" = "$FORMULA_PATH" ]
}

BD_FALLBACK_HINT=""
if [ -n "${BD:-}" ]; then
    if ! bd_resolves_repo_formula "$BD"; then
        echo -e "${RED}BD is set but does not resolve the checked-in beads-release formula:${NC}"
        echo "  BD=$BD"
        echo "  Expected formula source: $FORMULA_PATH"
        exit 1
    fi
    BD_CMD=("$BD")
elif command -v bd >/dev/null 2>&1 && bd_resolves_repo_formula bd; then
    BD_CMD=(bd)
elif [ -x "$REPO_ROOT/bd" ] && bd_resolves_repo_formula "$REPO_ROOT/bd"; then
    BD_CMD=("$REPO_ROOT/bd")
else
    BD_CMD=(go run -tags gms_pure_go ./cmd/bd)
    BD_FALLBACK_HINT=" (falling back to go run; run make install for faster releases)"
fi

echo -e "${BLUE}╔═══════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║${NC}   ${GREEN}Beads Release v${VERSION}${NC}                                       ${BLUE}║${NC}"
echo -e "${BLUE}║${NC}   Creating release molecule (not running a batch script)      ${BLUE}║${NC}"
echo -e "${BLUE}╚═══════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo "Using bd: ${BD_CMD[*]}${BD_FALLBACK_HINT}"
echo ""

if [ "$DRY_RUN" = true ]; then
    echo -e "${YELLOW}DRY RUN - showing what would happen${NC}"
    echo ""
    echo "Would create release molecule with:"
    echo "  ${BD_CMD[*]} mol wisp beads-release --var version=${VERSION}"
    echo ""
    if ! FORMULA_OUTPUT=$("${BD_CMD[@]}" formula show beads-release 2>&1); then
        echo -e "${RED}Could not load beads-release formula:${NC}"
        echo "$FORMULA_OUTPUT"
        exit 1
    fi
    FORMULA_STEPS=$(printf "%s\n" "$FORMULA_OUTPUT" | grep -E "[├└]──" || true)
    STEP_COUNT=$(printf "%s\n" "$FORMULA_STEPS" | sed '/^$/d' | wc -l | tr -d ' ')
    if [ "$STEP_COUNT" -eq 0 ]; then
        echo -e "${RED}Could not enumerate beads-release formula steps.${NC}"
        echo "Run: ${BD_CMD[*]} formula show beads-release"
        exit 1
    fi
    echo "The molecule has ${STEP_COUNT} steps:"
    printf "%s\n" "$FORMULA_STEPS" | head -15
    if [ "$STEP_COUNT" -gt 15 ]; then
        echo "   ... ($((STEP_COUNT - 15)) more steps)"
    fi
    echo ""
    echo -e "${BLUE}Why a molecule instead of a script?${NC}"
    echo "  • Each step is a ledger entry (your work history)"
    echo "  • Resumable if interrupted (no restart from scratch)"
    echo "  • Observable via bd activity --follow"
    echo "  • Gates wait for CI without polling"
    echo ""
    echo "Read: Orchestrator docs → PRIMING.md → 'The Batch-Closure Heresy'"
    exit 0
fi

# Create the release molecule (wisp)
echo -e "${YELLOW}Creating release molecule...${NC}"
echo ""

# Create the wisp and capture the output
OUTPUT=$("${BD_CMD[@]}" mol wisp beads-release --var version="${VERSION}" 2>&1)
MOL_ID=$(echo "$OUTPUT" | grep -oE 'bd-wisp-[a-z0-9]+' | head -1)

if [ -z "$MOL_ID" ]; then
    echo -e "${RED}Failed to create release molecule${NC}"
    echo "$OUTPUT"
    exit 1
fi

echo -e "${GREEN}✓ Created release molecule: ${MOL_ID}${NC}"
echo ""

# Show the molecule
"${BD_CMD[@]}" show "$MOL_ID" 2>/dev/null | head -20
echo ""

echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}Release molecule ready!${NC}"
echo ""
echo "Next steps:"
echo ""
echo "  ${YELLOW}Option 1: Work on it yourself${NC}"
echo "    ${BD_CMD[*]} hook ${MOL_ID}"
echo "    # Then follow the steps in ${BD_CMD[*]} show ${MOL_ID}"
echo ""
echo "  ${YELLOW}Option 2: Assign to an agent${NC}"
echo "    ${BD_CMD[*]} sling beads/agents/p1 --mol ${MOL_ID}"
echo ""
echo "  ${YELLOW}Watch progress:${NC}"
echo "    ${BD_CMD[*]} activity --follow"
echo ""
echo "  ${YELLOW}See all steps:${NC}"
echo "    ${BD_CMD[*]} mol steps ${MOL_ID}"
echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}"
