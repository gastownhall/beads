#!/bin/bash
# check-website-docs-drift.sh — Freshness check for committed website doc mirrors.
#
# scripts/sync-website-docs.sh regenerates the Docusaurus mirrors of selected
# root docs (website/docs/..., and, when a versioned snapshot exists,
# website/versioned_docs/version-<latest>/... plus
# website/versioned_sidebars/version-<latest>-sidebars.json) from their
# source files in docs/. Unlike the CLI reference docs, this mirroring is a
# pure text transform of files already committed in this repo — no bd binary
# build and no blame-scoped attribution across a merge-base is needed. The
# check simply runs the sync script and fails if it produces any diff
# (tracked or untracked), then restores the working tree so the check itself
# is side-effect-free.
#
# Usage: check-website-docs-drift.sh

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_ROOT"

# All paths sync-website-docs.sh can write to. Checked, backed up, diffed,
# and restored as a set.
CHECK_PATHS=(website/docs website/versioned_docs website/versioned_sidebars)

if [ -n "$(git status --porcelain -- "${CHECK_PATHS[@]}" 2>/dev/null)" ]; then
    echo "WARN: working tree has uncommitted changes under ${CHECK_PATHS[*]};"
    echo "      this check compares against the working tree as-is and will"
    echo "      restore it to its pre-check state on exit."
fi

TMP_BACKUP="$(mktemp -d)"
mkdir -p "$TMP_BACKUP/website"
cp -Rf website/docs "$TMP_BACKUP/website/docs"
if [ -d website/versioned_docs ]; then
    cp -Rf website/versioned_docs "$TMP_BACKUP/website/versioned_docs"
fi
if [ -d website/versioned_sidebars ]; then
    cp -Rf website/versioned_sidebars "$TMP_BACKUP/website/versioned_sidebars"
fi

restore_tree() {
    rm -rf website/docs website/versioned_docs website/versioned_sidebars
    cp -Rf "$TMP_BACKUP/website/docs" website/docs
    if [ -d "$TMP_BACKUP/website/versioned_docs" ]; then
        cp -Rf "$TMP_BACKUP/website/versioned_docs" website/versioned_docs
    fi
    if [ -d "$TMP_BACKUP/website/versioned_sidebars" ]; then
        cp -Rf "$TMP_BACKUP/website/versioned_sidebars" website/versioned_sidebars
    fi
    rm -rf "$TMP_BACKUP"
}
trap restore_tree EXIT

"$SCRIPT_DIR/sync-website-docs.sh"

# git diff alone misses drift where sync-website-docs.sh recreates a mirror
# file that isn't currently tracked (e.g. a committed mirror was deleted, or
# a new sync target was never committed): the recreated file is untracked
# and invisible to `git diff`. Treat any new untracked file under the
# checked paths as drift too.
UNTRACKED="$(git ls-files --others --exclude-standard -- "${CHECK_PATHS[@]}")"

if git diff --quiet -- "${CHECK_PATHS[@]}" && [ -z "$UNTRACKED" ]; then
    echo "PASS: committed website doc mirrors are fresh (sync-website-docs.sh produces no changes)."
    exit 0
fi

echo "FAIL: website doc mirrors are out of sync with their source docs."
echo "Run: ./scripts/sync-website-docs.sh"
echo ""
if ! git diff --quiet -- "${CHECK_PATHS[@]}"; then
    git diff --stat -- "${CHECK_PATHS[@]}" | sed 's/^/  /'
    echo ""
    git diff -- "${CHECK_PATHS[@]}" | sed -n '1,200p'
fi
if [ -n "$UNTRACKED" ]; then
    echo ""
    echo "New (untracked) mirror files sync-website-docs.sh produced:"
    echo "$UNTRACKED" | sed 's/^/  /'
fi
exit 1
