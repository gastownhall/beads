#!/bin/bash
# Black-box routing tests for the required PR lint wrapper.

set -euo pipefail

fail() {
    printf 'PR lint routing contract: %s\n' "$1" >&2
    exit 1
}

script_source="${BASH_SOURCE[0]}"
case "$script_source" in
    */*) script_parent="${script_source%/*}" ;;
    *) script_parent="." ;;
esac
SCRIPT_DIR="$(cd -P -- "$script_parent" && pwd)"
REPO_ROOT="$(cd -P -- "$SCRIPT_DIR/../.." && pwd)"
PR_LINT="$SCRIPT_DIR/pr-lint.sh"

[[ "${BEADS_CI_TOOLCHAIN_BOUND:-}" == "1" ]] ||
    fail "the host toolchain was not bound"
[[ "${BASH:-}" == "${BEADS_CI_BASH:-}" ]] ||
    fail "routing test did not reuse the bound Bash"
[[ "$BEADS_CI_BASH" == /* ||
   "$BEADS_CI_BASH" =~ ^[A-Za-z]:/ ]] ||
    fail "the bound Bash is not absolute: $BEADS_CI_BASH"
stub_bash="$BEADS_CI_BASH"
if [[ "$stub_bash" == *[[:space:]]* ]]; then
    [[ "$BEADS_CI_EXPECTED_OUTER_OS" == "windows" ]] ||
        fail "the bound Bash contains whitespace outside Windows"
    stub_bash="/usr/bin/bash"
    [[ -f "$stub_bash" && -x "$stub_bash" &&
       "$stub_bash" -ef "$BEADS_CI_BASH" ]] ||
        fail "the shebang-safe Bash alias is not the bound Windows Bash"
fi

require_absolute_executable() {
    local label="$1"
    local path="$2"
    [[ ("$path" == /* || "$path" =~ ^[A-Za-z]:/) &&
       -f "$path" && -x "$path" ]] ||
        fail "$label is not an absolute executable: $path"
}

canonicalize_path() {
    local path="$1"
    local directory=""
    local leaf=""
    local target=""
    local link_count=0

    [[ "$path" == /* ]] || return 1
    while :; do
        directory="${path%/*}"
        leaf="${path##*/}"
        [[ -n "$directory" ]] || directory="/"
        directory="$(cd -P -- "$directory" 2>/dev/null && pwd)" || return 1
        if [[ "$directory" == "/" ]]; then
            path="/$leaf"
        else
            path="$directory/$leaf"
        fi
        if [[ ! -L "$path" ]]; then
            printf '%s' "$path"
            return 0
        fi

        link_count=$((link_count + 1))
        ((link_count <= 40)) || return 1
        target="$("$BEADS_CI_READLINK" "$path")" || return 1
        if [[ "$target" == /* ]]; then
            path="$target"
        else
            path="$directory/$target"
        fi
    done
}

resolve_executable() {
    local name="$1"
    local path=""
    path="$(type -P -- "$name" 2>/dev/null)" ||
        fail "required routing-test executable is unavailable: $name"
    path="$(canonicalize_path "$path")" ||
        fail "could not canonicalize routing-test executable: $name"
    require_absolute_executable "$name" "$path"
    printf '%s' "$path"
}

for variable in \
    BEADS_CI_CAT \
    BEADS_CI_CHMOD \
    BEADS_CI_CP \
    BEADS_CI_DIFF \
    BEADS_CI_ENV \
    BEADS_CI_MKDIR \
    BEADS_CI_MKTEMP \
    BEADS_CI_READLINK \
    BEADS_CI_RM \
    BEADS_CI_UNAME; do
    require_absolute_executable "$variable" "${!variable:-}"
done
[[ "${GO_VERSION:-}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "GO_VERSION is not one exact protected version"

cat_path="$BEADS_CI_CAT"
chmod_path="$BEADS_CI_CHMOD"
cp_path="$BEADS_CI_CP"
diff_path="$BEADS_CI_DIFF"

fixture_parent="${BEADS_CI_FIXTURE_TEMP_PARENT:-}"
if [[ "$BEADS_CI_EXPECTED_OUTER_OS" == "windows" &&
      "$fixture_parent" =~ ^[A-Za-z]:[\\/] ]]; then
    require_absolute_executable "BEADS_CI_CYGPATH" "${BEADS_CI_CYGPATH:-}"
    fixture_parent="$("$BEADS_CI_CYGPATH" -u "$fixture_parent")" ||
        fail "could not translate the routing-test fixture parent"
fi
[[ "$fixture_parent" == /* && -d "$fixture_parent" && ! -L "$fixture_parent" ]] ||
    fail "the routing-test fixture parent is not one absolute regular directory: ${fixture_parent:-<unset>}"
fixture_parent="$(cd -P -- "$fixture_parent" && pwd)" ||
    fail "could not canonicalize the routing-test fixture parent"
tmpdir="$(
    "$BEADS_CI_MKTEMP" -d "$fixture_parent/beads-pr-lint-routing.XXXXXXXX"
)" || fail "mktemp could not create the routing-test directory"
tmp_basename="${tmpdir##*/}"
tmp_parent="${tmpdir%/*}"
[[ "$tmp_basename" =~ ^beads-pr-lint-routing\.[A-Za-z0-9]{8}$ &&
   "$tmp_parent" == "$fixture_parent" &&
   -d "$tmpdir" &&
   ! -L "$tmpdir" ]] ||
    fail "mktemp returned an unauthorized routing-test directory"
tmpdir="$(cd -P -- "$tmpdir" && pwd)" ||
    fail "could not canonicalize the routing-test directory"
[[ "$tmpdir" == "$fixture_parent/$tmp_basename" ]] ||
    fail "mktemp returned a nested or redirected routing-test directory"
cleanup() {
    local original_status=$?
    local cleanup_failed=0
    local observed_parent=""

    observed_parent="$(cd -P -- "$fixture_parent" && pwd)" ||
        cleanup_failed=1
    if [[ "$cleanup_failed" -eq 0 &&
          "$observed_parent" == "$fixture_parent" &&
          "${tmpdir%/*}" == "$fixture_parent" &&
          "${tmpdir##*/}" =~ ^beads-pr-lint-routing\.[A-Za-z0-9]{8}$ &&
          -d "$tmpdir" &&
          ! -L "$tmpdir" ]]; then
        # This recursively removes only the fully validated private test
        # fixture. Production private-tool cleanup is deliberately leaf-only.
        "$BEADS_CI_RM" -rf -- "$tmpdir" || cleanup_failed=1
        [[ ! -e "$tmpdir" && ! -L "$tmpdir" ]] || cleanup_failed=1
    else
        cleanup_failed=1
    fi
    if [[ "$cleanup_failed" -ne 0 ]]; then
        printf 'PR lint routing contract: fixture cleanup failed: %s\n' \
            "$tmpdir" >&2
        original_status=1
    fi
    trap - EXIT
    exit "$original_status"
}
trap cleanup EXIT

stub_dir="$tmpdir/bin"
event_log="$tmpdir/ordered-events"
"$BEADS_CI_MKDIR" -p "$stub_dir"

printf '%s\n' same >"$tmpdir/diff-same-a"
printf '%s\n' same >"$tmpdir/diff-same-b"
printf '%s\n' different >"$tmpdir/diff-different"
"$diff_path" -u "$tmpdir/diff-same-a" "$tmpdir/diff-same-b" >/dev/null ||
    fail "the bound diff rejected known-identical files"
if "$diff_path" -u "$tmpdir/diff-same-a" "$tmpdir/diff-different" >/dev/null; then
    fail "the bound diff accepted known-different files"
fi

{
    printf '#!%s\n' "$stub_bash"
    "$cat_path" <<'EOF'
set -euo pipefail

expected=(
    --no-print-directory
    -f
    Makefile
    "CI_BASH=$BEADS_CI_BASH"
    "CI_GOFMT=$BEADS_CI_GOFMT"
    "CI_SED=$BEADS_CI_SED"
    "GO_VERSION=$GO_VERSION"
)
if [[ "$BEADS_CI_EXPECTED_OUTER_OS" == "windows" ]]; then
    expected+=(
        "WINDOWS_CMD_EXE=$BEADS_CI_WINDOWS_CMD"
        "GIT_WINDOWS_EXE=$BEADS_CI_WINDOWS_GIT"
        "CI_GIT=$BEADS_CI_WINDOWS_GIT"
    )
else
    expected+=(
        "CI_GIT=$BEADS_CI_GIT"
    )
fi
expected+=(fmt-check)
if [[ "$#" -ne "${#expected[@]}" ]]; then
    printf 'unexpected make argument count: %s\n' "$#" >&2
    exit 64
fi
for ((index = 0; index < ${#expected[@]}; index++)); do
    position=$((index + 1))
    actual="${!position}"
    if [[ "$actual" != "${expected[$index]}" ]]; then
        printf 'unexpected make argument %s: %q\n' \
            "$position" "$actual" >&2
        exit 64
    fi
done

printf 'format\tstate-change\n' >>"$PR_LINT_EVENT_LOG"
if [[ "${PR_LINT_MAKE_FAIL:-0}" == "1" ]]; then
    exit 69
fi
EOF
} >"$stub_dir/make"

{
    printf '#!%s\n' "$stub_bash"
    "$cat_path" <<'EOF'
set -euo pipefail

generation=1
while IFS= read -r event; do
    if [[ "$event" == format$'\t'* || "$event" == lint$'\t'* ]]; then
        generation=2
    fi
done <"$PR_LINT_EVENT_LOG"
{
    printf 'go-env\t%s\t%s' "$generation" "${1:-}"
    for argument in "${@:2}"; do
        printf '\t%s' "$argument"
    done
    printf '\n'
} >>"$PR_LINT_EVENT_LOG"

if [[ "${PR_LINT_GO_ENV_FAIL:-0}" == "1" ]]; then
    exit 68
fi

if ((generation == 1)); then
    goos="$PR_LINT_SNAPSHOT_GOOS"
    goarch="$PR_LINT_SNAPSHOT_GOARCH"
    cgo_enabled="$PR_LINT_SNAPSHOT_CGO_ENABLED"
else
    goos="$PR_LINT_NEXT_GOOS"
    goarch="$PR_LINT_NEXT_GOARCH"
    cgo_enabled="$PR_LINT_NEXT_CGO_ENABLED"
fi

if [[ "$#" -eq 4 &&
      "$1" == "env" &&
      "$2" == "GOOS" &&
      "$3" == "GOARCH" &&
      "$4" == "CGO_ENABLED" ]]; then
    if [[ "${PR_LINT_GO_ENV_MALFORMED:-0}" == "1" ]]; then
        printf '%s\n%s\n' "$goos" "$goarch"
    else
        printf '%s\n%s\n%s\n' "$goos" "$goarch" "$cgo_enabled"
    fi
    exit 0
fi

if [[ "$#" -eq 2 && "$1" == "env" ]]; then
    case "$2" in
        GOOS) printf '%s\n' "$goos" ;;
        GOARCH) printf '%s\n' "$goarch" ;;
        CGO_ENABLED) printf '%s\n' "$cgo_enabled" ;;
        *)
            printf 'unexpected go env key: %s\n' "$2" >&2
            exit 66
            ;;
    esac
    exit 0
fi

if [[ "$#" -eq 0 ]]; then
    printf 'unexpected empty go invocation\n' >&2
else
    printf 'unexpected go invocation:' >&2
    printf ' %q' "$@" >&2
    printf '\n' >&2
fi
exit 65
EOF
} >"$stub_dir/go"

{
    printf '#!%s\n' "$stub_bash"
    "$cat_path" <<'EOF'
set -euo pipefail

if [[ "$#" -ne 7 ||
      "$1" != "run" ||
      "$2" != "--config" ||
      "$3" != "$PR_LINT_EXPECTED_CONFIG" ||
      "$4" != "--modules-download-mode=readonly" ||
      "$5" != "--timeout=5m" ||
      "$6" != "--build-tags=gms_pure_go" ||
      "$7" != "./..." ]]; then
    printf 'unexpected golangci-lint invocation:' >&2
    printf ' %q' "$@" >&2
    printf '\n' >&2
    exit 67
fi
[[ "${GOENV:-}" == "off" ]] || exit 67
[[ "${GOWORK:-}" == "off" ]] || exit 67
[[ "${GOFLAGS:-}" == "-mod=readonly -tags=gms_pure_go" ]] || exit 67

lint_generation=1
while IFS= read -r event; do
    if [[ "$event" == lint$'\t'* ]]; then
        lint_generation=$((lint_generation + 1))
    fi
done <"$PR_LINT_EVENT_LOG"
printf 'lint\t%s\t%s\t%s\t%s\n' \
    "$lint_generation" \
    "${GOOS:-<unset>}" \
    "${GOARCH:-<unset>}" \
    "${CGO_ENABLED:-<unset>}" >>"$PR_LINT_EVENT_LOG"

if [[ "${PR_LINT_FAIL_LINT_GENERATION:-0}" == "$lint_generation" ]]; then
    exit 70
fi
EOF
} >"$stub_dir/golangci-lint"

"$chmod_path" +x "$stub_dir/make" "$stub_dir/go" "$stub_dir/golangci-lint"

assert_event_log() {
    local name="$1"
    shift

    local actual="$tmpdir/$name.events.actual"
    local expected="$tmpdir/$name.events.expected"
    "$cp_path" "$event_log" "$actual"
    printf '%s\n' "$@" >"$expected"
    if ! "$diff_path" -u "$expected" "$actual"; then
        printf 'PR lint routing case %s observed the wrong event sequence\n' "$name" >&2
        return 1
    fi

    local first_event=""
    local go_env_events=0
    local format_events=0
    while IFS= read -r event; do
        if [[ -z "$first_event" ]]; then
            first_event="$event"
        fi
        if [[ "$event" == go-env$'\t'* ]]; then
            go_env_events=$((go_env_events + 1))
        elif [[ "$event" == format$'\t'* ]]; then
            format_events=$((format_events + 1))
        fi
    done <"$event_log"

    [[ "$go_env_events" -eq 1 ]] ||
        fail "case $name did not use exactly one go env event"
    [[ "$format_events" -le 1 ]] ||
        fail "case $name formatted more than once"
    [[ "$first_event" == go-env$'\t'* ]] ||
        fail "case $name performed work before freezing target state"
}

case_count=0

run_case_with_state() {
    local name="$1"
    local snapshot_goos="$2"
    local snapshot_goarch="$3"
    local snapshot_cgo_enabled="$4"
    local next_goos="$5"
    local next_goarch="$6"
    local next_cgo_enabled="$7"
    shift 7
    case_count=$((case_count + 1))

    : >"$event_log"

    if ! "$BEADS_CI_ENV" -i \
        PATH= \
        BASH_ENV= \
        ENV= \
        HOME="${HOME:-}" \
        GOENV=off \
        GOWORK=off \
        GOFLAGS=-mod=readonly \
        GOOS=plan9 \
        GOARCH=386 \
        CGO_ENABLED=0 \
        GO_VERSION="$GO_VERSION" \
        BEADS_CI_TOOLCHAIN_BOUND=1 \
        BEADS_CI_EXPECTED_OUTER_OS="$BEADS_CI_EXPECTED_OUTER_OS" \
        BEADS_CI_BASH="$BEADS_CI_BASH" \
        BEADS_CI_ENV="$BEADS_CI_ENV" \
        BEADS_CI_GIT="$stub_dir/go" \
        BEADS_CI_GO="$stub_dir/go" \
        BEADS_CI_GOFMT="$stub_dir/go" \
        BEADS_CI_GOLANGCI_LINT="$stub_dir/golangci-lint" \
        BEADS_CI_GOLANGCI_CONFIG="$REPO_ROOT/.golangci.yml" \
        BEADS_CI_MAKE="$stub_dir/make" \
        BEADS_CI_SED="$stub_dir/go" \
        BEADS_CI_UNAME="$BEADS_CI_UNAME" \
        BEADS_CI_WINDOWS_CMD='C:\bound\cmd.exe' \
        BEADS_CI_WINDOWS_GIT='C:\bound\git.exe' \
        BEADS_CI_TARGET_GOOS="$snapshot_goos" \
        BEADS_CI_TARGET_GOARCH="$snapshot_goarch" \
        BEADS_CI_TARGET_CGO_ENABLED="$snapshot_cgo_enabled" \
        PR_LINT_EXPECTED_CONFIG="$REPO_ROOT/.golangci.yml" \
        PR_LINT_SNAPSHOT_GOOS="$snapshot_goos" \
        PR_LINT_SNAPSHOT_GOARCH="$snapshot_goarch" \
        PR_LINT_SNAPSHOT_CGO_ENABLED="$snapshot_cgo_enabled" \
        PR_LINT_NEXT_GOOS="$next_goos" \
        PR_LINT_NEXT_GOARCH="$next_goarch" \
        PR_LINT_NEXT_CGO_ENABLED="$next_cgo_enabled" \
        PR_LINT_EVENT_LOG="$event_log" \
        "$BEADS_CI_BASH" --noprofile --norc "$PR_LINT" \
        >"$tmpdir/$name.output" 2>&1; then
        printf 'PR lint routing case %s failed:\n' "$name" >&2
        "$cat_path" "$tmpdir/$name.output" >&2
        return 1
    fi

    assert_event_log "$name" "$@"
}

run_case() {
    local name="$1"
    local goos="$2"
    local goarch="$3"
    local cgo_enabled="$4"
    shift 4
    run_case_with_state \
        "$name" \
        "$goos" "$goarch" "$cgo_enabled" \
        freebsd arm64 0 \
        "$@"
}

run_refusal_case() {
    local name="$1"
    local mode="$2"
    shift 2
    case_count=$((case_count + 1))
    : >"$event_log"

    local go_env_fail=0
    local go_env_malformed=0
    local lint_failure=0
    local make_failure=0
    case "$mode" in
        go-failure) go_env_fail=1 ;;
        go-malformed) go_env_malformed=1 ;;
        lint-failure-1) lint_failure=1 ;;
        lint-failure-2) lint_failure=2 ;;
        make-failure) make_failure=1 ;;
        *) fail "unknown refusal mode: $mode" ;;
    esac

    if "$BEADS_CI_ENV" -i \
        PATH= \
        BASH_ENV= \
        ENV= \
        HOME="${HOME:-}" \
        GOENV=off \
        GOWORK=off \
        GOFLAGS=-mod=readonly \
        GOOS=plan9 \
        GOARCH=386 \
        CGO_ENABLED=0 \
        GO_VERSION="$GO_VERSION" \
        BEADS_CI_TOOLCHAIN_BOUND=1 \
        BEADS_CI_EXPECTED_OUTER_OS="$BEADS_CI_EXPECTED_OUTER_OS" \
        BEADS_CI_BASH="$BEADS_CI_BASH" \
        BEADS_CI_ENV="$BEADS_CI_ENV" \
        BEADS_CI_GIT="$stub_dir/go" \
        BEADS_CI_GO="$stub_dir/go" \
        BEADS_CI_GOFMT="$stub_dir/go" \
        BEADS_CI_GOLANGCI_LINT="$stub_dir/golangci-lint" \
        BEADS_CI_GOLANGCI_CONFIG="$REPO_ROOT/.golangci.yml" \
        BEADS_CI_MAKE="$stub_dir/make" \
        BEADS_CI_SED="$stub_dir/go" \
        BEADS_CI_UNAME="$BEADS_CI_UNAME" \
        BEADS_CI_WINDOWS_CMD='C:\bound\cmd.exe' \
        BEADS_CI_WINDOWS_GIT='C:\bound\git.exe' \
        BEADS_CI_TARGET_GOOS=linux \
        BEADS_CI_TARGET_GOARCH=amd64 \
        BEADS_CI_TARGET_CGO_ENABLED=1 \
        PR_LINT_EXPECTED_CONFIG="$REPO_ROOT/.golangci.yml" \
        PR_LINT_SNAPSHOT_GOOS=linux \
        PR_LINT_SNAPSHOT_GOARCH=amd64 \
        PR_LINT_SNAPSHOT_CGO_ENABLED=1 \
        PR_LINT_NEXT_GOOS=freebsd \
        PR_LINT_NEXT_GOARCH=arm64 \
        PR_LINT_NEXT_CGO_ENABLED=0 \
        PR_LINT_GO_ENV_FAIL="$go_env_fail" \
        PR_LINT_GO_ENV_MALFORMED="$go_env_malformed" \
        PR_LINT_FAIL_LINT_GENERATION="$lint_failure" \
        PR_LINT_MAKE_FAIL="$make_failure" \
        PR_LINT_EVENT_LOG="$event_log" \
        "$BEADS_CI_BASH" --noprofile --norc "$PR_LINT" \
        >"$tmpdir/$name.output" 2>&1; then
        fail "$name unexpectedly succeeded"
    fi
    assert_event_log "$name" "$@"
}

run_case required_linux_cgo linux amd64 1 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\tlinux\tamd64\t1' \
    $'lint\t2\twindows\tamd64\t0'

run_case required_macos_arm64_cgo darwin arm64 1 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\tdarwin\tarm64\t1' \
    $'lint\t2\twindows\tarm64\t0'

run_case linux_to_windows_noncgo linux amd64 0 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\tlinux\tamd64\t0' \
    $'lint\t2\twindows\tamd64\t0'

run_case native_windows_noncgo windows amd64 0 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\twindows\tamd64\t0'

run_case windows_arm64_cgo_two_pass windows arm64 1 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\twindows\tarm64\t1' \
    $'lint\t2\twindows\tarm64\t0'

run_case_with_state changing_state_explicit_snapshot \
    linux amd64 1 \
    freebsd arm64 0 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\tlinux\tamd64\t1' \
    $'lint\t2\twindows\tamd64\t0'

run_refusal_case go_env_failure go-failure \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED'

run_refusal_case go_env_malformed go-malformed \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED'

run_refusal_case linter_generation_1_failure_is_fatal lint-failure-1 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\tlinux\tamd64\t1'

run_refusal_case linter_generation_2_failure_is_fatal lint-failure-2 \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change' \
    $'lint\t1\tlinux\tamd64\t1' \
    $'lint\t2\twindows\tamd64\t0'

run_refusal_case format_make_failure_is_fatal make-failure \
    $'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED' \
    $'format\tstate-change'

expected_case_count=11
[[ "$case_count" -eq "$expected_case_count" ]] ||
    fail "expected $expected_case_count mandatory cases, ran $case_count"

authority_refusal_count=0
assert_refusal_event_log() {
    local name="$1"
    shift

    local actual="$tmpdir/$name.events.actual"
    local expected="$tmpdir/$name.events.expected"
    "$cp_path" "$event_log" "$actual"
    if [[ "$#" -eq 0 ]]; then
        : >"$expected"
    else
        printf '%s\n' "$@" >"$expected"
    fi
    if ! "$diff_path" -u "$expected" "$actual"; then
        printf 'PR lint authority refusal %s observed unexpected work\n' "$name" >&2
        return 1
    fi
}

run_authority_refusal_case() {
    local name="$1"
    local mode="$2"
    authority_refusal_count=$((authority_refusal_count + 1))
    : >"$event_log"

    local config="$REPO_ROOT/.golangci.yml"
    local gowork=off
    local goflags=-mod=readonly
    local expected_goos=linux
    local expected_events=()
    case "$mode" in
        sibling-config)
            config="$tmpdir/.golangci.yaml"
            printf '%s\n' 'version: "2"' >"$config"
            ;;
        go-work)
            gowork="$tmpdir/go.work"
            printf '%s\n' 'go 1.26.0' >"$gowork"
            ;;
        vendor-mode)
            goflags=-mod=vendor
            ;;
        target-mismatch)
            expected_goos=windows
            expected_events+=($'go-env\t1\tenv\tGOOS\tGOARCH\tCGO_ENABLED')
            ;;
        *) fail "unknown authority refusal mode: $mode" ;;
    esac

    if "$BEADS_CI_ENV" -i \
        PATH= \
        BASH_ENV= \
        ENV= \
        HOME="${HOME:-}" \
        GOENV=off \
        GOWORK="$gowork" \
        GOFLAGS="$goflags" \
        GOOS=linux \
        GOARCH=amd64 \
        CGO_ENABLED=1 \
        GO_VERSION="$GO_VERSION" \
        BEADS_CI_TOOLCHAIN_BOUND=1 \
        BEADS_CI_EXPECTED_OUTER_OS="$BEADS_CI_EXPECTED_OUTER_OS" \
        BEADS_CI_BASH="$BEADS_CI_BASH" \
        BEADS_CI_ENV="$BEADS_CI_ENV" \
        BEADS_CI_GIT="$stub_dir/go" \
        BEADS_CI_GO="$stub_dir/go" \
        BEADS_CI_GOFMT="$stub_dir/go" \
        BEADS_CI_GOLANGCI_LINT="$stub_dir/golangci-lint" \
        BEADS_CI_GOLANGCI_CONFIG="$config" \
        BEADS_CI_MAKE="$stub_dir/make" \
        BEADS_CI_SED="$stub_dir/go" \
        BEADS_CI_UNAME="$BEADS_CI_UNAME" \
        BEADS_CI_WINDOWS_CMD='C:\bound\cmd.exe' \
        BEADS_CI_WINDOWS_GIT='C:\bound\git.exe' \
        BEADS_CI_TARGET_GOOS="$expected_goos" \
        BEADS_CI_TARGET_GOARCH=amd64 \
        BEADS_CI_TARGET_CGO_ENABLED=1 \
        PR_LINT_EXPECTED_CONFIG="$REPO_ROOT/.golangci.yml" \
        PR_LINT_SNAPSHOT_GOOS=linux \
        PR_LINT_SNAPSHOT_GOARCH=amd64 \
        PR_LINT_SNAPSHOT_CGO_ENABLED=1 \
        PR_LINT_NEXT_GOOS=freebsd \
        PR_LINT_NEXT_GOARCH=arm64 \
        PR_LINT_NEXT_CGO_ENABLED=0 \
        PR_LINT_EVENT_LOG="$event_log" \
        "$BEADS_CI_BASH" --noprofile --norc "$PR_LINT" \
        >"$tmpdir/$name.output" 2>&1; then
        fail "$name unexpectedly accepted ambiguous authority"
    fi
    assert_refusal_event_log "$name" "${expected_events[@]}"
}

run_authority_refusal_case sibling_config_is_ignored_only_by_exact_binding sibling-config
run_authority_refusal_case go_work_is_forbidden go-work
run_authority_refusal_case vendor_auto_mode_is_forbidden vendor-mode
run_authority_refusal_case public_target_mismatch_is_forbidden target-mismatch

expected_authority_refusal_count=4
[[ "$authority_refusal_count" -eq "$expected_authority_refusal_count" ]] ||
    fail "expected $expected_authority_refusal_count authority refusals, ran $authority_refusal_count"

{
    printf '#!%s\n' "$stub_bash"
    "$cat_path" <<'EOF'
printf '%s\n' 'gofmt failure stub executed' >"$0.receipt"
exit 73
EOF
} >"$stub_dir/gofmt-failure"
"$chmod_path" +x "$stub_dir/gofmt-failure"
false_gofmt_receipt="$stub_dir/gofmt-failure.receipt"
false_gofmt_output="$tmpdir/false-gofmt.output"
[[ ! -e "$false_gofmt_receipt" ]] ||
    fail "the nonzero gofmt execution receipt already exists"
false_gofmt_arguments=(
    --no-print-directory
    -f
    Makefile
    "CI_GOFMT=$stub_dir/gofmt-failure"
)
if [[ "$BEADS_CI_EXPECTED_OUTER_OS" == "windows" ]]; then
    false_gofmt_arguments+=(
        "WINDOWS_CMD_EXE=$BEADS_CI_WINDOWS_CMD"
        "GIT_WINDOWS_EXE=$BEADS_CI_WINDOWS_GIT"
        "CI_GIT=$BEADS_CI_WINDOWS_GIT"
    )
else
    false_gofmt_arguments+=("CI_GIT=$BEADS_CI_GIT")
fi
false_gofmt_arguments+=(fmt-check)
if "$BEADS_CI_MAKE" "${false_gofmt_arguments[@]}" \
    >"$false_gofmt_output" 2>&1; then
    fail "fmt-check accepted a nonzero gofmt producer"
fi
[[ -f "$false_gofmt_receipt" && ! -L "$false_gofmt_receipt" ]] ||
    fail "fmt-check failed without executing the nonzero gofmt producer"
IFS= read -r false_gofmt_receipt_value <"$false_gofmt_receipt" ||
    fail "could not read the nonzero gofmt execution receipt"
[[ "$false_gofmt_receipt_value" == "gofmt failure stub executed" ]] ||
    fail "the nonzero gofmt execution receipt was malformed"
false_gofmt_diagnostic_seen=0
while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "gofmt failed while checking formatting" ]]; then
        false_gofmt_diagnostic_seen=1
    fi
done <"$false_gofmt_output"
[[ "$false_gofmt_diagnostic_seen" -eq 1 ]] ||
    fail "fmt-check failed for an unrelated reason without the gofmt producer-status diagnostic"

printf 'PR lint target routing tests passed (%s mandatory cases; %s authority refusals; nonzero gofmt refused)\n' \
    "$case_count" "$authority_refusal_count"
