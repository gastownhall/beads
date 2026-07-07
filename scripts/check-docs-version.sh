#!/bin/bash
# Check that the released Docusaurus docs metadata is self-consistent.
#
# Between a version bump and the actual release, cmd/bd/version.go can be ahead
# of the latest released docs snapshot. In normal PR/main CI, keep validating
# the latest released docs without forcing it to match the unreleased binary
# version. Set BEADS_REQUIRE_RELEASE_DOCS=1, or run from a stable v* tag, to
# require the released docs snapshot to match the current binary version.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

if [ ! -f "cmd/bd/version.go" ] || [ ! -f "website/docusaurus.config.ts" ]; then
    echo -e "${RED}Error: must run from repository root${NC}"
    exit 1
fi

CANONICAL=$(grep 'Version = ' cmd/bd/version.go | sed 's/.*"\(.*\)".*/\1/')
if [ -z "$CANONICAL" ]; then
    echo -e "${RED}Could not read version from cmd/bd/version.go${NC}"
    exit 1
fi

MISMATCH=0

check_equal() {
    local description=$1
    local actual=$2
    local expected=$3

    if [ "$actual" != "$expected" ]; then
        echo -e "${RED}❌ $description: $actual (expected $expected)${NC}"
        MISMATCH=1
    else
        echo -e "${GREEN}✓ $description: $actual${NC}"
    fi
}

check_exists() {
    local description=$1
    local path=$2

    if [ ! -e "$path" ]; then
        echo -e "${RED}❌ Missing $description: $path${NC}"
        MISMATCH=1
    else
        echo -e "${GREEN}✓ $description: $path${NC}"
    fi
}

echo "Canonical version (from version.go): $CANONICAL"
echo ""

LATEST_DOCS_VERSION=$(grep -oE '"[0-9]+\.[0-9]+\.[0-9]+"' website/versions.json | head -1 | tr -d '"' || true)
LAST_VERSION=$(grep -oE "lastVersion: '[0-9]+\.[0-9]+\.[0-9]+'" website/docusaurus.config.ts | head -1 | sed "s/.*'\([^']*\)'.*/\1/" || true)
LLMS_VERSION_LABEL=$(grep -oE 'version: [^)]+' website/static/llms-full.txt | head -1 | sed 's/version: //' || true)
CLI_REF_LABEL=""
if [ -n "$LATEST_DOCS_VERSION" ]; then
    CLI_REF_LABEL=$(grep -oE 'Reference for bd v[0-9]+\.[0-9]+\.[0-9]+' "website/versioned_docs/version-$LATEST_DOCS_VERSION/cli-reference/index.md" 2>/dev/null | head -1 | sed 's/Reference for bd v//' || true)
fi

REQUIRE_RELEASE_DOCS="${BEADS_REQUIRE_RELEASE_DOCS:-}"
IS_PRERELEASE=0
if [[ "$CANONICAL" == *-* ]]; then
    IS_PRERELEASE=1
fi
if [ -z "$REQUIRE_RELEASE_DOCS" ] && [ "$IS_PRERELEASE" -eq 0 ] && [[ "${GITHUB_REF:-}" == refs/tags/v* ]]; then
    REQUIRE_RELEASE_DOCS=1
fi
if [ -z "$REQUIRE_RELEASE_DOCS" ] && [ "$IS_PRERELEASE" -eq 0 ] && [ "${GITHUB_REF_TYPE:-}" = "tag" ]; then
    REQUIRE_RELEASE_DOCS=1
fi

check_equal "website/versions.json latest released docs version" "$LATEST_DOCS_VERSION" "$LAST_VERSION"
check_equal "website/docusaurus.config.ts lastVersion" "$LAST_VERSION" "$LATEST_DOCS_VERSION"
check_equal "website/static/llms-full.txt source version label" "$LLMS_VERSION_LABEL" "latest released"
check_equal "versioned CLI reference label" "$CLI_REF_LABEL" "$LATEST_DOCS_VERSION"

check_exists "versioned docs snapshot" "website/versioned_docs/version-$LATEST_DOCS_VERSION"
check_exists "versioned sidebar snapshot" "website/versioned_sidebars/version-$LATEST_DOCS_VERSION-sidebars.json"

case "$REQUIRE_RELEASE_DOCS" in
    1|true|TRUE|yes|YES)
        check_equal "release docs version" "$LATEST_DOCS_VERSION" "$CANONICAL"
        ;;
    *)
        if [ "$LATEST_DOCS_VERSION" != "$CANONICAL" ]; then
            echo "Release docs strict mode is off; canonical version $CANONICAL may be ahead of latest released docs $LATEST_DOCS_VERSION."
        fi
        ;;
esac

echo ""

if [ "$MISMATCH" -ne 0 ]; then
    echo -e "${RED}Docs version policy mismatch detected.${NC}"
    echo ""
    case "$REQUIRE_RELEASE_DOCS" in
        1|true|TRUE|yes|YES)
            echo "To prepare release docs for $CANONICAL:"
            echo "  cd website"
            echo "  npm ci"
            echo "  npx docusaurus docs:version $CANONICAL"
            echo "  cd .."
            echo "  # Ensure website/docusaurus.config.ts lastVersion is '$CANONICAL'"
            echo "  ./scripts/generate-cli-docs.sh --versioned $CANONICAL ./bd"
            echo "  ./scripts/generate-llms-full.sh"
            echo "  BEADS_REQUIRE_RELEASE_DOCS=1 ./scripts/check-docs-version.sh"
            ;;
        *)
            echo "Latest released docs should remain internally consistent."
            echo "For unreleased version bumps, leave website/versions.json and"
            echo "website/docusaurus.config.ts lastVersion on the latest released snapshot."
            ;;
    esac
    exit 1
fi

echo -e "${GREEN}✓ Released docs version policy passed: $LATEST_DOCS_VERSION${NC}"
