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
    local bin="$1" version=""
    if command -v go >/dev/null 2>&1; then
        version="$(go version "$bin" 2>/dev/null | awk '{ print $NF }')"
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
