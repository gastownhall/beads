#!/usr/bin/env bash
# Run the actual helper packages on Windows, including their native-only tests.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
# These environment helpers are pure Go; no C compiler is needed for this lane.
export CGO_ENABLED=0
# shellcheck source=../../.buildflags
source "$root/.buildflags"

go_bin=$(command -v go) || { echo 'Go is required for Windows environment tests' >&2; exit 1; }
host=$("$go_bin" env GOHOSTOS GOOS) || exit $?
if [[ "$host" != $'windows\nwindows' ]]; then
    echo 'Environment helper tests require native Windows Go host and target' >&2
    exit 1
fi

log=$(mktemp)
trap 'rm -f "$log"' EXIT
if "$go_bin" test -count=1 -v ./internal/execenv ./internal/githooksenv ./internal/gittraceenv >"$log" 2>&1; then
    cat "$log"
else
    status=$?
    cat "$log"
    exit "$status"
fi

# Additional passing cases are welcome; these Windows boundaries must execute.
required=(
    TestEnvironmentOperationsForBothHosts/Windows
    TestKeyEqualUsesHostSemantics
    TestDisabledEnvUsesHostKeySemantics
    TestScrubEnvWindowsCaseInsensitiveNames
    TestScrubEnvDoesNotEqualFoldUnicodeKeys
    TestStderrDirectedWindowsLeadingSlashIsFileTarget
)
for name in "${required[@]}"; do
    count=$(grep -Ec "^[[:space:]]*--- PASS: $name \\(" "$log" || true)
    if [[ "$count" != 1 ]]; then
        echo "Required Windows test did not pass exactly once: $name" >&2
        exit 1
    fi
done
