#!/usr/bin/env bash
# hook-timeout-backends: run the tracked git hook shims against REAL timeout
# implementations (formerly checks.nix's flake check of the same name).
#
# cmd/bd/hooks_timeout_process_test.go pins the shim's selection logic with
# scripted helpers, and TestTrackedManagedHookSectionsMatchGenerator holds the
# tracked .githooks/* byte-equal to the generator. This test closes the
# remaining gap — what the helpers on real hosts actually do — by running the
# tracked sections against the binaries themselves. That is where the
# surprises live: uutils prints a different banner (GH#5541); busybox exits
# 143 on expiry, not 124, so allowlisting it as-is would turn every timeout
# into a failed hook; toybox exits 125 from --version.
#
# Each case: one hook, one shell (the `sh`s a `#!/usr/bin/env sh` hook meets in
# the wild: dash, bash, busybox ash), a PATH of exactly one timeout
# implementation — under the name `timeout`, or for the multicall ones also
# under `gtimeout` alone — plus optionally perl, BEADS_HOOK_TIMEOUT=1, and a
# fake `bd` that blocks on a held-open fifo until a helper kills it. The fake
# records the signal that killed it, which is how the test asserts the backend
# and not just "it finished":
#   coreutils backend → TERM from timeout, shim reports the deadline, exit 0
#   perl backend      → ALRM from perl's alarm, same report, exit 0
#   no backend        → the fake returns at once; shim warns it is running
#                       without a deadline, exit 0
# A non-zero hook exit blocks the user's push, so every case asserts exit 0.
# No clock is involved: if nothing kills the fake, the harness's own
# `timeout 10` (GNU, on its full PATH, not the shim's) fails the case.
#
# The multicall implementations dispatch on argv[0] and print the CANONICAL
# utility name: uutils matches `argv[0].ends_with(<util>)`, so under
# `gtimeout` it is `timeout` to it and reports "timeout (uutils coreutils)
# <version>" — accepted; busybox and toybox know no `gtimeout` applet and fail
# the probe — rejected either way. GNU has no gtimeout variant: Homebrew's
# gtimeout is a separate binary whose banner is the fixed "timeout (GNU
# coreutils) <version>", so GNU is immune by construction.
#
# Usage: hook_timeout_backends_test.sh BUSYBOX TOYBOX UUTILS_COREUTILS HOOK...
# (-test.* arguments, which Bazel lane configs append, are ignored)
# (the static binaries come in hermetically, MODULE.bazel; dash, bash, perl and
# GNU coreutils come from the executor, and a missing one fails the test).
set -euo pipefail

# Lane configs pass Go test flags to every target (--config=prcore's
# --test_arg=-test.short, -test.parallel, ...); they mean nothing here.
args=()
for arg in "$@"; do
    [[ "$arg" == -test.* ]] || args+=("$arg")
done
set -- "${args[@]}"

[[ $# -ge 4 ]] || { echo "usage: $0 BUSYBOX TOYBOX UUTILS HOOK..." >&2; exit 2; }
busybox="$(realpath "$1")"
toybox="$(realpath "$2")"
uutils="$(realpath "$3")"
shift 3
hook_files=("$@")

# The identity allowlist in cmd/bd/hooks.go, as data.
allowlisted=" gnu-coreutils uutils-coreutils "

require_tool() {
    local path
    path="$(command -v "$1" 2>/dev/null)" || { echo "FAIL: $1 is not on the executor's PATH" >&2; exit 1; }
    realpath "$path"
}
dash="$(require_tool dash)"
bash_bin="$(require_tool bash)"
perl="$(require_tool perl)"
gnu_timeout="$(require_tool timeout)"
case "$("$gnu_timeout" --version 2>/dev/null | head -n 1)" in
    "timeout (GNU coreutils) "*) ;;
    *) echo "FAIL: the executor's timeout is not GNU coreutils: $gnu_timeout" >&2; exit 1 ;;
esac
case "$("$uutils" timeout --version 2>/dev/null | head -n 1)" in
    "timeout (uutils coreutils) "*) ;;
    *) echo "FAIL: $uutils is not uutils coreutils" >&2; exit 1 ;;
esac

tmp="${TEST_TMPDIR:-$(mktemp -d)}/htb"
rm -rf "$tmp"
mkdir -p "$tmp"

# 1. Shims under test: the managed section of each tracked hook, as bd
#    installs it into a fresh repo (shebang + section).
shims="$tmp/shims"
mkdir -p "$shims"
hooks=()
for file in "${hook_files[@]}"; do
    hook="$(basename "$file")"
    hooks+=("$hook")
    {
        echo '#!/usr/bin/env sh'
        sed -n '/^# --- BEGIN BEADS INTEGRATION/,/^# --- END BEADS INTEGRATION/p' "$file"
    } > "$shims/$hook"
    grep -q 'bd hooks run' "$shims/$hook" || { echo "FAIL: no managed section in .githooks/$hook" >&2; exit 1; }
done
for want in pre-commit post-merge pre-push post-checkout prepare-commit-msg; do
    [[ -f "$shims/$want" ]] || { echo "FAIL: .githooks/$want is not in the test's data" >&2; exit 1; }
done

# 2. The fake bd the shim will find on its restricted PATH. It blocks until
#    killed and records the signal, re-raising it so it dies the way a real bd
#    would (143 / 142), not by a made-up exit code.
fake="$tmp/fakebin"
mkdir -p "$fake"
cat > "$fake/bd" <<'FAKE'
#!/bin/sh
trap 'echo TERM > "$FAKE_BD_SIGNAL"; trap - TERM; kill -TERM $$' TERM
trap 'echo ALRM > "$FAKE_BD_SIGNAL"; trap - ALRM; kill -ALRM $$' ALRM
[ -n "$FAKE_BD_BLOCK" ] && read -r _
exit 0
FAKE
chmod +x "$fake/bd"
mkfifo "$tmp/hang"
exec 3<> "$tmp/hang"

# 3. The installs: every implementation as itself, and the multicalls once
#    more under the other candidate name. "impl" is what the model reasons
#    about; "label" is what the case is called. A dir holds exactly the one
#    command.
bin_dir() { # bin_dir NAME CMD TARGET: a dir holding CMD -> TARGET
    mkdir -p "$tmp/bins/$1"
    ln -s "$3" "$tmp/bins/$1/$2"
    echo "$tmp/bins/$1"
}
install_impls=() install_labels=() install_dirs=()
add_install() { install_impls+=("$1"); install_labels+=("$2"); install_dirs+=("$3"); }
add_install gnu-coreutils gnu-coreutils "$(bin_dir gnu-coreutils timeout "$gnu_timeout")"
add_install none none ""
for multicall in uutils-coreutils:"$uutils" busybox:"$busybox" toybox:"$toybox"; do
    impl="${multicall%%:*}" target="${multicall#*:}"
    add_install "$impl" "$impl" "$(bin_dir "$impl" timeout "$target")"
    add_install "$impl" "$impl-as-gtimeout" "$(bin_dir "$impl-as-gtimeout" gtimeout "$target")"
done
perl_dir="$(bin_dir perl perl "$perl")"

shell_names=(dash bash busybox-ash)
shell_paths=("$dash" "$bash_bin" "$busybox")

backend_for() { # backend_for IMPL WITH_PERL
    if [[ "$allowlisted" == *" $1 "* ]]; then
        echo coreutils
    elif [[ "$2" == 1 ]]; then
        echo perl
    else
        echo none
    fi
}

# 4. The matrix.
results="$tmp/results"
mkdir -p "$results"
run_case() {
    local name=$1 hook=$2 backend=$3 shell=$4
    shift 4
    local case_dir="$tmp/cases/$name"
    mkdir -p "$case_dir/bin"
    ln -s "$shell" "$case_dir/bin/sh"
    local path="$fake:$case_dir/bin" dir
    for dir in "$@"; do path="$path:$dir"; done
    local block=1
    [[ "$backend" == none ]] && block=
    local signal_file="$case_dir/signal" rc=0 case_out signal verdict=ok
    case_out=$("$gnu_timeout" 10 env PATH="$path" BEADS_HOOK_TIMEOUT=1 \
        FAKE_BD_BLOCK="$block" FAKE_BD_SIGNAL="$signal_file" \
        "$case_dir/bin/sh" "$shims/$hook" origin https://example.invalid 2>&1 <&3) || rc=$?
    signal=$(cat "$signal_file" 2>/dev/null || true)
    if [[ "$rc" -eq 124 ]]; then
        verdict="FAIL: nothing killed the fake bd (harness safety net fired), signal='$signal': $case_out"
    elif [[ "$rc" -ne 0 ]]; then
        verdict="FAIL: hook exit $rc (would block the push): $case_out"
    else
        case "$backend" in
            coreutils)
                [[ "$case_out" == *"timed out after 1s"* ]] || verdict="FAIL: no deadline report: $case_out"
                [[ "$signal" == TERM ]] || verdict="FAIL: expected coreutils timeout (TERM), fake saw '$signal'"
                ;;
            perl)
                [[ "$case_out" == *"timed out after 1s"* ]] || verdict="FAIL: no deadline report: $case_out"
                [[ "$signal" == ALRM ]] || verdict="FAIL: expected perl alarm (ALRM), fake saw '$signal'"
                ;;
            none)
                [[ "$case_out" == *"running without timeout"* ]] || verdict="FAIL: no unbounded warning: $case_out"
                ;;
        esac
    fi
    printf '%-52s %-10s %s\n' "$name" "$backend" "$verdict" > "$results/${name//\//_}"
}

total=0
for i in "${!install_impls[@]}"; do
    for with_perl in 0 1; do
        backend="$(backend_for "${install_impls[$i]}" "$with_perl")"
        dirs=()
        [[ -n "${install_dirs[$i]}" ]] && dirs+=("${install_dirs[$i]}")
        suffix=
        if [[ "$with_perl" == 1 ]]; then
            dirs+=("$perl_dir")
            suffix=+perl
        fi
        for s in "${!shell_names[@]}"; do
            for hook in "${hooks[@]}"; do
                run_case "$hook/${shell_names[$s]}/${install_labels[$i]}$suffix" "$hook" "$backend" "${shell_paths[$s]}" ${dirs[@]+"${dirs[@]}"} &
                total=$((total + 1))
            done
        done
    done
done
wait

printf '%-52s %-10s %s\n' CASE BACKEND RESULT
sort "$results"/*
echo
reported=$(find "$results" -type f | wc -l)
failed=$(grep -l FAIL "$results"/* | wc -l || true)
echo "$reported of $total cases reported, $failed failed"
if [[ "$reported" -ne "$total" ]]; then
    echo "FAIL: some cases never reported a verdict" >&2
    exit 1
fi
if [[ "$failed" -ne 0 ]]; then
    exit 1
fi
