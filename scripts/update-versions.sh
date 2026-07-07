#!/bin/bash
set -e

# =============================================================================
# Quick version bump utility (no git operations)
# =============================================================================
#
# Updates version numbers across all beads components without any git
# operations. Use this for local testing or when you want manual control
# over commits.
#
# For full releases with CI gates and verification, use:
#   bd mol wisp beads-release --var version=X.Y.Z
#
# =============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

usage() {
    echo "Usage: $0 <version> [--skip-docs]"
    echo ""
    echo "Updates version numbers across all components (no git operations),"
    echo "and snapshots the Docusaurus release docs so version.go and the docs"
    echo "snapshot cannot drift apart for stable releases."
    echo ""
    echo "  --skip-docs   Skip the Docusaurus snapshot (e.g. on a host without"
    echo "                Node.js). You must then run scripts/snapshot-release-docs.sh"
    echo "                <version> elsewhere before tagging a stable release, or CI"
    echo "                will fail. Prereleases skip docs snapshots by default."
    echo ""
    echo "Examples:"
    echo "  $0 0.47.1"
    echo "  $0 1.1.0-rc.1"
    echo ""
    echo "For full releases, use: bd mol wisp beads-release --var version=X.Y.Z"
}

NEW_VERSION=""
SKIP_DOCS=0
for arg in "$@"; do
    case "$arg" in
        --skip-docs) SKIP_DOCS=1 ;;
        -h|--help) usage; exit 0 ;;
        -*) echo "Unknown option: $arg" >&2; usage; exit 1 ;;
        *)
            if [ -n "$NEW_VERSION" ]; then
                echo "Error: multiple versions given" >&2; usage; exit 1
            fi
            NEW_VERSION="$arg"
            ;;
    esac
done

if [ -z "$NEW_VERSION" ]; then
    usage
    exit 1
fi

# Validate semantic versioning. Accept prerelease identifiers so release
# candidates can be cut without pretending to be stable package releases.
if ! [[ $NEW_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    echo -e "${RED}Error: Invalid version format '$NEW_VERSION'${NC}"
    echo "Expected: MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-prerelease (e.g., 0.47.1 or 1.1.0-rc.1)"
    exit 1
fi

BASE_VERSION="${NEW_VERSION%%-*}"
IS_PRERELEASE=0
if [ "$BASE_VERSION" != "$NEW_VERSION" ]; then
    IS_PRERELEASE=1
fi

# Check we're in repo root
if [ ! -f "cmd/bd/version.go" ]; then
    echo -e "${RED}Error: Must run from repository root${NC}"
    exit 1
fi

# Get current version
CURRENT_VERSION=$(grep 'Version = ' cmd/bd/version.go | sed 's/.*"\(.*\)".*/\1/')
# Base (prerelease-stripped) form of the current version. The Windows PE
# numeric fields (file_version/product_version, manifest version) only ever
# hold the base form, so they must be matched on the base, not on the full
# CURRENT_VERSION which may carry a -rc.N suffix.
CURRENT_BASE="${CURRENT_VERSION%%-*}"
echo -e "${YELLOW}Bumping: $CURRENT_VERSION → $NEW_VERSION${NC}"
echo ""

# Cross-platform sed helper
update_file() {
    local file=$1
    local old=$2
    local new=$3
    if [[ "$OSTYPE" == "darwin"* ]]; then
        sed -i '' "s|$old|$new|g" "$file"
    else
        sed -i "s|$old|$new|g" "$file"
    fi
}

echo "Updating version files..."

# 1. cmd/bd/version.go
echo "  • cmd/bd/version.go"
update_file "cmd/bd/version.go" "Version = \"$CURRENT_VERSION\"" "Version = \"$NEW_VERSION\""

# 2. Plugin JSON files
echo "  • plugin metadata"
update_file "plugins/beads/.claude-plugin/plugin.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""
update_file "plugins/beads/.codex-plugin/plugin.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""
update_file "plugins/beads/.copilot-plugin/plugin.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""
update_file ".claude-plugin/marketplace.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""

# 3. MCP Python package
echo "  • integrations/beads-mcp/*"
update_file "integrations/beads-mcp/pyproject.toml" "version = \"$CURRENT_VERSION\"" "version = \"$NEW_VERSION\""
update_file "integrations/beads-mcp/src/beads_mcp/__init__.py" "__version__ = \"$CURRENT_VERSION\"" "__version__ = \"$NEW_VERSION\""

# 4. npm package
echo "  • npm-package/package.json"
update_file "npm-package/package.json" "\"version\": \"$CURRENT_VERSION\"" "\"version\": \"$NEW_VERSION\""

# 5. README badge
echo "  • README.md"
update_file "README.md" "Alpha (v$CURRENT_VERSION)" "Alpha (v$NEW_VERSION)"

# 6. default.nix
echo "  • default.nix"
update_file "default.nix" "version = \"$CURRENT_VERSION\";" "version = \"$NEW_VERSION\";"

# 7. Hook templates — now generated dynamically by cmd/bd/hooks.go using the
# Version constant from version.go. No template files to update.
# (Previously updated cmd/bd/templates/hooks/* which no longer exist.)

# 8. Windows PE resource metadata
echo "  • cmd/bd/winres/winres.json"
update_file "cmd/bd/winres/winres.json" "\"file_version\": \"$CURRENT_BASE\"" "\"file_version\": \"$BASE_VERSION\""
update_file "cmd/bd/winres/winres.json" "\"product_version\": \"$CURRENT_BASE\"" "\"product_version\": \"$BASE_VERSION\""
update_file "cmd/bd/winres/winres.json" "\"FileVersion\": \"$CURRENT_VERSION\"" "\"FileVersion\": \"$NEW_VERSION\""
update_file "cmd/bd/winres/winres.json" "\"ProductVersion\": \"$CURRENT_VERSION\"" "\"ProductVersion\": \"$NEW_VERSION\""
echo "  • cmd/bd/winres/manifest.xml"
update_file "cmd/bd/winres/manifest.xml" "version=\"$CURRENT_BASE.0\"" "version=\"$BASE_VERSION.0\""

echo ""
echo -e "${GREEN}✓ Version constants updated to $NEW_VERSION${NC}"
echo ""

# Snapshot the Docusaurus release docs as part of the same bump so version.go
# and the published docs cannot diverge. This is the failure mode that left
# main red after the 1.0.5 release (version bumped, docs snapshot missing).
if [ "$SKIP_DOCS" -eq 1 ]; then
    echo -e "${YELLOW}Skipping docs snapshot (--skip-docs).${NC}"
    if [ "$IS_PRERELEASE" -eq 1 ]; then
        echo "  Prerelease CI does not require a stable docs snapshot for $NEW_VERSION."
    else
        echo "  Run scripts/snapshot-release-docs.sh $NEW_VERSION before tagging,"
        echo "  or CI (check-version-consistency) will fail."
    fi
elif [ "$IS_PRERELEASE" -eq 1 ]; then
    echo -e "${YELLOW}Skipping docs snapshot for prerelease $NEW_VERSION.${NC}"
    echo "  Stable docs stay on the latest stable release until $BASE_VERSION ships."
else
    echo "Snapshotting release docs..."
    ./scripts/snapshot-release-docs.sh "$NEW_VERSION"
fi

echo ""
echo "Changed files:"
git diff --stat 2>/dev/null || true
echo ""
echo "Next steps:"
echo "  • Update CHANGELOG.md with release notes"
echo "  • Update cmd/bd/info.go versionChanges"
echo "  • Or use: bd mol wisp beads-release --var version=$NEW_VERSION"
