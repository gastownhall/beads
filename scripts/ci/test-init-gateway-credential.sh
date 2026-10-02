#!/usr/bin/env bash
# Run the init credential shell boundary on one declared native host.
# Direct invocation: bash scripts/ci/test-init-gateway-credential.sh Linux|macOS|Windows
# Keep this compatible with macOS Bash 3.2.
set -euo pipefail

case "${1:-}" in
    Linux) expected_os=linux ;;
    macOS) expected_os=darwin ;;
    Windows) expected_os=windows ;;
    *) echo 'Expected one native host: Linux, macOS, or Windows' >&2; exit 1 ;;
esac
[[ $# -eq 1 ]] || { echo 'Expected exactly one native host' >&2; exit 1; }

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"
# shellcheck source=../../.buildflags
source "$repo_root/.buildflags"
# These fixtures exercise the production shell without opening a database, so
# no database build configuration is needed either way — and the build config is
# deliberately left at whatever .buildflags defaults to rather than forced.
#
# Forcing CGO_ENABLED=0 here used to compile a package graph disjoint from the
# ./cmd/bd build the same job performs a couple of steps earlier under the
# default: 742 packages versus 1505 for `go list -deps -tags=gms_pure_go
# ./cmd/bd`, with `net` and `os/user` among the cgo-using differences, whose
# build IDs propagate upward. Nothing was reused, so this blocking lane paid a
# second cold compile on macOS and Windows. Inheriting the default makes this
# step incremental, and it also means the natively executed fixtures exercise
# the same build of cmd/bd that actually ships on those platforms.
go_executable="$(command -v go)"
[[ "$go_executable" = /* && -x "$go_executable" ]] || {
    echo 'Go must resolve to an absolute executable' >&2; exit 1;
}
host_info="$("$go_executable" env GOHOSTOS GOOS)"
[[ "$host_info" = "$expected_os"$'\n'"$expected_os" ]] || {
    echo "Expected native $expected_os Go host and target" >&2; exit 1;
}

# This single list owns both test selection and required execution evidence.
expected=(
    TestApplyInitGatewayCredentialAdoptsToken
    TestApplyInitGatewayCredentialSkipsEmbeddedMode
    TestApplyInitGatewayCredentialNoopWithoutCommand
    TestApplyInitGatewayCredentialFailsClosed
    TestApplyInitGatewayCredentialPresetWins
)
# ...which makes the array the only wiring between a fixture and native
# execution, with nothing tying it back to the source file. A new
# TestApplyInitGatewayCredential* fixture added later would fall out of the
# selector, lose macOS/Windows execution entirely, and still leave this driver
# exiting 0 with the required gate green — the exact "green because nothing ran"
# mode the per-name PASS counting below exists to prevent. Compare the list
# against the source of truth by name and by count, before running anything.
declared_fixtures="$(grep -Eo '^func TestApplyInitGatewayCredential[A-Za-z0-9_]*' cmd/bd/init_gateway_test.go | sed 's/^func //' | sort)"
wired_fixtures="$(printf '%s\n' "${expected[@]}" | sort)"
if [[ "$declared_fixtures" != "$wired_fixtures" ]]; then
    echo 'Init credential fixture list is out of sync with cmd/bd/init_gateway_test.go.' >&2
    echo 'Declared in the test file:' >&2
    printf '%s\n' "$declared_fixtures" | sed 's/^/  /' >&2
    echo 'Wired into this native lane:' >&2
    printf '%s\n' "$wired_fixtures" | sed 's/^/  /' >&2
    exit 1
fi

selector="$(IFS='|'; echo "${expected[*]}")"
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
status=0
"$go_executable" test -v -count=1 -timeout 5m -run "^($selector)$" ./cmd/bd >"$log" 2>&1 || status=$?
cat "$log"
[[ $status -eq 0 ]] || exit "$status"
if grep -Eq -- '^[[:space:]]*--- (FAIL|SKIP):' "$log"; then
    echo 'Init credential fixtures must pass without skips' >&2
    exit 1
fi
for test_name in "${expected[@]}"; do
    passes="$(grep -Ec -- "^--- PASS: $test_name( |$)" "$log" || true)"
    [[ "$passes" = 1 ]] || {
        echo "Expected exactly one PASS for $test_name, got $passes" >&2
        exit 1
    }
done
