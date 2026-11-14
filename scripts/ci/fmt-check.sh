#!/usr/bin/env bash
# Shared Go formatting check for Make and PR lint wrappers.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$REPO_ROOT"

# Which gofmt runs decides the verdict, so resolve it from go.mod instead of
# PATH and report it. Naming the binary here is what makes a toolchain skew
# legible as a skew, rather than as unformatted files in code no branch
# touched. See scripts/ci/gofmt-bin.sh.
GOFMT_BIN="$("$SCRIPT_DIR/gofmt-bin.sh")"

describe_gofmt() {
    local bin="$1" version="" go_bin=""
    if [[ -n "${TEST_SRCDIR:-}" ]]; then
        # Under `bazel test` a bare `go` on PATH can be the farm's exit-127
        # mask for the host toolchain, so only call a go binary the test
        # explicitly declared as data: BEADS_TEST_GO, set the same way
        # scripts/prlintmake:prlintmake_test wires in the registered SDK's go
        # ($(rlocationpath @go_sdk//:bin/go)). A caller that did not wire it
        # up (e.g. scripts/repochecks:fmt_test) just gets the version-less
        # fallback below, never a PATH `go`.
        if [[ -n "${BEADS_TEST_GO:-}" ]]; then
            local candidate="$BEADS_TEST_GO"
            if [[ "$candidate" != /* ]]; then
                candidate="$TEST_SRCDIR/$candidate"
            fi
            if [[ -x "$candidate" ]]; then
                go_bin="$candidate"
            fi
        fi
    elif command -v go >/dev/null 2>&1; then
        go_bin="go"
    fi
    if [[ -n "$go_bin" ]]; then
        version="$("$go_bin" version "$bin" 2>/dev/null | awk '{ print $NF }')"
    fi
    if [[ -n "$version" ]]; then
        printf '%s (%s)' "$version" "$bin"
    else
        printf '%s' "$bin"
    fi
}

printf 'Checking Go formatting...\n'
printf 'Using gofmt %s\n' "$(describe_gofmt "$GOFMT_BIN")"
if UNFORMATTED="$("$GOFMT_BIN" -l .)"; then
    :
else
    status=$?
    printf 'gofmt failed while checking formatting\n' >&2
    exit "$status"
fi

if [[ -n "$UNFORMATTED" ]]; then
    printf 'The following files are not properly formatted:\n'
    printf '%s\n' "$UNFORMATTED"
    printf '\n'
    printf "Run 'make fmt' to fix formatting\n"
    exit 1
fi

printf 'All Go files are properly formatted\n'
