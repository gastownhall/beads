#!/usr/bin/env bash
# build-examples.sh — build every Go module under examples/.
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

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT" || exit 1

# Canonical build settings (CGO_ENABLED, -tags=gms_pure_go via GOFLAGS). The
# examples link the same go-mysql-server that pulls in ICU under cgo, so they
# need the project tag exactly as the main build does.
# shellcheck source=/dev/null
source ./.buildflags

mapfile -t modules < <(git ls-files 'examples/*/go.mod' | xargs -r -n1 dirname | sort)

if [[ "${#modules[@]}" -eq 0 ]]; then
    echo "build-examples: no example modules found under examples/" >&2
    exit 0
fi

# `go build ./...` on a main package writes the binary into the working
# directory, which would leave untracked junk in examples/ after every local
# run. Send output to a scratch directory instead.
outdir="$(mktemp -d)"
trap 'rm -rf "$outdir"' EXIT

failed=()
for mod in "${modules[@]}"; do
    echo "==> building $mod"
    if ( cd "$mod" && go build -o "$outdir/" ./... ); then
        echo "    ok"
    else
        echo "    FAILED" >&2
        failed+=("$mod")
    fi
done

if [[ "${#failed[@]}" -eq 0 ]]; then
    echo "build-examples: ${#modules[@]} example module(s) built cleanly"
    exit 0
fi

{
    echo
    echo "build-examples: ${#failed[@]} of ${#modules[@]} example module(s) failed to build:"
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
