#!/bin/bash
# Resolve and bind the complete host/tool contract for the required PR lint gate.

set -euo pipefail

fail() {
    printf 'pr-lint host contract: %s\n' "$1" >&2
    exit 1
}

probe_only=0
case "$#" in
    0) ;;
    1)
        [[ "$1" == "--probe-only" ]] || fail "unknown argument: $1"
        [[ "${GITHUB_ACTIONS:-}" != "true" ]] ||
            fail "--probe-only is forbidden in GitHub Actions"
        probe_only=1
        ;;
    *) fail "expected no arguments or --probe-only" ;;
esac

script_source="${BASH_SOURCE[0]}"
case "$script_source" in
    */*) script_parent="${script_source%/*}" ;;
    *) script_parent="." ;;
esac
SCRIPT_DIR="$(cd -P -- "$script_parent" && pwd)"
REPO_ROOT="$(cd -P -- "$SCRIPT_DIR/../.." && pwd)"

require_absolute_executable() {
    local label="$1"
    local path="$2"
    [[ "$path" == /* ]] || fail "$label did not resolve to an absolute path: $path"
    [[ "$path" != *$'\n'* && "$path" != *$'\r'* ]] ||
        fail "$label path contains a line break"
    [[ -f "$path" && -x "$path" ]] || fail "$label is not an executable file: $path"
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
        target="$("$readlink_path" "$path")" || return 1
        if [[ "$target" == /* ]]; then
            path="$target"
        else
            path="$directory/$target"
        fi
    done
}

windows_path_to_unambiguous_posix() {
    local path="$1"
    local drive=""
    local remainder=""

    [[ "$path" =~ ^[A-Za-z]:[\\/] ]] || return 1
    [[ "$path" != *$'\n'* && "$path" != *$'\r'* ]] || return 1
    drive="${path:0:1}"
    remainder="${path:2}"
    remainder="${remainder//\\//}"
    printf '/%s%s' "$drive" "$remainder"
}

resolve_executable() {
    local name="$1"
    local path=""
    path="$(type -P -- "$name" 2>/dev/null)" ||
        fail "required executable is unavailable: $name"
    path="$(canonicalize_path "$path")" ||
        fail "could not canonicalize required executable: $name"
    require_absolute_executable "$name" "$path"
    printf '%s' "$path"
}

append_path_directory() {
    local executable="$1"
    local directory="${executable%/*}"
    case ":$curated_path:" in
        *":$directory:"*) ;;
        *) curated_path="${curated_path:+$curated_path:}$directory" ;;
    esac
}

expected_outer_os="${BEADS_CI_EXPECTED_OUTER_OS:-}"
if [[ -z "$expected_outer_os" ]]; then
    [[ "${GITHUB_ACTIONS:-}" != "true" ]] ||
        fail "BEADS_CI_EXPECTED_OUTER_OS is mandatory in GitHub Actions"
    case "${OSTYPE:-}" in
        linux*) expected_outer_os="linux" ;;
        darwin*) expected_outer_os="macos" ;;
        cygwin*|msys*) expected_outer_os="windows" ;;
        *) fail "cannot derive a supported local outer OS from OSTYPE=${OSTYPE:-<unset>}" ;;
    esac
fi
case "$expected_outer_os" in
    linux|macos|windows) ;;
    *) fail "unsupported expected outer OS: $expected_outer_os" ;;
esac

readlink_path="$(type -P -- readlink 2>/dev/null)" ||
    fail "required executable is unavailable: readlink"
require_absolute_executable "readlink" "$readlink_path"
readlink_path="$(canonicalize_path "$readlink_path")" ||
    fail "could not canonicalize readlink"
require_absolute_executable "canonical readlink" "$readlink_path"

invoked_bash="${BASH:-}"
if [[ "$expected_outer_os" == "windows" &&
      "$invoked_bash" =~ ^[A-Za-z]:[\\/] ]]; then
    invoked_bash_alias="$(type -P -- bash 2>/dev/null)" ||
        fail "could not resolve a POSIX alias for the current Bash"
    [[ "$invoked_bash_alias" == /* &&
       -f "$invoked_bash_alias" &&
       -x "$invoked_bash_alias" &&
       "$invoked_bash_alias" -ef "$invoked_bash" ]] ||
        fail "the POSIX Bash alias does not name the running interpreter"
    invoked_bash="$invoked_bash_alias"
fi
require_absolute_executable "current Bash" "$invoked_bash"
bash_path="$(canonicalize_path "$invoked_bash")" ||
    fail "could not canonicalize the current Bash"
require_absolute_executable "canonical current Bash" "$bash_path"
[[ "$bash_path" -ef "$invoked_bash" ]] ||
    fail "canonical Bash identity does not name the running interpreter"
((BASH_VERSINFO[0] > 3 ||
   (BASH_VERSINFO[0] == 3 && BASH_VERSINFO[1] >= 2))) ||
    fail "Bash 3.2 or newer is required"

env_path="$(resolve_executable env)"
cat_path="$(resolve_executable cat)"
chmod_path="$(resolve_executable chmod)"
cp_path="$(resolve_executable cp)"
diff_path="$(resolve_executable diff)"
go_path="$(resolve_executable go)"
mkdir_path="$(resolve_executable mkdir)"
mktemp_path="$(resolve_executable mktemp)"
rm_path="$(resolve_executable rm)"
rmdir_path="$(resolve_executable rmdir)"
sed_path="$(resolve_executable sed)"
uname_path="$(resolve_executable uname)"
cygpath_path=""
windows_cmd_path=""
windows_git_path=""
windows_bundle_root=""
if [[ "$expected_outer_os" == "windows" ]]; then
    cygpath_path="$(resolve_executable cygpath)"
fi
invoking_git="${BEADS_CI_INVOKING_GIT:-}"
[[ -n "$invoking_git" ]] ||
    fail "CI_GIT identity did not cross the public Make boundary"
if [[ "$expected_outer_os" == "windows" &&
      "$invoking_git" =~ ^[A-Za-z]:[\\/] ]]; then
    invoking_git="$(windows_path_to_unambiguous_posix "$invoking_git")" ||
        fail "could not translate the invoking Git without a mount alias"
fi
case "$invoking_git" in
    /*)
        git_path="$(canonicalize_path "$invoking_git")" ||
            fail "could not canonicalize the invoking Git"
        require_absolute_executable "invoking Git" "$git_path"
        ;;
    *) git_path="$(resolve_executable "$invoking_git")" ;;
esac

make_candidate="${BEADS_CI_INVOKING_MAKE:-}"
make_invocation_path="$make_candidate"
make_origin="${BEADS_CI_INVOKING_MAKE_ORIGIN:-}"
invoking_makefile_list="${BEADS_CI_INVOKING_MAKEFILE_LIST:-}"
makeflags_origin="${BEADS_CI_INVOKING_MAKEFLAGS_ORIGIN:-}"
invoking_mflags="${BEADS_CI_INVOKING_MFLAGS:-}"
invoking_gnumakeflags="${BEADS_CI_INVOKING_GNUMAKEFLAGS:-}"
makefiles_origin="${BEADS_CI_INVOKING_MAKEFILES_ORIGIN:-}"
invoking_makefiles="${BEADS_CI_INVOKING_MAKEFILES:-}"
[[ "$invoking_makefile_list" == "Makefile" ]] ||
    fail "the public lint boundary must load exactly -f Makefile"
if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    [[ "$make_origin" == "default" ]] ||
        fail "MAKE origin must be default in GitHub Actions, observed: ${make_origin:-<unset>}"
    [[ -n "$make_candidate" ]] ||
        fail "the absolute invoking GNU Make did not cross the public boundary"
    [[ "$makeflags_origin" == "file" ]] ||
        fail "MAKEFLAGS origin must be file in GitHub Actions, observed: ${makeflags_origin:-<unset>}"
    [[ -z "$invoking_mflags" ]] ||
        fail "MFLAGS must be empty at the public Make boundary"
    [[ -z "$invoking_gnumakeflags" ]] ||
        fail "GNUMAKEFLAGS must be absent at the public Make boundary"
    # This post-parse check is diagnostic, not a sandbox for command-line
    # MAKEFILES: protected workflows guarantee absence through curated
    # environment and fixed argv before Make starts.
    # Stock Apple GNU Make 3.81 leaves an absent MAKEFILES undefined; newer
    # GNU Make exposes the same empty built-in with default origin.
    [[ ("$makefiles_origin" == "default" ||
        "$makefiles_origin" == "undefined") &&
       -z "$invoking_makefiles" ]] ||
        fail "MAKEFILES must be absent at the public Make boundary"
fi
if [[ -n "$make_candidate" ]]; then
    if [[ "$expected_outer_os" == "windows" &&
          "$make_candidate" =~ ^[A-Za-z]:[\\/] ]]; then
        make_candidate="$(windows_path_to_unambiguous_posix "$make_candidate")" ||
            fail "could not translate invoking GNU Make without a mount alias"
    fi
    make_path="$(canonicalize_path "$make_candidate")" ||
        fail "could not canonicalize the invoking GNU Make"
    require_absolute_executable "invoking GNU Make" "$make_path"
else
    make_path="$(resolve_executable make)"
fi

uname_system="$("$uname_path" -s)"
case "$expected_outer_os" in
    linux)
        [[ "${OSTYPE:-}" == linux* && "$uname_system" == "Linux" ]] ||
            fail "expected Linux, observed OSTYPE=${OSTYPE:-<unset>} uname=$uname_system"
        expected_make_host_pattern="linux"
        ;;
    macos)
        [[ "${OSTYPE:-}" == darwin* && "$uname_system" == "Darwin" ]] ||
            fail "expected macOS, observed OSTYPE=${OSTYPE:-<unset>} uname=$uname_system"
        expected_make_host_pattern="darwin"
        ;;
    windows)
        [[ "${OSTYPE:-}" == cygwin* || "${OSTYPE:-}" == msys* ]] ||
            fail "expected a Windows POSIX Bash, observed OSTYPE=${OSTYPE:-<unset>}"
        [[ "$uname_system" == MINGW*_NT-* ||
           "$uname_system" == MSYS*_NT-* ||
           "$uname_system" == CYGWIN*_NT-* ]] ||
            fail "expected a Windows POSIX uname, observed $uname_system"
        expected_make_host_pattern="windows"
        comspec="${COMSPEC:-${ComSpec:-}}"
        [[ -n "$comspec" ]] || fail "COMSPEC is required for native Windows Make"
        windows_cmd_posix="$(windows_path_to_unambiguous_posix "$comspec")" ||
            fail "could not translate the Windows command processor"
        windows_cmd_posix="$(canonicalize_path "$windows_cmd_posix")"
        require_absolute_executable "Windows command processor" "$windows_cmd_posix"
        windows_cmd_path="$("$cygpath_path" -aw "$windows_cmd_posix")"
        ;;
esac

probe_value='spaces * ? [ ] $ " preserved'
# The single-quoted program is evaluated by the freshly started bound Bash.
# shellcheck disable=SC2016
probe_output="$(
    "$env_path" -i \
        BEADS_HOST_PROBE="$probe_value" \
        "$bash_path" --noprofile --norc -c \
        '[[ -z "${BASH_ENV+x}" && -z "${ENV+x}" &&
            "$BEADS_HOST_PROBE" == "$1" ]] || exit 71
         printf "%s" "$BEADS_HOST_PROBE"' \
        beads-host-probe "$probe_value"
)" || fail "Bash startup, environment, or quoting probe failed"
[[ "$probe_output" == "$probe_value" ]] ||
    fail "Bash quoting probe changed its argument"

go_module_version=""
while IFS=' ' read -r directive value _; do
    if [[ "$directive" == "go" ]]; then
        [[ -z "$go_module_version" ]] || fail "go.mod contains multiple go directives"
        go_module_version="${value%$'\r'}"
    fi
done <"$REPO_ROOT/go.mod"
[[ "$go_module_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "go.mod does not contain one exact three-component Go version"

go_version_output="$(
    GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local GOEXPERIMENT='' \
        "$go_path" version
)" || fail "the bound Go executable could not report its version"
[[ "$go_version_output" == "go version go${go_module_version} "* ]] ||
    fail "expected Go $go_module_version, observed: $go_version_output"
if ! go_host_output="$(
    GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local GOEXPERIMENT='' \
        "$go_path" env GOHOSTOS GOHOSTARCH
)"; then
    fail "the bound Go executable could not report its host tuple"
fi
go_host_values=()
while IFS= read -r value; do
    go_host_values+=("$value")
done <<<"$go_host_output"
[[ "${#go_host_values[@]}" -eq 2 ]] ||
    fail "Go returned an invalid host tuple"
go_host_os="${go_host_values[0]}"
go_host_arch="${go_host_values[1]}"
case "$expected_outer_os" in
    linux) [[ "$go_host_os" == "linux" ]] || fail "Go host OS is $go_host_os, expected linux" ;;
    macos) [[ "$go_host_os" == "darwin" ]] || fail "Go host OS is $go_host_os, expected darwin" ;;
    windows) [[ "$go_host_os" == "windows" ]] || fail "Go host OS is $go_host_os, expected windows" ;;
esac
[[ "$go_host_arch" =~ ^[a-z0-9_]+$ ]] ||
    fail "Go returned an invalid host architecture: $go_host_arch"

target_source="local"
if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    target_source="hosted"
    for variable in \
        BEADS_CI_EXPECTED_TARGET_GOOS \
        BEADS_CI_EXPECTED_TARGET_GOARCH \
        BEADS_CI_EXPECTED_TARGET_CGO_ENABLED; do
        [[ -n "${!variable:-}" ]] ||
            fail "$variable is mandatory in GitHub Actions"
    done
    target_goos="$BEADS_CI_EXPECTED_TARGET_GOOS"
    target_goarch="$BEADS_CI_EXPECTED_TARGET_GOARCH"
    target_cgo_enabled="$BEADS_CI_EXPECTED_TARGET_CGO_ENABLED"
    for variable in GOOS GOARCH CGO_ENABLED; do
        case "$variable" in
            GOOS)
                expected_value="$target_goos"
                present="${BEADS_CI_AMBIENT_GOOS_PRESENT:-}"
                observed="${BEADS_CI_AMBIENT_GOOS:-}"
                ;;
            GOARCH)
                expected_value="$target_goarch"
                present="${BEADS_CI_AMBIENT_GOARCH_PRESENT:-}"
                observed="${BEADS_CI_AMBIENT_GOARCH:-}"
                ;;
            CGO_ENABLED)
                expected_value="$target_cgo_enabled"
                present="${BEADS_CI_AMBIENT_CGO_ENABLED_PRESENT:-}"
                observed="${BEADS_CI_AMBIENT_CGO_ENABLED:-}"
                ;;
        esac
        [[ "$present" == "0" || "$present" == "1" ]] ||
            fail "the public Make boundary omitted $variable presence authority"
        if [[ "$present" == "1" && "$observed" != "$expected_value" ]]; then
            fail "$variable conflicts with the protected hosted target"
        fi
    done
    [[ "$target_goos" == "$go_host_os" &&
       "$target_goarch" == "$go_host_arch" ]] ||
        fail "the protected hosted target must equal the actual Go host"
else
    if ! target_output="$(
        GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local GOEXPERIMENT='' \
            "$go_path" env GOOS GOARCH CGO_ENABLED
    )"; then
        fail "the bound Go executable could not report its local target tuple"
    fi
    target_values=()
    while IFS= read -r value; do
        target_values+=("$value")
    done <<<"$target_output"
    [[ "${#target_values[@]}" -eq 3 ]] ||
        fail "Go returned an invalid local target tuple"
    target_goos="${target_values[0]}"
    target_goarch="${target_values[1]}"
    target_cgo_enabled="${target_values[2]}"
fi
[[ "$target_goos" =~ ^[a-z0-9_]+$ &&
   "$target_goarch" =~ ^[a-z0-9_]+$ &&
   "$target_cgo_enabled" =~ ^[01]$ ]] ||
    fail "invalid selected target: $target_goos/$target_goarch cgo=$target_cgo_enabled"
target_supported=0
if ! supported_targets_output="$(
    GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local GOEXPERIMENT='' \
        "$go_path" tool dist list
)"; then
    fail "the bound Go executable could not enumerate supported Go targets"
fi
while IFS= read -r supported_target; do
    if [[ "$supported_target" == "$target_goos/$target_goarch" ]]; then
        target_supported=1
        break
    fi
done <<<"$supported_targets_output"
[[ "$target_supported" -eq 1 ]] ||
    fail "unsupported selected Go target: $target_goos/$target_goarch"

sed_go_version="$("$sed_path" -n 's/^go //p' "$REPO_ROOT/go.mod")"
sed_go_version="${sed_go_version%$'\r'}"
[[ "$sed_go_version" == "$go_module_version" ]] ||
    fail "bound sed did not parse the protected Go version"

linter_config_path="$REPO_ROOT/.golangci.yml"
[[ "$linter_config_path" == /* &&
   -f "$linter_config_path" &&
   ! -L "$linter_config_path" ]] ||
    fail "the protected golangci-lint configuration is not one regular file"

go_directory="${go_path%/*}"
case "$expected_outer_os" in
    windows) gofmt_path="$go_directory/gofmt.exe" ;;
    *) gofmt_path="$go_directory/gofmt" ;;
esac
gofmt_path="$(canonicalize_path "$gofmt_path")" ||
    fail "could not canonicalize gofmt from the bound Go toolchain"
require_absolute_executable "gofmt from the bound Go toolchain" "$gofmt_path"

make_version_output="$("$make_path" --version)"
make_version_first_line="${make_version_output%%$'\n'*}"
make_version_first_line="${make_version_first_line%$'\r'}"
[[ "$make_version_first_line" =~ ^GNU\ Make\ ([0-9]+\.[0-9]+(\.[0-9]+)?)$ ]] ||
    fail "expected GNU Make, observed: $make_version_first_line"
if [[ -n "${BEADS_CI_EXPECTED_MAKE_VERSION:-}" ]]; then
    [[ "$make_version_first_line" == "GNU Make ${BEADS_CI_EXPECTED_MAKE_VERSION}" ]] ||
        fail "expected GNU Make ${BEADS_CI_EXPECTED_MAKE_VERSION}, observed: $make_version_first_line"
fi
if [[ "${GITHUB_ACTIONS:-}" == "true" &&
      ("$expected_outer_os" == "macos" ||
       "$expected_outer_os" == "windows") ]]; then
    [[ -n "${BEADS_CI_EXPECTED_MAKE_VERSION:-}" ]] ||
        fail "the exact GNU Make version is mandatory on $expected_outer_os"
fi
if [[ "${GITHUB_ACTIONS:-}" == "true" &&
      "$expected_outer_os" == "macos" ]]; then
    [[ "$make_invocation_path" == "/usr/bin/make" ]] ||
        fail "stock macOS GNU Make must be invoked through /usr/bin/make"
fi

# GNU Make, not this shell, expands MAKE_HOST. Feed the probe as a makefile so
# this remains valid on the stock GNU Make 3.81 supplied by macOS.
# shellcheck disable=SC2016
make_host_output="$(
    printf '%s\n' \
        '$(info MAKE_HOST=$(MAKE_HOST))' \
        '.PHONY: noop' \
        'noop: ;' |
        "$make_path" --no-print-directory -f - noop
)"
make_host=""
make_host_line_count=0
while IFS= read -r line; do
    if [[ "$line" == MAKE_HOST=* ]]; then
        make_host_line_count=$((make_host_line_count + 1))
        [[ "$make_host_line_count" -eq 1 ]] ||
            fail "GNU Make emitted multiple MAKE_HOST identities"
        make_host="${line#MAKE_HOST=}"
        make_host="${make_host%$'\r'}"
    fi
done <<<"$make_host_output"
[[ "$make_host_line_count" -eq 1 ]] || fail "GNU Make did not emit MAKE_HOST"
case "$expected_make_host_pattern" in
    linux) [[ "$make_host" == *linux* ]] || fail "GNU Make host is $make_host, expected Linux" ;;
    darwin)
        if [[ -n "$make_host" ]]; then
            [[ "$make_host" == *darwin* ]] ||
                fail "GNU Make host is $make_host, expected macOS"
        else
            # Apple's stock GNU Make 3.81 leaves MAKE_HOST empty. Bind that
            # exceptional identity to the original immutable system leaf and
            # exact version after the outer Darwin identity has already been
            # proved. make_path is the validated canonical target and is kept
            # only for execution and diagnostics: /usr/bin/make currently
            # resolves inside Xcode on hosted macOS.
            # Do not parse the remaining free-form build banner: current hosted
            # images are allowed to change it without changing either authority.
            if [[ "$make_version_first_line" != "GNU Make 3.81" ]]; then
                fail "GNU Make omitted MAKE_HOST outside the stock macOS 3.81 contract: invocation=$make_invocation_path canonical=$make_path version=$make_version_first_line"
            fi
        fi
        ;;
    windows)
        [[ "$make_host" == "Windows32" || "$make_host" == "mingw32" ||
           "$make_host" == *-mingw32 ]] ||
            fail "GNU Make host is $make_host, expected native Windows"
        ;;
esac

git_version_output="$("$git_path" --version)"
[[ "$git_version_output" == "git version "* ]] ||
    fail "bound Git did not report a Git version"
if [[ "$expected_outer_os" == "windows" ]]; then
    invoking_windows_git="${BEADS_CI_INVOKING_WINDOWS_GIT:-}"
    [[ -n "$invoking_windows_git" ]] ||
        fail "GIT_WINDOWS_EXE identity did not cross the public Make boundary"
    if [[ "$invoking_windows_git" =~ ^[A-Za-z]:[\\/] ]]; then
        invoking_windows_git="$(
            windows_path_to_unambiguous_posix "$invoking_windows_git"
        )" || fail "could not translate GIT_WINDOWS_EXE without a mount alias"
    fi
    invoking_windows_git="$(canonicalize_path "$invoking_windows_git")" ||
        fail "could not canonicalize GIT_WINDOWS_EXE"
    require_absolute_executable "GIT_WINDOWS_EXE" "$invoking_windows_git"

    git_exec_path="$(
        GIT_CONFIG_NOSYSTEM=1 \
        GIT_CONFIG_GLOBAL=/dev/null \
            "$invoking_windows_git" --exec-path
    )"
    if [[ "$git_exec_path" =~ ^[A-Za-z]:[\\/] ]]; then
        git_exec_path="$(
            windows_path_to_unambiguous_posix "$git_exec_path"
        )" || fail "could not translate the bound Git exec path"
    fi
    git_exec_path="$(canonicalize_path "$git_exec_path")" ||
        fail "could not canonicalize the bound Git exec path"
    windows_bundle_root="$(cd -P -- "$git_exec_path/../../.." && pwd)" ||
        fail "could not derive the Windows Git bundle root"
    bundle_path() {
        local relative="$1"
        if [[ "$windows_bundle_root" == "/" ]]; then
            printf '/%s' "$relative"
        else
            printf '%s/%s' "$windows_bundle_root" "$relative"
        fi
    }

    invoking_git_in_bundle=0
    resolved_git_in_bundle=0
    for relative_git in \
        cmd/git.exe cmd/git \
        bin/git.exe bin/git \
        mingw64/bin/git.exe mingw64/bin/git; do
        candidate="$(bundle_path "$relative_git")"
        [[ "$invoking_windows_git" == "$candidate" ]] &&
            invoking_git_in_bundle=1
        [[ "$git_path" == "$candidate" ]] &&
            resolved_git_in_bundle=1
    done
    [[ "$invoking_git_in_bundle" -eq 1 ]] ||
        fail "GIT_WINDOWS_EXE is outside its derived Windows Git bundle"
    [[ "$resolved_git_in_bundle" -eq 1 ]] ||
        fail "the resolved Git executable is outside the GIT_WINDOWS_EXE bundle"
    windows_git_path="$("$cygpath_path" -aw "$invoking_windows_git")"

    for binding in \
        "Bash|$bash_path|$(bundle_path usr/bin/bash.exe)" \
        "cat|$cat_path|$(bundle_path usr/bin/cat.exe)" \
        "chmod|$chmod_path|$(bundle_path usr/bin/chmod.exe)" \
        "cp|$cp_path|$(bundle_path usr/bin/cp.exe)" \
        "diff|$diff_path|$(bundle_path usr/bin/diff.exe)" \
        "env|$env_path|$(bundle_path usr/bin/env.exe)" \
        "mkdir|$mkdir_path|$(bundle_path usr/bin/mkdir.exe)" \
        "mktemp|$mktemp_path|$(bundle_path usr/bin/mktemp.exe)" \
        "readlink|$readlink_path|$(bundle_path usr/bin/readlink.exe)" \
        "rm|$rm_path|$(bundle_path usr/bin/rm.exe)" \
        "rmdir|$rmdir_path|$(bundle_path usr/bin/rmdir.exe)" \
        "sed|$sed_path|$(bundle_path usr/bin/sed.exe)" \
        "uname|$uname_path|$(bundle_path usr/bin/uname.exe)" \
        "cygpath|$cygpath_path|$(bundle_path usr/bin/cygpath.exe)"; do
        label="${binding%%|*}"
        paths="${binding#*|}"
        observed="${paths%%|*}"
        expected="${paths#*|}"
        require_absolute_executable "Windows Git bundle $label" "$expected"
        expected_alias="${expected%.exe}"
        observed_windows="$("$cygpath_path" -aw "$observed")" ||
            fail "could not translate the resolved $label to Windows authority"
        observed_physical="$(
            windows_path_to_unambiguous_posix "$observed_windows"
        )" || fail "could not normalize the resolved $label"
        [[ "$observed_physical" == "$expected" ||
           "$observed_physical" == "$expected_alias" ]] ||
            fail "$label resolved outside the GIT_WINDOWS_EXE bundle"
    done
fi
git_root="$(
    GIT_CONFIG_NOSYSTEM=1 \
    GIT_CONFIG_GLOBAL=/dev/null \
        "$git_path" -C "$REPO_ROOT" rev-parse --show-toplevel
)"
expected_git_root="$REPO_ROOT"
if [[ "$expected_outer_os" == "windows" ]]; then
    expected_git_root="$(cd -P -- "$REPO_ROOT" && pwd -W)"
    expected_git_root="${expected_git_root//\\//}"
fi
[[ "$git_root" == "$expected_git_root" ]] ||
    fail "bound Git resolved the wrong repository root: $git_root"

temporary_linter_root=""
temporary_linter_parent=""
temporary_linter_binary=""
cleanup_authorized=0
cleanup() {
    local original_status=$?
    local cleanup_failed=0
    local observed_parent=""
    local observed_root=""
    local basename=""
    local binary_leaf=""

    # This proves canonical path, type, and shape immediately before mutation;
    # it does not claim a portable file identity. The hosted runner's private
    # RUNNER_TEMP is trusted to have one owner and no concurrent same-type
    # replacement writer while this handler runs.
    if [[ "$cleanup_authorized" -eq 1 ]]; then
        basename="${temporary_linter_root##*/}"
        if [[ "$basename" =~ ^beads-pr-lint-tools\.[A-Za-z0-9]{8}$ &&
              "${temporary_linter_root%/*}" == "$temporary_linter_parent" &&
              -d "$temporary_linter_parent" &&
              ! -L "$temporary_linter_parent" ]]; then
            observed_parent="$(cd -P -- "$temporary_linter_parent" && pwd)" ||
                cleanup_failed=1
            if [[ "$observed_parent" != "$temporary_linter_parent" ]]; then
                cleanup_failed=1
            elif [[ ! -e "$temporary_linter_root" &&
                    ! -L "$temporary_linter_root" ]]; then
                :
            elif [[ -d "$temporary_linter_root" &&
                    ! -L "$temporary_linter_root" ]]; then
                observed_root="$(cd -P -- "$temporary_linter_root" && pwd)" ||
                    cleanup_failed=1
                if [[ "$observed_root" != "$temporary_linter_root" ]]; then
                    cleanup_failed=1
                else
                    binary_leaf="${temporary_linter_binary##*/}"
                    if ! (
                        cd -P -- "$temporary_linter_root" || exit 1
                        [[ "$PWD" == "$temporary_linter_root" ]] || exit 1
                        shopt -s dotglob nullglob
                        entries=(./*)
                        if [[ -n "$temporary_linter_binary" ]]; then
                            [[ "$binary_leaf" == "golangci-lint" ||
                               "$binary_leaf" == "golangci-lint.exe" ]] ||
                                exit 1
                            [[ "$temporary_linter_binary" == "$temporary_linter_root/$binary_leaf" ]] ||
                                exit 1
                            [[ "${#entries[@]}" -eq 1 &&
                               "${entries[0]}" == "./$binary_leaf" &&
                               -f "./$binary_leaf" &&
                               ! -L "./$binary_leaf" ]] ||
                                exit 1
                            "$rm_path" -- "./$binary_leaf" || exit 1
                            [[ ! -e "./$binary_leaf" &&
                               ! -L "./$binary_leaf" ]] ||
                                exit 1
                        else
                            [[ "${#entries[@]}" -eq 0 ]] || exit 1
                        fi
                        remaining_entries=(./*)
                        [[ "${#remaining_entries[@]}" -eq 0 ]] || exit 1
                    ); then
                        cleanup_failed=1
                    fi
                    if [[ "$cleanup_failed" -eq 0 ]]; then
                        observed_parent="$(
                            cd -P -- "$temporary_linter_parent" && pwd
                        )" || cleanup_failed=1
                    fi
                    if [[ "$cleanup_failed" -eq 0 &&
                          ("$observed_parent" != "$temporary_linter_parent" ||
                           ! -d "$temporary_linter_root" ||
                           -L "$temporary_linter_root") ]]; then
                        cleanup_failed=1
                    fi
                    if [[ "$cleanup_failed" -eq 0 ]] &&
                       ! "$rmdir_path" -- "$temporary_linter_root"; then
                        cleanup_failed=1
                    fi
                    if [[ "$cleanup_failed" -eq 0 &&
                          (-e "$temporary_linter_root" ||
                           -L "$temporary_linter_root") ]]; then
                        cleanup_failed=1
                    fi
                fi
            else
                cleanup_failed=1
            fi
        else
            cleanup_failed=1
        fi
    fi
    if [[ "$cleanup_failed" -ne 0 ]]; then
        printf 'pr-lint host contract: refused unsafe private-tool cleanup: %s\n' \
            "$temporary_linter_root" >&2
        original_status=1
    fi
    trap - EXIT
    exit "$original_status"
}
trap cleanup EXIT

if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    [[ -n "${BEADS_CI_EXPECTED_GOLANGCI_LINT_VERSION:-}" ]] ||
        fail "the expected golangci-lint version is mandatory in GitHub Actions"
    [[ "${BEADS_CI_INSTALL_GOLANGCI_LINT:-}" == "1" ]] ||
        fail "GitHub Actions must install a private bound golangci-lint"
fi
expected_linter_version="${BEADS_CI_EXPECTED_GOLANGCI_LINT_VERSION:-2.10.1}"
[[ "$expected_linter_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "invalid expected golangci-lint version: $expected_linter_version"

case "${BEADS_CI_INSTALL_GOLANGCI_LINT:-0}" in
    0|1) ;;
    *) fail "BEADS_CI_INSTALL_GOLANGCI_LINT must be 0 or 1" ;;
esac
if [[ "${BEADS_CI_INSTALL_GOLANGCI_LINT:-0}" == "1" ]]; then
    runner_temp="${RUNNER_TEMP:-}"
    if [[ "$expected_outer_os" == "windows" &&
          "$runner_temp" =~ ^[A-Za-z]:[\\/] ]]; then
        runner_temp="$(
            windows_path_to_unambiguous_posix "$runner_temp"
        )" || fail "could not translate RUNNER_TEMP without a mount alias"
    fi
    [[ "$runner_temp" == /* && -d "$runner_temp" ]] ||
        fail "RUNNER_TEMP must be an existing absolute directory for tool installation"
    [[ ! -L "$runner_temp" ]] ||
        fail "RUNNER_TEMP must not be a symbolic link"
    runner_temp="$(cd -P -- "$runner_temp" && pwd)" ||
        fail "could not canonicalize RUNNER_TEMP"
    created_linter_root="$(
        "$mktemp_path" -d "$runner_temp/beads-pr-lint-tools.XXXXXXXX"
    )" || fail "could not create the private linter directory"
    created_basename="${created_linter_root##*/}"
    created_parent="${created_linter_root%/*}"
    [[ "$created_basename" =~ ^beads-pr-lint-tools\.[A-Za-z0-9]{8}$ &&
       "$created_parent" == "$runner_temp" &&
       -d "$created_linter_root" &&
       ! -L "$created_linter_root" ]] ||
        fail "mktemp returned an unauthorized private linter directory"
    canonical_linter_root="$(cd -P -- "$created_linter_root" && pwd)" ||
        fail "could not canonicalize the private linter directory"
    [[ "$canonical_linter_root" == "$runner_temp/$created_basename" ]] ||
        fail "mktemp returned a nested or redirected private linter directory"
    temporary_linter_parent="$runner_temp"
    temporary_linter_root="$canonical_linter_root"
    cleanup_authorized=1

    install_path="${go_path%/*}:${git_path%/*}:${env_path%/*}"
    install_environment=(
        "PATH=$install_path"
        "HOME=${HOME:-}"
        "GOBIN=$temporary_linter_root"
        "GOENV=off"
        "GOWORK=off"
        "GOFLAGS="
        "GOTOOLCHAIN=local"
        "GOEXPERIMENT="
        "GOOS=$go_host_os"
        "GOARCH=$go_host_arch"
        "CGO_ENABLED=0"
        "GIT_CONFIG_NOSYSTEM=1"
        "GIT_CONFIG_GLOBAL=/dev/null"
        "GIT_TERMINAL_PROMPT=0"
    )
    case "$go_host_arch" in
        amd64) install_environment+=("GOAMD64=v1") ;;
        arm64) install_environment+=("GOARM64=v8.0") ;;
        *) fail "unsupported private-linter host architecture: $go_host_arch" ;;
    esac
    for name in \
        APPDATA \
        LOCALAPPDATA \
        PROGRAMDATA \
        SYSTEMROOT \
        SystemRoot \
        TEMP \
        TMP \
        TMPDIR \
        USER \
        USERPROFILE \
        WINDIR; do
        if [[ -n "${!name+x}" ]]; then
            install_environment+=("$name=${!name}")
        fi
    done
    "$env_path" -i \
        "${install_environment[@]}" \
        "$go_path" install \
        "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${expected_linter_version}"
    case "$expected_outer_os" in
        windows) linter_path="$temporary_linter_root/golangci-lint.exe" ;;
        *) linter_path="$temporary_linter_root/golangci-lint" ;;
    esac
    require_absolute_executable "newly installed golangci-lint" "$linter_path"
    [[ ! -L "$linter_path" &&
       "${linter_path%/*}" == "$temporary_linter_root" ]] ||
        fail "newly installed golangci-lint is not one direct regular file"
    canonical_linter_binary="$(canonicalize_path "$linter_path")" ||
        fail "could not canonicalize the newly installed golangci-lint"
    [[ "$canonical_linter_binary" == "$linter_path" ]] ||
        fail "newly installed golangci-lint resolved outside its proved direct path"
    temporary_linter_binary="$canonical_linter_binary"
else
    linter_path="$(resolve_executable golangci-lint)"
fi

linter_version_output="$("$linter_path" version)"
expected_linter_version_regex="${expected_linter_version//./\\.}"
[[ "$linter_version_output" =~ (^|[[:space:]])version[[:space:]]${expected_linter_version_regex}([[:space:]]|$) ]] ||
    fail "expected golangci-lint $expected_linter_version, observed: $linter_version_output"

if ! cc_name="$(
    GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local GOEXPERIMENT='' \
        "$go_path" env CC
)"; then
    fail "the bound Go executable could not report its C compiler"
fi
if ! cxx_name="$(
    GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local GOEXPERIMENT='' \
        "$go_path" env CXX
)"; then
    fail "the bound Go executable could not report its C++ compiler"
fi
case "$cc_name" in
    /*)
        cc_path="$(canonicalize_path "$cc_name")"
        require_absolute_executable "Go C compiler" "$cc_path"
        ;;
    *) cc_path="$(resolve_executable "$cc_name")" ;;
esac
case "$cxx_name" in
    /*)
        cxx_path="$(canonicalize_path "$cxx_name")"
        require_absolute_executable "Go C++ compiler" "$cxx_path"
        ;;
    *) cxx_path="$(resolve_executable "$cxx_name")" ;;
esac
if [[ -n "${BEADS_CI_EXPECTED_CC_VERSION:-}" ]]; then
    expected_cc_version="$BEADS_CI_EXPECTED_CC_VERSION"
    [[ "$expected_cc_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
        fail "invalid expected C compiler version: $expected_cc_version"
    cc_version_output="$("$cc_path" --version)"
    cc_version_first_line="${cc_version_output%%$'\n'*}"
    [[ "$cc_version_first_line" =~ (^|[[:space:]])${expected_cc_version}([[:space:]]|$) ]] ||
        fail "expected C compiler $expected_cc_version, observed: $cc_version_first_line"
    cxx_version_output="$("$cxx_path" --version)"
    cxx_version_first_line="${cxx_version_output%%$'\n'*}"
    [[ "$cxx_version_first_line" =~ (^|[[:space:]])${expected_cc_version}([[:space:]]|$) ]] ||
        fail "expected C++ compiler $expected_cc_version, observed: $cxx_version_first_line"
fi
if [[ "$expected_outer_os" == "windows" && "$target_cgo_enabled" == "1" ]]; then
    [[ "${GITHUB_ACTIONS:-}" != "true" ||
       -n "${BEADS_CI_EXPECTED_CC_VERSION:-}" ]] ||
        fail "native Windows CGO requires an exact protected compiler version"
    cc_machine="$("$cc_path" -dumpmachine)"
    [[ "$cc_machine" == "x86_64-w64-mingw32" ]] ||
        fail "expected an x86_64 MinGW compiler, observed: $cc_machine"
    cxx_machine="$("$cxx_path" -dumpmachine)"
    [[ "$cxx_machine" == "x86_64-w64-mingw32" ]] ||
        fail "expected an x86_64 MinGW C++ compiler, observed: $cxx_machine"
fi

curated_path=""
for executable in \
    "$linter_path" \
    "$cat_path" \
    "$chmod_path" \
    "$cp_path" \
    "$diff_path" \
    "$go_path" \
    "$gofmt_path" \
    "$make_path" \
    "$git_path" \
    "$cc_path" \
    "$cxx_path" \
    "$bash_path" \
    "$env_path" \
    "$mkdir_path" \
    "$mktemp_path" \
    "$rm_path" \
    "$rmdir_path" \
    "$sed_path" \
    "$uname_path" \
    "$readlink_path"; do
    append_path_directory "$executable"
done
if [[ -n "$cygpath_path" ]]; then
    append_path_directory "$cygpath_path"
fi

fixture_temp_parent="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
if [[ "$expected_outer_os" == "windows" &&
      "$fixture_temp_parent" =~ ^[A-Za-z]:[\\/] ]]; then
    fixture_temp_parent="$("$cygpath_path" -u "$fixture_temp_parent")"
fi
[[ "$fixture_temp_parent" == /* &&
   -d "$fixture_temp_parent" &&
   ! -L "$fixture_temp_parent" ]] ||
    fail "the test-fixture temp parent is not one absolute regular directory"
fixture_temp_parent="$(cd -P -- "$fixture_temp_parent" && pwd)" ||
    fail "could not canonicalize the test-fixture temp parent"

printf 'host contract: os=%s uname=%s bash=%s make=%s go=%s lint=%s target=%s/%s,cgo=%s source=%s\n' \
    "$expected_outer_os" \
    "$uname_system" \
    "$bash_path" \
    "$make_version_first_line" \
    "go${go_module_version}" \
    "$expected_linter_version" \
    "$target_goos" \
    "$target_goarch" \
    "$target_cgo_enabled" \
    "$target_source"

if [[ "$probe_only" -eq 1 ]]; then
    printf 'pr-lint host capability probe passed\n'
    exit 0
fi

child_environment=(
    "PATH=$curated_path"
    "HOME=${HOME:-}"
    "GOENV=off"
    "GOWORK=off"
    "GOFLAGS=-mod=readonly"
    "GOOS=$target_goos"
    "GOARCH=$target_goarch"
    "CGO_ENABLED=$target_cgo_enabled"
    "CC=$cc_path"
    "CXX=$cxx_path"
    "GIT_CONFIG_NOSYSTEM=1"
    "GIT_CONFIG_GLOBAL=/dev/null"
    "GIT_TERMINAL_PROMPT=0"
    "BASH_ENV="
    "ENV="
    "BEADS_CI_TOOLCHAIN_BOUND=1"
    "BEADS_CI_EXPECTED_OUTER_OS=$expected_outer_os"
    "BEADS_CI_BASH=$bash_path"
    "BEADS_CI_CAT=$cat_path"
    "BEADS_CI_CHMOD=$chmod_path"
    "BEADS_CI_CP=$cp_path"
    "BEADS_CI_DIFF=$diff_path"
    "BEADS_CI_ENV=$env_path"
    "BEADS_CI_GIT=$git_path"
    "BEADS_CI_GO=$go_path"
    "BEADS_CI_GOFMT=$gofmt_path"
    "BEADS_CI_GOLANGCI_LINT=$linter_path"
    "BEADS_CI_GOLANGCI_CONFIG=$linter_config_path"
    "BEADS_CI_MAKE=$make_path"
    "BEADS_CI_MKDIR=$mkdir_path"
    "BEADS_CI_MKTEMP=$mktemp_path"
    "BEADS_CI_READLINK=$readlink_path"
    "BEADS_CI_RM=$rm_path"
    "BEADS_CI_SED=$sed_path"
    "BEADS_CI_UNAME=$uname_path"
    "BEADS_CI_EXPECTED_GOLANGCI_LINT_VERSION=$expected_linter_version"
    "BEADS_CI_TARGET_GOOS=$target_goos"
    "BEADS_CI_TARGET_GOARCH=$target_goarch"
    "BEADS_CI_TARGET_CGO_ENABLED=$target_cgo_enabled"
    "BEADS_CI_FIXTURE_TEMP_PARENT=$fixture_temp_parent"
)
if [[ -n "${BEADS_CI_EXPECTED_CC_VERSION:-}" ]]; then
    child_environment+=(
        "BEADS_CI_EXPECTED_CC_VERSION=$BEADS_CI_EXPECTED_CC_VERSION"
    )
fi
if [[ "$expected_outer_os" == "windows" ]]; then
    child_environment+=(
        "BEADS_CI_CYGPATH=$cygpath_path"
        "BEADS_CI_WINDOWS_CMD=$windows_cmd_path"
        "BEADS_CI_WINDOWS_GIT=$windows_git_path"
        "COMSPEC=$windows_cmd_path"
    )
fi
for name in \
    APPDATA \
    CI \
    GITHUB_ACTIONS \
    GITHUB_JOB \
    GITHUB_RUN_ATTEMPT \
    GITHUB_RUN_ID \
    GITHUB_STEP_SUMMARY \
    LOCALAPPDATA \
    PROGRAMDATA \
    RUNNER_TEMP \
    SYSTEMROOT \
    SystemRoot \
    TEMP \
    TMP \
    TMPDIR \
    USER \
    USERPROFILE \
    WINDIR; do
    if [[ -n "${!name+x}" ]]; then
        child_environment+=("$name=${!name}")
    fi
done

make_contract=(
    --no-print-directory
    -f
    Makefile
    "GOOS=$target_goos"
    "GOARCH=$target_goarch"
    "CGO_ENABLED=$target_cgo_enabled"
    "CI_BASH=$bash_path"
    "CI_GOFMT=$gofmt_path"
    "CI_SED=$sed_path"
    "GO_VERSION=$go_module_version"
)
if [[ "$expected_outer_os" == "windows" ]]; then
    make_contract+=(
        "WINDOWS_CMD_EXE=$windows_cmd_path"
        "GIT_WINDOWS_EXE=$windows_git_path"
        "CI_GIT=$windows_git_path"
    )
else
    make_contract+=(
        "CI_GIT=$git_path"
    )
fi
make_contract+=(ci-pr-lint-bound)

"$env_path" -i \
    "${child_environment[@]}" \
    "$make_path" "${make_contract[@]}"
