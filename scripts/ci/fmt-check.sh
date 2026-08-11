#!/usr/bin/env bash
# Shared Go formatting check for Make and PR lint wrappers.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$REPO_ROOT"

printf 'Checking Go formatting...\n'
if UNFORMATTED="$(gofmt -l .)"; then
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
