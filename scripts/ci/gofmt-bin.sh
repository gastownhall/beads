#!/usr/bin/env bash
# Resolve the gofmt that matches go.mod's pinned Go toolchain, and print its
# absolute path on stdout.
#
# gofmt's output is not stable across Go releases, so whichever binary runs
# decides the verdict. A bare `gofmt` on PATH belongs to whatever Go happens to
# be installed locally, and GOTOOLCHAIN=auto does not correct for that: the
# toolchain switch only ever moves UP to satisfy go.mod's go directive, never
# down. CI has no such freedom -- actions/setup-go installs exactly the go.mod
# version via go-version-file -- so a host running a newer Go formats
# differently from the gate that judges it.
#
# The failure is in the dangerous direction. The gate reds on files no branch
# touched, which reads as "main's lint is broken", and its own advice ("run
# make fmt") rewrites those files into a form CI's pinned gofmt then rejects.
#
# Every gofmt invocation in this repo goes through here: scripts/ci/fmt-check.sh,
# the Makefile fmt target, and .githooks/pre-commit. Add call sites to that list
# rather than reaching for a bare gofmt.
#
# Set GOFMT to override the resolution entirely.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

warn() {
    printf 'gofmt-bin: %s\n' "$*" >&2
}

# Falling back is safer than failing, but it must be LOUD: a silent fallback to
# the PATH binary is indistinguishable from a correct resolution, and it
# reintroduces exactly the skew this script exists to remove.
fallback() {
    local reason="$1" path
    if ! path="$(command -v gofmt 2>/dev/null)"; then
        warn "$reason, and there is no gofmt on PATH either"
        return 1
    fi
    warn "$reason"
    warn "falling back to $path, which may format differently from CI"
    printf '%s\n' "$path"
}

if [[ -n "${GOFMT:-}" ]]; then
    printf '%s\n' "$GOFMT"
    exit 0
fi

pinned="$(awk '$1 == "go" { print $2; exit }' "$REPO_ROOT/go.mod")"
if [[ -z "$pinned" ]]; then
    fallback "no go directive in $REPO_ROOT/go.mod"
    exit
fi

# A "go 1.26" directive is legal; GOTOOLCHAIN names need the patch component.
if [[ "$pinned" =~ ^[0-9]+\.[0-9]+$ ]]; then
    pinned="$pinned.0"
fi

if ! command -v go >/dev/null 2>&1; then
    fallback "go is not on PATH, so go.mod's go$pinned toolchain cannot be resolved"
    exit
fi

goroot=""
if [[ "$(go env GOVERSION 2>/dev/null)" == "go$pinned" ]]; then
    # The CI case: the host go already is the pinned one. Read GOROOT directly
    # rather than naming a GOTOOLCHAIN, so nothing is fetched on a machine that
    # has nothing to fetch.
    goroot="$(go env GOROOT 2>/dev/null || true)"
elif ! goroot="$(GOTOOLCHAIN="go$pinned" go env GOROOT 2>/dev/null)"; then
    goroot=""
fi

if [[ -z "$goroot" || ! -x "$goroot/bin/gofmt" ]]; then
    fallback "could not resolve a gofmt from go.mod's go$pinned toolchain"
    exit
fi

printf '%s\n' "$goroot/bin/gofmt"
