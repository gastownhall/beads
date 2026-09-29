#!/bin/bash
# Check that all version files are in sync
# Run this before committing version bumps

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

# Get the canonical version from version.go
CANONICAL=$(grep 'Version = ' cmd/bd/version.go | sed 's/.*"\(.*\)".*/\1/')

if [ -z "$CANONICAL" ]; then
    echo -e "${RED}❌ Could not read version from cmd/bd/version.go${NC}"
    exit 1
fi

echo "Canonical version (from version.go): $CANONICAL"
echo ""

MISMATCH=0

# Base (prerelease-stripped) form. The Windows PE numeric fields must be purely
# numeric, so they carry this rather than $CANONICAL.
BASE_VERSION="${CANONICAL%%-*}"

check_expected() {
    local _file=$1
    local version=$2
    local expected=$3
    local description=$4

    if [ "$version" != "$expected" ]; then
        echo -e "${RED}❌ $description: ${version:-missing} (expected $expected)${NC}"
        MISMATCH=1
    else
        echo -e "${GREEN}✓ $description: $version${NC}"
    fi
}

check_version() {
    check_expected "$1" "$2" "$CANONICAL" "$3"
}

# Check all version files
check_version "integrations/beads-mcp/pyproject.toml" \
    "$(grep '^version = ' integrations/beads-mcp/pyproject.toml 2>/dev/null | sed 's/.*"\(.*\)".*/\1/')" \
    "MCP pyproject.toml"

check_version "integrations/beads-mcp/src/beads_mcp/__init__.py" \
    "$(grep '__version__ = ' integrations/beads-mcp/src/beads_mcp/__init__.py 2>/dev/null | sed 's/.*"\(.*\)".*/\1/')" \
    "MCP __init__.py"

check_version "plugins/beads/.claude-plugin/plugin.json" \
    "$(jq -r '.version' plugins/beads/.claude-plugin/plugin.json 2>/dev/null)" \
    "Claude plugin.json"

check_version "plugins/beads/.codex-plugin/plugin.json" \
    "$(jq -r '.version' plugins/beads/.codex-plugin/plugin.json 2>/dev/null)" \
    "Codex plugin.json"

# update-versions.sh bumps this one too, but it was never gated here — so a
# drifted copilot manifest (the same drift class that carried stale .githooks
# markers through v1.2.0) would silently no-op the bump's old->new sed, pass
# this script, pass the pre-push hook and CI, and ship advertising the previous
# version.
check_version "plugins/beads/.copilot-plugin/plugin.json" \
    "$(jq -r '.version' plugins/beads/.copilot-plugin/plugin.json 2>/dev/null)" \
    "Copilot plugin.json"

check_version ".claude-plugin/marketplace.json" \
    "$(jq -r '.plugins[0].version' .claude-plugin/marketplace.json 2>/dev/null)" \
    "Claude marketplace.json"

check_version "npm-package/package.json" \
    "$(jq -r '.version' npm-package/package.json 2>/dev/null)" \
    "npm package.json"

# The release workflow's MCP package gate runs `uv sync --locked`, so a stale
# lockfile fails the release only in the tag-triggered run — after the tag
# exists and can no longer be rewritten (this burned the v1.1.1 tag; v1.1.0
# lost its first run the same way). Gate it here so the release-tag pre-push
# hook and CI refuse the stale lock before a tag exists. The pinned-version
# check is dependency-free; the fuller `uv lock --check` runs when uv is
# available and also catches dependency edits made without a relock.
#
# uv.lock records the PEP 440-normalized version (1.1.0-rc.1 → 1.1.0rc1), so
# normalize the canonical form before comparing.
LOCK_EXPECTED=$(printf '%s' "$CANONICAL" | sed -E 's/-rc\.?/rc/')
LOCK_VERSION=$(awk -F '"' '/^name = "beads-mcp"$/ { found=1; next } found && /^version = / { print $2; exit }' integrations/beads-mcp/uv.lock 2>/dev/null)
if [ "$LOCK_VERSION" != "$LOCK_EXPECTED" ]; then
    echo -e "${RED}❌ MCP uv.lock (beads-mcp pin): ${LOCK_VERSION:-missing} (expected $LOCK_EXPECTED) — run: uv lock --directory integrations/beads-mcp${NC}"
    MISMATCH=1
else
    echo -e "${GREEN}✓ MCP uv.lock (beads-mcp pin): $LOCK_VERSION${NC}"
fi

if command -v uv >/dev/null 2>&1; then
    if uv lock --check --directory integrations/beads-mcp >/dev/null 2>&1; then
        echo -e "${GREEN}✓ MCP uv.lock: fresh (uv lock --check)${NC}"
    else
        echo -e "${RED}❌ MCP uv.lock: stale — run: uv lock --directory integrations/beads-mcp${NC}"
        MISMATCH=1
    fi
fi

# Tracked managed git-hook sections (.githooks/*): the BEGIN/END markers embed
# the binary Version, and TestTrackedManagedHookSectionsMatchGenerator holds
# them equal to the cmd/bd/hooks.go generator output. A version bump that skips
# them reddens main only after the push (that was the v1.2.0 bump), so gate the
# markers here. Marker version only — full body equality stays the test's job.
for hook in .githooks/*; do
    [ -f "$hook" ] || continue
    for prefix in "BEGIN" "END"; do
        marker=$(grep -oE -- "--- $prefix BEADS INTEGRATION v[^ ]+ ---" "$hook" | head -1)
        if [ -z "$marker" ]; then
            echo -e "${RED}❌ $hook: no '$prefix BEADS INTEGRATION' marker found${NC}"
            MISMATCH=1
            continue
        fi
        check_version "$hook" \
            "$(printf '%s' "$marker" | sed -E 's/.* v([^ ]+) ---/\1/')" \
            "$hook $prefix marker"
    done
done

# Windows PE resource metadata. gen-winres.sh re-derives winres.json's numeric
# fields from version.go at build time, so those self-heal — but
# manifest.xml's <assemblyIdentity> version is embedded verbatim with no
# backstop, and update-versions.sh rewrites both with a sed anchored on the OLD
# value, so a drifted file is a silent no-op. Gate them.
#
# The numeric fields (file_version/product_version, and the manifest's
# four-part version) must be purely numeric, so they carry the base version:
# for 1.1.0-rc.1 they read 1.1.0 while FileVersion/ProductVersion read the full
# prerelease string.
WINRES_JSON="cmd/bd/winres/winres.json"
for field in file_version product_version; do
    check_expected "$WINRES_JSON" \
        "$(jq -r ".RT_VERSION.\"#1\".\"0000\".fixed.$field" "$WINRES_JSON" 2>/dev/null)" \
        "$BASE_VERSION" "winres.json $field"
done
for field in FileVersion ProductVersion; do
    check_expected "$WINRES_JSON" \
        "$(jq -r ".RT_VERSION.\"#1\".\"0000\".info.\"0409\".$field" "$WINRES_JSON" 2>/dev/null)" \
        "$CANONICAL" "winres.json $field"
done

# Anchor on line start: the XML declaration on line 1 also carries a
# version="1.0" attribute.
check_expected "cmd/bd/winres/manifest.xml" \
    "$(grep -oE '^[[:space:]]*version="[0-9][^"]*"' cmd/bd/winres/manifest.xml 2>/dev/null | head -1 | sed -E 's/.*"(.*)"/\1/')" \
    "$BASE_VERSION.0" "manifest.xml assemblyIdentity version"

echo ""

if [ $MISMATCH -eq 1 ]; then
    echo -e "${RED}❌ Version mismatch detected!${NC}"
    echo ""
    echo "Note: re-running 'scripts/update-versions.sh $CANONICAL' will NOT fix"
    echo "most of these. It derives the OLD version from cmd/bd/version.go, which"
    echo "already reads $CANONICAL, so its old->new substitutions rewrite"
    echo "$CANONICAL -> $CANONICAL and no-op on a file that drifted. Only the"
    echo ".githooks markers (rewritten wholesale) and uv.lock (regenerated) heal"
    echo "on a re-run."
    echo ""
    echo "Fix whichever applies:"
    echo "  • cmd/bd/version.go itself is wrong (you meant another version):"
    echo "      scripts/update-versions.sh <intended-version>"
    echo "  • one gated file drifted: edit that file to $CANONICAL and re-run"
    echo "    this script."
    echo "  • several drifted: set cmd/bd/version.go back to the previous"
    echo "    version, then scripts/update-versions.sh $CANONICAL to replay the"
    echo "    whole bump."
    exit 1
else
    echo -e "${GREEN}✓ Version files in sync at: $CANONICAL${NC}"
    echo "  (Version files only — this script does not check docs. The docs"
    echo "   release line and docs/cli-docs.pin are manual release steps; see"
    echo "   RELEASING.md.)"
fi
