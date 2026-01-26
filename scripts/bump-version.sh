#!/bin/bash
# =============================================================================
# DEPRECATED: Use the release molecule instead
# =============================================================================
#
# This script has been replaced by the beads-release formula, which provides
# guided, step-by-step release workflows with proper handoffs and CI gates.
#
# To cut a release:
#
#   bd mol wisp beads-release --var version=X.Y.Z
#
# The molecule will guide you through:
#   1. Preflight checks (clean git, up to date)
#   2. CHANGELOG and info.go updates
#   3. Version bumps across all components
#   4. Git commit, tag, and push
#   5. CI gate (waits for GitHub Actions)
#   6. Verification (GitHub, npm, PyPI)
#   7. Local installation update
#
# For quick local-only version bumps (no release):
#
#   ./scripts/update-versions.sh X.Y.Z
#
# =============================================================================

echo "This script is deprecated."
echo ""
echo "Use the release molecule instead:"
echo ""
echo "  bd mol wisp beads-release --var version=X.Y.Z"
echo ""
echo "For quick local version bumps only:"
echo ""
echo "  ./scripts/update-versions.sh X.Y.Z"
echo ""
exit 1
