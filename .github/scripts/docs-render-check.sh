#!/usr/bin/env bash
#
# docs-render-check.sh — baseline-aware Mintlify broken-link check for CI.
#
# Runs `mint broken-links` on the HEAD docs tree. When the HEAD has broken
# page links, materializes the BASE docs tree (via git archive) and reports
# only NET-NEW breakage — links that the PR introduced, not pre-existing ones.
# Static-asset refs (.png/.svg/.jpg/.gif) are excluded: mint over-reports
# in-tree images the published build serves fine.
#
# Usage:
#   docs-render-check.sh [<base-ref>]
#
#   <base-ref>  The base branch/sha to compare against (default: origin/main).
#               Enables baseline-aware net-new detection.
#
# Exit codes:
#   0 — no net-new page-link regressions
#   1 — net-new page-link regressions found (details on stdout), or the
#       mint tool itself failed to produce a broken-links report
#
# Requires: git, npx (Node.js)
#
set -euo pipefail

DOCS_DIR="docs"
DOCS_CONFIG="$DOCS_DIR/docs.json"
BASE_REF="${1:-origin/main}"
# Pinned so a floating release can't silently change the output format this
# script parses (keep in sync with mint.sh).
MINT_VERSION="${MINT_VERSION:-4.2.687}"
MINT_CMD="${MINT_CMD:-npx --yes mint@$MINT_VERSION}"

WORK_TMP="$(mktemp -d)"
trap 'rm -rf "$WORK_TMP"' EXIT

# Verify we have a Mintlify docs tree.
if [[ ! -f "$DOCS_CONFIG" ]]; then
    echo "docs-render-check: no docs/docs.json found — skipping" >&2
    exit 0
fi

# Static-asset refs are excluded: mint over-reports in-tree images the
# published build serves fine.
ASSET_EXTS='\.png$|\.svg$|\.jpg$|\.jpeg$|\.gif$|\.ico$|\.webp$|\.woff2?$|\.ttf$|\.eot$'

# has_report <mint-output-file> — did mint produce a broken-links report at
# all (as opposed to dying on a registry outage / crash)?
has_report() {
    grep -qE '✗|broken' "$1" 2>/dev/null
}

# Parse `mint broken-links` report lines. Each broken link looks like:
#   [broken-links]  tutorials/01-beads.md  ->  /tutorials/01-beads.md
# or
#   ✗  /tutorials/01-beads.md
# Capture path-shaped tokens on report lines only — root-relative (/foo) or
# relative with a slash — excluding URLs and static assets. The same parser
# runs on both HEAD and BASE output, so residual noise cancels out in the
# net-new comparison.
parse_mint_broken_links() {
    local file="$1"
    grep -E '✗|broken' "$file" 2>/dev/null \
        | grep -oE '[/][^ )]+|[A-Za-z0-9_.-]+/[^ )]+' \
        | grep -vE '^https?:|^[A-Za-z0-9_.-]+\.[A-Za-z]{2,}/' \
        | grep -vE "$ASSET_EXTS" \
        | sort -u || true
}

# --- run_mint <docs-root> <output-file> → exit code -------------------------
run_mint() {
    local root="$1"
    local out="$2"
    cd "$root"
    if $MINT_CMD broken-links >"$out" 2>&1; then
        cd - >/dev/null
        return 0
    fi
    cd - >/dev/null
    return 1
}

HEAD_OUT="$WORK_TMP/head-mint.txt"
BASE_OUT="$WORK_TMP/base-mint.txt"
BASE_TREE="$WORK_TMP/base-docs"

# --- HEAD check --------------------------------------------------------------
HEAD_EXIT=0
run_mint "." "$HEAD_OUT" || HEAD_EXIT=$?

if [[ $HEAD_EXIT -eq 0 ]]; then
    echo "docs-render-check: no broken links in HEAD — PASS" >&2
    exit 0
fi

HEAD_LINKS="$WORK_TMP/head-links.txt"
parse_mint_broken_links "$HEAD_OUT" | sort -u >"$HEAD_LINKS"

if [[ ! -s "$HEAD_LINKS" ]]; then
    if has_report "$HEAD_OUT"; then
        # mint produced a report, but every flagged link is a static asset
        # we deliberately ignore. Pass.
        echo "docs-render-check: mint flagged only static-asset links — PASS" >&2
        exit 0
    fi
    # mint exited non-zero without producing a broken-links report at all
    # (registry outage, crash, output-format change). Fail closed rather
    # than let the gate silently stop enforcing.
    echo "::error::docs-render-check: mint failed without producing a broken-links report — tool failure, not a clean run"
    echo "mint output follows:" >&2
    cat "$HEAD_OUT" >&2
    exit 1
fi

# --- BASE check (baseline-aware) --------------------------------------------
mkdir -p "$BASE_TREE"
if git archive "$BASE_REF" -- "$DOCS_DIR" 2>/dev/null | tar -x -C "$BASE_TREE"; then
    BASE_EXIT=0
    run_mint "$BASE_TREE" "$BASE_OUT" || BASE_EXIT=$?
    BASE_LINKS="$WORK_TMP/base-links.txt"
    parse_mint_broken_links "$BASE_OUT" | sort -u >"$BASE_LINKS"
    # Net-new = in HEAD but NOT in BASE.
    NEW_LINKS="$WORK_TMP/new-links.txt"
    comm -23 "$HEAD_LINKS" "$BASE_LINKS" >"$NEW_LINKS"
else
    echo "docs-render-check: could not materialize base tree from $BASE_REF — checking HEAD only" >&2
    cp "$HEAD_LINKS" "$WORK_TMP/new-links.txt"
    NEW_LINKS="$WORK_TMP/new-links.txt"
fi

if [[ ! -s "$NEW_LINKS" ]]; then
    echo "docs-render-check: broken links exist but none are net-new — PASS (pre-existing baseline)" >&2
    exit 0
fi

# --- NET-NEW regressions found — fail with explanation --------------
echo ""
echo "::error::docs-render-check: net-new broken Mintlify page links detected"
echo ""
echo "The following page links are newly broken by this PR:"
while IFS= read -r link; do
    echo "  $link"
done <"$NEW_LINKS"
echo ""
echo "──────────────────────────────────────────────────────────────────────────"
echo "docs/ is authored for the Mintlify site (the beads Mintlify site),"
echo "not for direct GitHub viewing. These paths/links are intentional —"
echo "please don't reformat them for GitHub. If something is genuinely broken"
echo "on the live site, note it in the PR and we'll fix it Mintlify-side."
echo "──────────────────────────────────────────────────────────────────────────"
exit 1
