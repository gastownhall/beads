#!/bin/bash
# check-cli-docs-drift.sh — Blame-scoped freshness check for generated CLI docs.
#
# Strict probe first: regenerate the generated doc artifacts and diff against
# the committed copies, exactly like generate-cli-docs.sh --check. When the
# probe fails AND a diff base is known (a PR), the drift is attributed before
# failing anyone:
#
#   * regenerate the same artifacts at HEAD and at the merge-base, each in a
#     scratch worktree with the canonical pinned build
#     (CGO_ENABLED=0 go build -tags gms_pure_go);
#   * if the change neither alters the regenerated CLI surface nor touches the
#     generated files, the drift was inherited from the base branch: warn and
#     pass — contributors only own docs for the code they actually touched;
#   * otherwise fail, and write the exact regeneration diff as a patch
#     (--patch-out / $DOC_DRIFT_PATCH_OUT) so the fix never depends on the
#     contributor's local build environment.
#
# Without a diff base (push to main, releases, plain local runs) the strict
# probe result stands, preserving the historical behavior.
#
# Usage: check-cli-docs-drift.sh [--base <ref>] [--patch-out <file>] [bd-binary]
#   --base       diff base ref (default: $BD_DOCS_DIFF_BASE, then
#                origin/$GITHUB_BASE_REF when set, as in GitHub PR CI)
#   --patch-out  where to write the regeneration patch on attributable drift
#                (default: $DOC_DRIFT_PATCH_OUT when set)
#
# Attribution compares committed states; commit local changes before relying
# on it outside CI.

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

BASE_REF=""
PATCH_OUT="${DOC_DRIFT_PATCH_OUT:-}"
BD_ARG=""

while [ "$#" -gt 0 ]; do
    case "$1" in
        --base)
            [ "$#" -ge 2 ] || { echo "Error: --base needs a ref" >&2; exit 2; }
            BASE_REF="$2"
            shift 2
            ;;
        --patch-out)
            [ "$#" -ge 2 ] || { echo "Error: --patch-out needs a path" >&2; exit 2; }
            PATCH_OUT="$2"
            shift 2
            ;;
        -h|--help)
            sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            BD_ARG="$1"
            shift
            ;;
    esac
done

if [ -z "$BASE_REF" ]; then
    BASE_REF="${BD_DOCS_DIFF_BASE:-}"
fi
if [ -z "$BASE_REF" ] && [ -n "${GITHUB_BASE_REF:-}" ]; then
    BASE_REF="origin/${GITHUB_BASE_REF}"
fi

# The complete set of generated doc artifacts, as git pathspecs.
GEN_PATHSPECS=(
    "docs/CLI_REFERENCE.md"
    "docs/cli-reference"
    "docs/docs.json"
)

print_fix_help() {
    echo ""
    echo "To fix in any environment (build a canonical bd, then regenerate):"
    echo "  CGO_ENABLED=0 go build -tags gms_pure_go -o ./bd-docs ./cmd/bd/"
    echo "  ./scripts/generate-cli-docs.sh ./bd-docs"
    echo "  rm ./bd-docs"
}

# Check out a commit into a scratch worktree and regenerate every generated
# doc artifact there with the canonical pinned build.
regen_worktree() {
    local sha="$1" dir="$2"
    git -C "$PROJECT_ROOT" worktree add --detach --quiet "$dir" "$sha"
    (
        cd "$dir"
        CGO_ENABLED=0 go build -tags gms_pure_go -o "$dir/.docs-bd" ./cmd/bd/
        ./scripts/generate-cli-docs.sh "$dir/.docs-bd" >/dev/null
        rm -f "$dir/.docs-bd"
    )
}

# Stable content hash of the regenerated artifacts in a worktree, so two
# regenerations can be compared without caring about temp paths.
surface_fingerprint() {
    local dir="$1"
    (
        cd "$dir"
        local f d
        for f in docs/CLI_REFERENCE.md docs/docs.json; do
            if [ -f "$f" ]; then
                printf '== %s\n' "$f"
                cat "$f"
            fi
        done
        for d in docs/cli-reference; do
            if [ -d "$d" ]; then
                find "$d" -type f -name '*.md' | sort | while IFS= read -r f; do
                    printf '== %s\n' "$f"
                    cat "$f"
                done
            fi
        done
    ) | sha256sum | awk '{print $1}'
}

# --- Strict probe ------------------------------------------------------------

if "$PROJECT_ROOT/scripts/generate-cli-docs.sh" --check ${BD_ARG:+"$BD_ARG"}; then
    exit 0
fi

echo ""
echo "Strict CLI docs freshness probe failed; attributing the drift..."

strict_stands() {
    echo "$1"
    echo "FAIL: generated CLI docs are out of sync (strict result stands)."
    print_fix_help
    exit 1
}

if [ -z "$BASE_REF" ]; then
    strict_stands "No diff base available (not a PR build)."
fi
if ! command -v go >/dev/null 2>&1; then
    strict_stands "Go toolchain unavailable; cannot regenerate for attribution."
fi
if ! git -C "$PROJECT_ROOT" rev-parse --verify --quiet "$BASE_REF" >/dev/null; then
    strict_stands "Diff base $BASE_REF is not available locally (shallow clone? use fetch-depth: 0)."
fi

HEAD_SHA="$(git -C "$PROJECT_ROOT" rev-parse HEAD)"
if ! MERGE_BASE="$(git -C "$PROJECT_ROOT" merge-base "$HEAD_SHA" "$BASE_REF")"; then
    strict_stands "Cannot compute merge-base with $BASE_REF (shallow clone? use fetch-depth: 0)."
fi

if [ -n "$(git -C "$PROJECT_ROOT" status --porcelain)" ]; then
    echo "Note: working tree is dirty; attribution uses committed state only."
fi

SCRATCH="$(mktemp -d)"
W_HEAD="$SCRATCH/head"
W_BASE="$SCRATCH/base"
cleanup() {
    git -C "$PROJECT_ROOT" worktree remove --force "$W_HEAD" >/dev/null 2>&1 || true
    git -C "$PROJECT_ROOT" worktree remove --force "$W_BASE" >/dev/null 2>&1 || true
    rm -rf "$SCRATCH"
}
trap cleanup EXIT

echo "Regenerating docs at HEAD (${HEAD_SHA:0:12}) with the canonical pinned build..."
if ! regen_worktree "$HEAD_SHA" "$W_HEAD"; then
    strict_stands "Regeneration at HEAD failed."
fi

if git -C "$W_HEAD" diff --quiet; then
    echo "PASS: committed docs are fresh under the canonical pinned build."
    echo "(The strict probe failed for a reason that does not affect committed state:"
    echo " either the supplied bd binary differs from the canonical pinned build"
    echo " [CGO_ENABLED=0 go build -tags gms_pure_go], or uncommitted local edits"
    echo " to generated docs made the working tree diverge. Attribution compares"
    echo " committed states only - commit local changes before relying on it.)"
    exit 0
fi

echo "Regenerating docs at merge-base (${MERGE_BASE:0:12})..."
if ! regen_worktree "$MERGE_BASE" "$W_BASE"; then
    strict_stands "Regeneration at the merge-base failed."
fi

PR_TOUCHED="$(git -C "$PROJECT_ROOT" diff --name-only "$MERGE_BASE" "$HEAD_SHA" -- "${GEN_PATHSPECS[@]}")"
FP_HEAD="$(surface_fingerprint "$W_HEAD")"
FP_BASE="$(surface_fingerprint "$W_BASE")"

if [ -z "$PR_TOUCHED" ] && [ "$FP_HEAD" = "$FP_BASE" ]; then
    echo ""
    echo "WARN: generated docs are stale, but the drift is inherited from the base branch:"
    echo "this change neither alters the regenerated CLI surface nor touches generated files."
    git -C "$W_HEAD" diff --stat | sed 's/^/  /'
    echo "Not failing this PR. The base branch needs a docs regeneration:"
    echo "  ./scripts/generate-cli-docs.sh"
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
        echo "::warning::Generated CLI docs are stale on the base branch (inherited drift, not introduced by this PR)."
    fi
    echo "PASS: no doc drift attributable to this change"
    exit 0
fi

echo ""
echo "FAIL: this change affects the generated CLI docs and the committed copies are stale."
if [ "$FP_HEAD" != "$FP_BASE" ]; then
    echo "  - the regenerated CLI surface differs from the merge-base"
fi
if [ -n "$PR_TOUCHED" ]; then
    echo "  - generated doc files were modified by this change:"
    echo "$PR_TOUCHED" | sed 's/^/      /'
fi
echo ""
git -C "$W_HEAD" diff --stat | sed 's/^/  /'

if [ -n "$PATCH_OUT" ]; then
    git -C "$W_HEAD" diff > "$PATCH_OUT"
    echo ""
    echo "The exact fix (regenerated with CI's canonical build) was written to:"
    echo "  $PATCH_OUT"
    echo "In CI it is uploaded as the 'cli-docs-freshness-patch' artifact;"
    echo "download it and run: git apply cli-docs-freshness.patch"
fi
print_fix_help
exit 1
