#!/bin/bash
# shellcheck source-path=SCRIPTDIR
# Required PR formatting and Go lint contract.

set -euo pipefail

fail() {
    printf 'pr-lint contract: %s\n' "$1" >&2
    exit 1
}

script_source="${BASH_SOURCE[0]}"
case "$script_source" in
    */*) script_parent="${script_source%/*}" ;;
    *) script_parent="." ;;
esac
SCRIPT_DIR="$(cd -P -- "$script_parent" && pwd)"
REPO_ROOT="$(cd -P -- "$SCRIPT_DIR/../.." && pwd)"

[[ "${BEADS_CI_TOOLCHAIN_BOUND:-}" == "1" ]] ||
    fail "the host toolchain was not bound"

require_absolute_executable() {
    local label="$1"
    local path="$2"
    [[ ("$path" == /* || "$path" =~ ^[A-Za-z]:/) &&
       -f "$path" && -x "$path" ]] ||
        fail "$label is not an absolute executable: $path"
}

for variable in \
    BEADS_CI_BASH \
    BEADS_CI_GIT \
    BEADS_CI_GO \
    BEADS_CI_GOFMT \
    BEADS_CI_GOLANGCI_LINT \
    BEADS_CI_MAKE \
    BEADS_CI_SED \
    BEADS_CI_UNAME; do
    value="${!variable:-}"
    require_absolute_executable "$variable" "$value"
done
[[ ("${BEADS_CI_GOLANGCI_CONFIG:-}" == /* ||
    "${BEADS_CI_GOLANGCI_CONFIG:-}" =~ ^[A-Za-z]:/) &&
   -f "$BEADS_CI_GOLANGCI_CONFIG" &&
   ! -L "$BEADS_CI_GOLANGCI_CONFIG" ]] ||
    fail "BEADS_CI_GOLANGCI_CONFIG is not an absolute regular file"
[[ "$BEADS_CI_GOLANGCI_CONFIG" -ef "$REPO_ROOT/.golangci.yml" ]] ||
    fail "the bound golangci-lint configuration is not the repository authority"
[[ "${BASH:-}" == "$BEADS_CI_BASH" ]] ||
    fail "wrapper Bash identity changed from $BEADS_CI_BASH to ${BASH:-<unset>}"
[[ "${GO_VERSION:-}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "GO_VERSION is not one exact protected version"
actual_uname="$("$BEADS_CI_UNAME" -s)"
case "${BEADS_CI_EXPECTED_OUTER_OS:-}" in
    linux) [[ "$actual_uname" == "Linux" ]] || fail "outer OS changed to $actual_uname" ;;
    macos) [[ "$actual_uname" == "Darwin" ]] || fail "outer OS changed to $actual_uname" ;;
    windows)
        [[ "$actual_uname" == MINGW*_NT-* ||
           "$actual_uname" == MSYS*_NT-* ||
           "$actual_uname" == CYGWIN*_NT-* ]] ||
            fail "outer OS changed to $actual_uname"
        ;;
    *) fail "BEADS_CI_EXPECTED_OUTER_OS is missing or unsupported" ;;
esac
if [[ "$BEADS_CI_EXPECTED_OUTER_OS" == "windows" ]]; then
    [[ "${BEADS_CI_WINDOWS_CMD:-}" =~ ^[A-Za-z]:[\\/] ]] ||
        fail "BEADS_CI_WINDOWS_CMD is not an absolute Windows path"
    [[ "${BEADS_CI_WINDOWS_GIT:-}" =~ ^[A-Za-z]:[\\/] ]] ||
        fail "BEADS_CI_WINDOWS_GIT is not an absolute Windows path"
fi

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"
[[ "${GOENV:-}" == "off" ]] ||
    fail "GOENV must be disabled"
[[ "${GOWORK:-}" == "off" ]] ||
    fail "GOWORK must be disabled"
[[ "${GOFLAGS:-}" == "-mod=readonly -tags=${BEADS_BUILD_TAGS}" ]] ||
    fail "GOFLAGS does not enforce the protected readonly module contract"

cd "$REPO_ROOT"

go_env_output=""
if ! go_env_output="$("$BEADS_CI_GO" env GOOS GOARCH CGO_ENABLED)"; then
    printf 'failed to snapshot GOOS, GOARCH, and CGO_ENABLED with go env\n' >&2
    exit 1
fi

frozen_go_env=()
while IFS= read -r value; do
    frozen_go_env+=("$value")
done <<<"$go_env_output"
if [[ "${#frozen_go_env[@]}" -ne 3 ]]; then
    printf 'go env returned an invalid target snapshot\n' >&2
    exit 1
fi

frozen_goos="${frozen_go_env[0]}"
frozen_goarch="${frozen_go_env[1]}"
frozen_cgo_enabled="${frozen_go_env[2]}"
if [[ ! "$frozen_goos" =~ ^[a-z0-9_]+$ ||
      ! "$frozen_goarch" =~ ^[a-z0-9_]+$ ||
      ! "$frozen_cgo_enabled" =~ ^[01]$ ]]; then
    printf 'go env returned an invalid target tuple\n' >&2
    exit 1
fi
[[ "$frozen_goos" == "${BEADS_CI_TARGET_GOOS:-}" &&
   "$frozen_goarch" == "${BEADS_CI_TARGET_GOARCH:-}" &&
   "$frozen_cgo_enabled" == "${BEADS_CI_TARGET_CGO_ENABLED:-}" ]] ||
    fail "the selected target changed across the public Make boundary"

run_linter() {
    local goos="$1"
    local goarch="$2"
    local cgo_enabled="$3"

    GOOS="$goos" \
    GOARCH="$goarch" \
    CGO_ENABLED="$cgo_enabled" \
        "$BEADS_CI_GOLANGCI_LINT" \
        run \
        --config "$BEADS_CI_GOLANGCI_CONFIG" \
        --modules-download-mode=readonly \
        --timeout=5m \
        --build-tags="$BEADS_BUILD_TAGS" \
        ./...
}

run_format_check() {
    local make_arguments=(
        --no-print-directory
        -f
        Makefile
        "CI_BASH=$BEADS_CI_BASH"
        "CI_GOFMT=$BEADS_CI_GOFMT"
        "CI_SED=$BEADS_CI_SED"
        "GO_VERSION=$GO_VERSION"
    )
    if [[ "$BEADS_CI_EXPECTED_OUTER_OS" == "windows" ]]; then
        make_arguments+=(
            "WINDOWS_CMD_EXE=$BEADS_CI_WINDOWS_CMD"
            "GIT_WINDOWS_EXE=$BEADS_CI_WINDOWS_GIT"
            "CI_GIT=$BEADS_CI_WINDOWS_GIT"
        )
    else
        make_arguments+=(
            "CI_GIT=$BEADS_CI_GIT"
        )
    fi
    make_arguments+=(fmt-check)
    "$BEADS_CI_MAKE" "${make_arguments[@]}"
}

ci_time "gofmt check" -- run_format_check
ci_time "golangci-lint" -- \
    run_linter "$frozen_goos" "$frozen_goarch" "$frozen_cgo_enabled"

# Other target tuples may not load files guarded by //go:build windows && !cgo.
# Cross-lint that non-CGO Windows build as part of the same required wrapper,
# preserving the frozen architecture and avoiding a duplicate only when the
# selected target already matches it exactly.
if [[ "$frozen_goos" != "windows" || "$frozen_cgo_enabled" != "0" ]]; then
    ci_time "golangci-lint (windows)" -- \
        run_linter windows "$frozen_goarch" 0
fi
