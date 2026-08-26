#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# channel-promote.sh — compute and stage a channel build for beads
# =============================================================================
#
# Part of the release-channel proposal for gastownhall/beads (issue #5867).
# Chrome-style channels: canary -> dev -> beta -> stable.
#
# This script does the *version arithmetic and prep* for a channel build. It
# does not tag, does not push, and does not publish. Those are the workflow's
# job, and stable promotion additionally requires a human approval.
#
# It deliberately calls the project's own scripts rather than reimplementing
# them, so a channel build performs byte-for-byte the prep that RELEASING.md
# prescribes:
#
#   scripts/update-versions.sh <version>     all 11 version-bearing files
#   (cd integrations/beads-mcp && uv lock)   MCP lockfile, or Package Gate reds
#   scripts/check-versions.sh                consistency validation
#
# DRY RUN BY DEFAULT. Nothing is written without --apply.
#
# Usage:
#   channel-promote.sh --channel dev  [--bump auto|minor|patch] [--stamp YYYYMMDD] [--apply]
#   channel-promote.sh --channel beta [--bump auto|minor|patch] [--seq N] [--apply]
#
# Canary is deliberately NOT a valid channel here — see "Why canary has no
# version" below.
# =============================================================================

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

CHANNEL=""
BUMP="auto"
STAMP=""
SEQ=""
BASE_TAG_OVERRIDE=""
ALLOW_MULTI_MIGRATION=0
APPLY=0

usage() {
    cat <<'USAGE'
Usage: channel-promote.sh --channel <dev|beta> [options]

Options:
  --channel <dev|beta>   Which channel to stage a build for. Required.
  --bump <auto|minor|patch>
                         How to derive the next base version from the current
                         one. "auto" (default) reads CHANGELOG [Unreleased]:
                         a "### Added" section means minor, otherwise patch.
  --stamp <YYYYMMDD>     Dev builds only. Defaults to today (UTC).
  --seq <N>              Beta builds only. Defaults to 1, or one past the
                         highest existing beta tag for the same base version.
  --allow-multi-migration
                         Beta only. Cut across more than one schema step. The
                         default refusal exists because twelve forward-only
                         steps in one release is what broke v1.2.1; override
                         deliberately or not at all.
  --base-tag <tag>       Compare the schema delta against this tag instead of
                         the nearest ancestor tag. Use when promoting a beta
                         from a prior beta rather than from a stable line.
                         Must be an ancestor of HEAD.
  --apply                Actually write. Without this, nothing is modified.
  -h, --help             This text.

Exit codes:
  0  staged (or dry-run computed) successfully
  1  usage or precondition error
  2  the computed version is not representable on a required channel
  3  a called project script failed
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --channel) CHANNEL="${2:-}"; shift 2 ;;
        --bump)    BUMP="${2:-}"; shift 2 ;;
        --stamp)   STAMP="${2:-}"; shift 2 ;;
        --seq)     SEQ="${2:-}"; shift 2 ;;
        --base-tag) BASE_TAG_OVERRIDE="${2:-}"; shift 2 ;;
        --allow-multi-migration) ALLOW_MULTI_MIGRATION=1; shift ;;
        --apply)   APPLY=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo -e "${RED}Unknown argument: $1${NC}" >&2; usage >&2; exit 1 ;;
    esac
done

# -----------------------------------------------------------------------------
# Preconditions
# -----------------------------------------------------------------------------

case "$CHANNEL" in
    dev|beta) ;;
    canary)
        cat >&2 <<'CANARY'
ERROR: canary takes no version bump.

A canary build is "main, built" — it is not a candidate for anything, so it
carries no version of its own and mutates no version-bearing file. The channel
workflow fast-forwards refs/heads/channel/canary and refreshes a single rolling
prerelease whose assets are replaced in place.

This is not only a simplification. A canary identifier is NOT REPRESENTABLE in
PEP 440, so it cannot be written into integrations/beads-mcp at all:

    1.3.0-dev.20260825    -> valid, normalises to 1.3.0.dev20260825
    1.3.0-beta.1          -> valid, normalises to 1.3.0b1
    1.3.0-canary.abc1234  -> INVALID
    1.3.0-canary.20260825 -> INVALID  (numeric suffix does not help; PEP 440
                                       admits only a/b/rc/post/dev labels)

Verified with python packaging.version on 2026-08-25.
CANARY
        exit 2 ;;
    "") echo -e "${RED}ERROR: --channel is required${NC}" >&2; usage >&2; exit 1 ;;
    *)  echo -e "${RED}ERROR: unknown channel '$CHANNEL'${NC}" >&2; exit 1 ;;
esac

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "$REPO_ROOT" ]; then
    echo -e "${RED}ERROR: not inside a git repository${NC}" >&2; exit 1
fi
cd "$REPO_ROOT"

for f in cmd/bd/version.go scripts/update-versions.sh scripts/check-versions.sh CHANGELOG.md; do
    if [ ! -e "$f" ]; then
        echo -e "${RED}ERROR: $f not found — is this the beads repo?${NC}" >&2; exit 1
    fi
done

# -----------------------------------------------------------------------------
# Current version, next base version
# -----------------------------------------------------------------------------

CURRENT_VERSION="$(grep 'Version = ' cmd/bd/version.go | head -1 | sed 's/.*"\(.*\)".*/\1/')"
if [ -z "$CURRENT_VERSION" ]; then
    echo -e "${RED}ERROR: could not read Version from cmd/bd/version.go${NC}" >&2; exit 1
fi
CURRENT_BASE="${CURRENT_VERSION%%-*}"

IFS='.' read -r MAJ MIN PAT <<< "$CURRENT_BASE"

# "auto": a new feature in [Unreleased] means a minor bump; otherwise patch.
# Read only the [Unreleased] block, stopping at the next version heading.
if [ "$BUMP" = "auto" ]; then
    UNRELEASED="$(awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{f=0} f' CHANGELOG.md || true)"
    if printf '%s' "$UNRELEASED" | grep -qE '^### Added'; then
        BUMP="minor"
    else
        BUMP="patch"
    fi
    AUTO_NOTE=" (auto)"
else
    AUTO_NOTE=""
fi

case "$BUMP" in
    minor) NEXT_BASE="${MAJ}.$((MIN + 1)).0" ;;
    patch) NEXT_BASE="${MAJ}.${MIN}.$((PAT + 1))" ;;
    *) echo -e "${RED}ERROR: --bump must be auto, minor or patch${NC}" >&2; exit 1 ;;
esac

# -----------------------------------------------------------------------------
# Channel version string
# -----------------------------------------------------------------------------

case "$CHANNEL" in
    dev)
        [ -n "$STAMP" ] || STAMP="$(date -u +%Y%m%d)"
        if ! printf '%s' "$STAMP" | grep -qE '^[0-9]{8}$'; then
            echo -e "${RED}ERROR: --stamp must be YYYYMMDD${NC}" >&2; exit 1
        fi
        NEW_VERSION="${NEXT_BASE}-dev.${STAMP}"
        ;;
    beta)
        if [ -z "$SEQ" ]; then
            HIGHEST="$(git tag --list "v${NEXT_BASE}-beta.*" \
                        | sed "s/^v${NEXT_BASE}-beta\.//" \
                        | grep -E '^[0-9]+$' | sort -n | tail -1 || true)"
            SEQ=$(( ${HIGHEST:-0} + 1 ))
        fi
        if ! printf '%s' "$SEQ" | grep -qE '^[0-9]+$'; then
            echo -e "${RED}ERROR: --seq must be an integer${NC}" >&2; exit 1
        fi
        NEW_VERSION="${NEXT_BASE}-beta.${SEQ}"
        ;;
esac

# update-versions.sh's own accepted shape. Fail here rather than halfway
# through an eleven-file rewrite.
if ! [[ $NEW_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    echo -e "${RED}ERROR: '$NEW_VERSION' is not a shape update-versions.sh accepts${NC}" >&2
    exit 2
fi

# The MCP package is Python: the identifier must also survive PEP 440, or the
# Package Gate (MCP) goes red at publish time rather than here.
if command -v python3 >/dev/null 2>&1; then
    if ! python3 - "$NEW_VERSION" <<'PY' 2>/dev/null
import sys
try:
    from packaging.version import Version
except ImportError:
    sys.exit(0)          # cannot check; do not block on a missing dev dep
try:
    Version(sys.argv[1])
except Exception:
    sys.exit(1)
PY
    then
        echo -e "${RED}ERROR: '$NEW_VERSION' is not representable in PEP 440${NC}" >&2
        echo "       integrations/beads-mcp cannot carry this identifier." >&2
        exit 2
    fi
fi

# -----------------------------------------------------------------------------
# Migration-boundary signal
# -----------------------------------------------------------------------------
#
# The base is the nearest ANCESTOR tag, never "the latest release". beads has
# tags that are not on this line at all — v1.2.2 is a hotfix re-release off the
# v1.1 line and is not an ancestor of main — so tag-by-date or tag-by-name
# would silently compare against a tree this branch never contained.

MIGRATION_DIR="internal/storage/schema/migrations"

if [ -n "$BASE_TAG_OVERRIDE" ]; then
    if ! git rev-parse --verify --quiet "${BASE_TAG_OVERRIDE}^{commit}" >/dev/null; then
        echo -e "${RED}ERROR: --base-tag '$BASE_TAG_OVERRIDE' is not a known ref${NC}" >&2
        exit 1
    fi
    # A base that is not an ancestor compares against a tree this branch never
    # contained, which is how v1.2.2 would silently become the baseline.
    if ! git merge-base --is-ancestor "$BASE_TAG_OVERRIDE" HEAD; then
        echo -e "${RED}ERROR: --base-tag '$BASE_TAG_OVERRIDE' is not an ancestor of HEAD${NC}" >&2
        echo "       Comparing against an off-line tag reports a fictional delta." >&2
        exit 1
    fi
    BASE_TAG="$BASE_TAG_OVERRIDE"
else
    BASE_TAG="$(git describe --tags --abbrev=0 --match 'v*' HEAD 2>/dev/null || true)"
fi

if [ -n "$BASE_TAG" ]; then
    # Top-level migrations only: the ignored/ subdirectory is not applied.
    NEW_MIGRATIONS="$(git diff --name-only --diff-filter=A "${BASE_TAG}..HEAD" -- "$MIGRATION_DIR" 2>/dev/null \
                       | grep -E "^${MIGRATION_DIR}/[0-9]{4}_.*\.up\.sql$" || true)"
    MIGRATION_COUNT="$(printf '%s' "$NEW_MIGRATIONS" | grep -c . || true)"
else
    NEW_MIGRATIONS=""
    MIGRATION_COUNT="unknown"
fi

# -----------------------------------------------------------------------------
# Report
# -----------------------------------------------------------------------------

echo
echo -e "${BLUE}=== beads channel promotion — ${CHANNEL} ===${NC}"
echo
printf '  %-22s %s\n' "current version"   "$CURRENT_VERSION"
printf '  %-22s %s\n' "base bump"         "${BUMP}${AUTO_NOTE}"
printf '  %-22s %s\n' "next base"         "$NEXT_BASE"
printf '  %-22s %s\n' "channel version"   "$NEW_VERSION"
printf '  %-22s %s\n' "ancestor tag"      "${BASE_TAG:-<none found>}"
printf '  %-22s %s\n' "commit"            "$(git rev-parse --short HEAD)"
echo

if [ "$MIGRATION_COUNT" = "unknown" ]; then
    echo -e "  ${YELLOW}schema delta: UNKNOWN — no ancestor tag found${NC}"
elif [ "$MIGRATION_COUNT" -eq 0 ]; then
    echo -e "  ${GREEN}schema delta: none — this span is schema-compatible${NC}"
else
    echo -e "  ${YELLOW}schema delta: ${MIGRATION_COUNT} migration(s) since ${BASE_TAG}${NC}"
    printf '%s\n' "$NEW_MIGRATIONS" | sed 's|^|    |'
    if [ "$CHANNEL" = "beta" ] && [ "$MIGRATION_COUNT" -gt 1 ]; then
        if [ "$ALLOW_MULTI_MIGRATION" -eq 1 ]; then
            echo
            echo -e "  ${YELLOW}OVERRIDDEN: cutting a beta across ${MIGRATION_COUNT} schema steps${NC}"
            echo "  because --allow-multi-migration was passed. Each step is applied"
            echo "  forward-only on first access, so a user who hits a problem is"
            echo "  ${MIGRATION_COUNT} steps from the last good schema, not one."
        else
            echo
            echo -e "  ${RED}BLOCKED: a beta may carry at most one schema step.${NC}"
            echo "  Twelve forward-only steps in one release is the shape that broke"
            echo "  v1.2.1. Cut a beta at the first migration boundary instead, or"
            echo "  pass --allow-multi-migration deliberately."
            exit 1
        fi
    fi
fi
echo

# Machine-readable for the calling workflow.
if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
        echo "version=$NEW_VERSION"
        echo "base_version=$NEXT_BASE"
        echo "tag=v$NEW_VERSION"
        echo "channel=$CHANNEL"
        echo "base_tag=${BASE_TAG:-}"
        echo "migration_count=$MIGRATION_COUNT"
    } >> "$GITHUB_OUTPUT"
fi

if [ "$APPLY" -ne 1 ]; then
    echo -e "${YELLOW}DRY RUN — nothing written. Re-run with --apply to stage.${NC}"
    exit 0
fi

# -----------------------------------------------------------------------------
# Apply: the project's own prep, in the project's own order
# -----------------------------------------------------------------------------

echo -e "${BLUE}--> scripts/update-versions.sh $NEW_VERSION${NC}"
./scripts/update-versions.sh "$NEW_VERSION" || { echo -e "${RED}update-versions.sh failed${NC}" >&2; exit 3; }

if command -v uv >/dev/null 2>&1; then
    echo -e "${BLUE}--> uv lock (integrations/beads-mcp)${NC}"
    ( cd integrations/beads-mcp && uv lock ) || { echo -e "${RED}uv lock failed${NC}" >&2; exit 3; }
else
    echo -e "${RED}ERROR: uv not found.${NC}" >&2
    echo "The MCP lockfile must be regenerated in the same commit as the version" >&2
    echo "bump or the Package Gate (MCP) goes red. Refusing to stage a build that" >&2
    echo "is known-red. Install uv or run this where uv is available." >&2
    exit 3
fi

echo -e "${BLUE}--> scripts/check-versions.sh${NC}"
./scripts/check-versions.sh || { echo -e "${RED}check-versions.sh failed${NC}" >&2; exit 3; }

echo
echo -e "${GREEN}Staged $NEW_VERSION.${NC} Working tree now carries the version bump."
echo "The workflow commits, fast-forwards refs/heads/channel/${CHANNEL}, and tags."
echo "This script does not tag, push, or publish."
