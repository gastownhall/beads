#!/usr/bin/env bash
# build-examples.sh — type-check every Go module under examples/.
#
# The example modules are separate Go modules that reach the parent through a
# `replace github.com/steveyegge/beads => ../..` directive, so their go.mod and
# go.sum record the parent's full dependency graph. Nothing in CI compiled them,
# which meant they went stale silently: on 2026-08-01 both
# examples/bd-example-extension-go and examples/library-usage failed a plain
# `go build` on main with "updates to go.mod needed; to update it: go mod tidy".
# Examples are the first code a new user copies, so a broken one is a bad first
# five minutes.
#
# Usage: scripts/build-examples.sh
#
# Exits non-zero listing every module that failed, so one broken example does
# not hide another.
#
# Why `go vet` and not `go build`: with `-o <dir>/`, `go build ./...` compiles
# only the MAIN packages and silently skips every library package — a module
# with a broken internal package would pass. Without `-o` it litters the tree
# with binaries. `go vet ./...` type-checks every package INCLUDING test files
# (examples/library-usage/main_test.go exercises a lot of live API, and
# `go mod tidy` counts its imports, so a build that never compiles it leaves
# part of the recorded dependency graph unverified), writes no artifacts, and
# still fails on the stale-go.mod condition this script exists for. It also
# avoids `go build`'s "no main packages to build" false failure on a
# library-only example module.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)" || exit 1
cd "$REPO_ROOT" || exit 1

# Canonical build settings (CGO_ENABLED, -tags=gms_pure_go via GOFLAGS). The
# examples link the same go-mysql-server that pulls in ICU under cgo, so they
# need the project tag exactly as the main build does. Guarded: with no errexit,
# a failed source would otherwise continue with GOFLAGS unset and silently
# type-check the ICU path instead, while check-build-tags.sh still passes (it
# only greps for the literal string `.buildflags`).
# shellcheck source=/dev/null
source ./.buildflags || { echo "build-examples: cannot source .buildflags" >&2; exit 1; }

# Bash 3.2 compatible (stock macOS ships 3.2, which has no `mapfile`), and no
# GNU-only `xargs -r` or `sort -z` (BSD sort has no -z; a sort stage would have
# emptied the pipeline on macOS — and `git ls-files` output is already sorted).
# NUL-delimited so a path containing whitespace cannot be split —
# `git ls-files | xargs -n1 dirname` turned "examples/has space/go.mod" into
# two bogus entries.
modules=()
while IFS= read -r -d '' f; do
    modules+=("$(dirname "$f")")
done < <(git ls-files -z 'examples/*/go.mod')

if [[ "${#modules[@]}" -eq 0 ]]; then
    # Not a pass. Either examples/ was restructured, or this is running outside
    # a git checkout (a release tarball), and a job that compiles nothing must
    # not report success.
    echo "build-examples: found no example modules (git ls-files 'examples/*/go.mod' was empty)" >&2
    echo "build-examples: refusing to report success having checked nothing" >&2
    exit 1
fi

failed=()
for mod in "${modules[@]}"; do
    echo "==> checking $mod"
    if ( cd "$mod" && go vet ./... ); then
        echo "    ok"
    else
        echo "    FAILED" >&2
        failed+=("$mod")
    fi
done

if [[ "${#failed[@]}" -eq 0 ]]; then
    echo "build-examples: ${#modules[@]} example module(s) type-check cleanly"
    exit 0
fi

{
    echo
    echo "build-examples: ${#failed[@]} of ${#modules[@]} example module(s) failed:"
    printf '  %s\n' "${failed[@]}"
    echo
    echo "If the error is \"updates to go.mod needed\", the example's recorded"
    echo "dependency graph has drifted from the parent module — usually because a"
    echo "change to the root go.mod was not mirrored into the examples. Fix with:"
    echo
    for mod in "${failed[@]}"; do
        echo "  (cd $mod && go mod tidy)"
    done
    echo
    echo "then commit the resulting go.mod/go.sum changes."
} >&2
exit 1
