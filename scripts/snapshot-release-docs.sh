#!/bin/bash
# snapshot-release-docs.sh — Create/refresh the Docusaurus release snapshot for a
# given version so the published docs (version dropdown, default docs route, and
# the llms-full artifact) match the released bd version.
#
# This is the single source of truth for the docs side of a version bump. It is
# called by scripts/update-versions.sh and referenced by the beads-release
# formula, so version.go and the docs snapshot cannot drift apart (the failure
# mode that left main red after the 1.0.5 release).
#
# Usage: scripts/snapshot-release-docs.sh <version>
#   e.g. scripts/snapshot-release-docs.sh 1.0.5
#
# Idempotent: re-running for an existing snapshot only regenerates artifacts and
# re-verifies; it does not duplicate the versioned_docs directory.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

if [ $# -ne 1 ]; then
    echo "Usage: $0 <version>" >&2
    echo "Example: $0 1.0.5" >&2
    exit 1
fi

VERSION="$1"

if ! [[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo -e "${RED}Error: Invalid version format '$VERSION'${NC}" >&2
    echo "Expected: MAJOR.MINOR.PATCH (e.g., 1.0.5)" >&2
    exit 1
fi

if [ ! -f "cmd/bd/version.go" ] || [ ! -f "website/docusaurus.config.ts" ]; then
    echo -e "${RED}Error: must run from repository root${NC}" >&2
    exit 1
fi

if ! command -v npx >/dev/null 2>&1; then
    echo -e "${RED}Error: npx (Node.js) is required to snapshot Docusaurus docs.${NC}" >&2
    echo "Install Node.js, or re-run the version bump with --skip-docs and snapshot separately." >&2
    exit 1
fi

echo -e "${YELLOW}Snapshotting release docs for $VERSION${NC}"

# 1. Install website dependencies if needed.
if [ ! -d "website/node_modules" ]; then
    echo "  • website: npm ci"
    (cd website && npm ci)
fi

# 2. Create the versioned snapshot if it does not already exist.
if [ -d "website/versioned_docs/version-$VERSION" ]; then
    echo "  • snapshot website/versioned_docs/version-$VERSION already exists (skipping docs:version)"
else
    echo "  • docusaurus docs:version $VERSION"
    (cd website && npx docusaurus docs:version "$VERSION")
fi

# 3. Point the site default at the new release snapshot.
echo "  • docusaurus.config.ts lastVersion -> $VERSION"
if [[ "$OSTYPE" == "darwin"* ]]; then
    sed -i '' "s/lastVersion: '[^']*'/lastVersion: '$VERSION'/" website/docusaurus.config.ts
else
    sed -i "s/lastVersion: '[^']*'/lastVersion: '$VERSION'/" website/docusaurus.config.ts
fi

# 4. Regenerate docs artifacts from a freshly built bd so the versioned CLI
#    reference carries the "Reference for bd v$VERSION" label and llms-full.txt
#    reflects the release snapshot.
echo "  • building bd for docs generation"
BD_DOCS="$(pwd)/bd-docs-snapshot"
trap 'rm -f "$BD_DOCS"' EXIT
CGO_ENABLED=0 go build -tags gms_pure_go -o "$BD_DOCS" ./cmd/bd/

echo "  • generate-cli-docs.sh"
./scripts/generate-cli-docs.sh --versioned "$VERSION" "$BD_DOCS"

echo "  • generate-llms-full.sh"
./scripts/generate-llms-full.sh

# 5. Verify consistency (fails non-zero if anything still drifts).
echo ""
./scripts/check-docs-version.sh
./scripts/check-doc-flags.sh "$BD_DOCS"

echo ""
echo -e "${GREEN}✓ Release docs snapshot ready for $VERSION${NC}"
