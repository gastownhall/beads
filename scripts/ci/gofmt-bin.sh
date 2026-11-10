#!/usr/bin/env bash
# Resolve the gofmt that matches go.mod's pinned Go toolchain, and print its
# absolute path on stdout.
#
# gofmt's output is not stable across Go releases, so whichever binary runs
# decides the verdict. A bare `gofmt` on PATH belongs to whatever Go happens to
# be installed locally, and GOTOOLCHAIN=auto does not correct for that: the
# toolchain switch only ever moves UP to satisfy go.mod's requirements, never
# down. CI has no such freedom -- actions/setup-go installs exactly the go.mod
# version via go-version-file -- so a host running a newer Go formats
# differently from the gate that judges it.
#
# The failure is in the dangerous direction. The gate reds on files no branch
# touched, which reads as "main's lint is broken", and its own advice ("run
# make fmt") rewrites those files into a form CI's pinned gofmt then rejects.
#
# The repo's shell-side gofmt call sites resolve through here:
# scripts/ci/fmt-check.sh, the Makefile fmt target, and .githooks/pre-commit.
# Add new shell call sites to that list rather than reaching for a bare gofmt.
#
# One front door is deliberately not on that list, because it does not run from
# this repo's checkout: cmd/bd/preflight.go's "Formatting" doctor check execs a
# bare PATH `gofmt -l .` (and prescribes `gofmt -w .`) against whatever project
# the user points bd at, so it has no scripts/ci/ to call and is reporting on
# the developer's own environment rather than reproducing CI's verdict. Routing
# it through a resolver is a separate change.
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
    # An override is honoured as given -- refusing it would defeat the escape
    # hatch -- but say so here, or a typo surfaces as a "gofmt: not found" from
    # whichever of the three call sites happened to run it.
    if [[ ! -x "$GOFMT" ]]; then
        warn "GOFMT is set to $GOFMT, which is not executable; using it anyway"
    fi
    printf '%s\n' "$GOFMT"
    exit 0
fi

# The `toolchain` directive wins over the `go` directive when present: `go` is
# the floor promised to importers, `toolchain` is what this repo is actually
# built and formatted with. That precedence is not a choice made here -- it is
# the repo's, implemented at Makefile:85-90 (which exports GOTOOLCHAIN from the
# same pair for every make target) and in Go at scripts/bazel_policy_test.go's
# goModToolchainVersion. Reading `go` here would resolve a second, older
# toolchain than the one every other route formats with, and would miss the
# GOVERSION fast path below on every host that honours the directive.
#
# Match on the value, not just the field name: `toolchain default` is a legal
# directive meaning "no toolchain upgrade", and accepting it verbatim would set
# pinned=default, which is non-empty, so the `go` fallback below never fires and
# the resolution degrades to the bare PATH gofmt this script exists to replace.
# Requiring the literal `go` prefix is what the repo's other two readers of this
# pair do (Makefile:85's `s/^toolchain go//p`, scripts/bazel_policy_test.go's
# `^toolchain\s+go(\S+)\s*$`), so `default` falls through to the `go` directive.
pinned="$(awk '$1 == "toolchain" && $2 ~ /^go/ { sub(/^go/, "", $2); print $2; exit }' "$REPO_ROOT/go.mod")"
if [[ -z "$pinned" ]]; then
    pinned="$(awk '$1 == "go" { print $2; exit }' "$REPO_ROOT/go.mod")"
fi
if [[ -z "$pinned" ]]; then
    fallback "no toolchain or go directive in $REPO_ROOT/go.mod"
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
