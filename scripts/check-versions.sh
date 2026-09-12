#!/bin/bash
# Compatibility entrypoint for the authoritative Go release-version checker.
# Run this before committing version bumps.

set -euo pipefail

if ! command -v go >/dev/null 2>&1; then
    echo "Release version checker unavailable: install Go and add it to PATH." >&2
    exit 127
fi

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

cd "$repo_root"
if ! checker_dir="$(mktemp -d "${TMPDIR:-/tmp}/beads-check-versions.XXXXXX")"; then
    echo "Release version checker unavailable: cannot create a temporary directory." >&2
    exit 127
fi
trap 'rm -rf -- "$checker_dir"' EXIT
# An explicit .exe name works on Unix and native Windows alike.
checker="$checker_dir/check-versions.exe"
if ! go build -tags=gms_pure_go -o "$checker" ./scripts/check-versions; then
    echo "Release version checker unavailable: Go could not build it." >&2
    exit 127
fi
# Running the binary preserves usage status 2 and validation status 1; go run
# collapses both to 1 and adds its own diagnostic.
"$checker" "$@"
