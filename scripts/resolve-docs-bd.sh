#!/bin/bash
# resolve-docs-bd.sh — Resolve the bd binary the docs pipeline must use.
#
# Reads the release pin from docs/cli-docs.pin. When the pin names a tag,
# builds (and caches) a canonical pure-Go bd from that tag's source and
# prints the binary's absolute path on stdout. When the pin is HEAD or the
# pin file is absent, prints nothing and exits 0: callers fall back to their
# current-checkout behavior.
#
# The build matches CI's canonical docs build (CGO_ENABLED=0, -tags
# gms_pure_go), so regenerated docs are reproducible in any environment.
# The binary is cached at build/docs-bd/<pin>/bd (gitignored via /build/).
#
# Usage: scripts/resolve-docs-bd.sh
#   stdout: absolute path to the pinned bd binary, or empty when unpinned
#   diagnostics go to stderr

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
PIN_FILE="$PROJECT_ROOT/docs/cli-docs.pin"

if [ ! -f "$PIN_FILE" ]; then
    exit 0
fi

PIN="$(grep -v '^[[:space:]]*#' "$PIN_FILE" | grep -m1 '[^[:space:]]' | tr -d '[:space:]' || true)"
if [ -z "$PIN" ] || [ "$PIN" = "HEAD" ]; then
    exit 0
fi

CACHE_DIR="$PROJECT_ROOT/build/docs-bd/$PIN"
CACHED_BD="$CACHE_DIR/bd"
if [ -x "$CACHED_BD" ]; then
    echo "$CACHED_BD"
    exit 0
fi

# Make sure the pinned tag exists locally; shallow CI checkouts may not have
# fetched tags.
if ! git -C "$PROJECT_ROOT" rev-parse --verify --quiet "refs/tags/$PIN^{commit}" >/dev/null; then
    echo "docs pin: fetching tag $PIN from origin..." >&2
    git -C "$PROJECT_ROOT" fetch --depth=1 origin "refs/tags/$PIN:refs/tags/$PIN" >&2
fi

echo "docs pin: building bd from tag $PIN (CGO_ENABLED=0 -tags gms_pure_go)..." >&2
WT="$(mktemp -d)"
cleanup() {
    git -C "$PROJECT_ROOT" worktree remove --force "$WT" >/dev/null 2>&1 || true
    rm -rf "$WT"
}
trap cleanup EXIT

git -C "$PROJECT_ROOT" worktree add --detach --quiet "$WT" "refs/tags/$PIN"
mkdir -p "$CACHE_DIR"
(cd "$WT" && CGO_ENABLED=0 go build -tags gms_pure_go -o "$CACHED_BD" ./cmd/bd/) >&2

echo "$CACHED_BD"
