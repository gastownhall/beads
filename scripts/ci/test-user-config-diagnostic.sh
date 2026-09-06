#!/usr/bin/env bash
# Exercise the native config source label, including both XDG states.
# Compatible with macOS Bash 3.2; run with linux, darwin, or windows.
set -euo pipefail

[[ $# -eq 1 ]] || { echo 'Expected one native Go OS' >&2; exit 1; }
expected_os="$1"
case "$(uname -s)" in
    Linux) actual_os=linux ;;
    Darwin) actual_os=darwin ;;
    MINGW*|MSYS*) actual_os=windows ;;
    *) echo 'Unsupported native Bash host' >&2; exit 1 ;;
esac
[[ "$expected_os" = "$actual_os" ]] || {
    echo "Expected $expected_os, running on $actual_os" >&2; exit 1;
}
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"
# shellcheck source=../../.buildflags
source "$repo_root/.buildflags"
export CGO_ENABLED=0
go_executable="$(type -P go)"
[[ "$go_executable" = /* && -x "$go_executable" ]] || {
    echo 'Go must resolve to an absolute application' >&2; exit 1;
}
host_info="$("$go_executable" env GOHOSTOS GOOS)"
[[ "$host_info" = "$expected_os"$'\n'"$expected_os" ]] || {
    echo "Expected native $expected_os Go host and target" >&2; exit 1;
}
parent=TestUserConfigYamlPathNamesNativeEnvironmentSource
expected=("$parent" "$parent/XDG_configured" "$parent/XDG_absent")
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
status=0
"$go_executable" test -tags "$BEADS_BUILD_TAGS" -v -count=1 -timeout 3m \
    -run "^$parent$" ./internal/config >"$log" 2>&1 || status=$?
cat "$log"
[[ $status -eq 0 ]] || exit "$status"
if grep -Eq -- '^[[:space:]]*--- (FAIL|SKIP):' "$log"; then
    echo 'Native config diagnostic tests must pass without skips' >&2
    exit 1
fi
passes="$(grep -Ec -- '^[[:space:]]*--- PASS:' "$log" || true)"
[[ "$passes" = "${#expected[@]}" ]] || {
    echo 'Expected exactly one parent and two child passes' >&2; exit 1;
}
for name in "${expected[@]}"; do
    passes="$(grep -Ec -- "^[[:space:]]*--- PASS: $name( |$)" "$log" || true)"
    [[ "$passes" = 1 ]] || {
        echo "Expected exactly one PASS for $name, got $passes" >&2; exit 1;
    }
done
