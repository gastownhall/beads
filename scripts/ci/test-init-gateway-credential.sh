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
# These fixtures exercise the production shell without opening a database.
export CGO_ENABLED=0
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
    TestApplyInitGatewayCredentialHelperProtocol
    TestApplyInitGatewayCredentialAdoptsToken
    TestApplyInitGatewayCredentialSkipsEmbeddedMode
    TestApplyInitGatewayCredentialNoopWithoutCommand
    TestApplyInitGatewayCredentialFailsClosed
    TestApplyInitGatewayCredentialPresetWins
)
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
