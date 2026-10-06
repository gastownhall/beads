#!/usr/bin/env bash
# Run a standalone `go test -c` binary (typically cross-compiled on a
# different host, e.g. scripts/ci/build-windows-test-binaries.sh on Linux)
# so that it behaves like `go test` would have, from the caller's point of
# view: same cwd convention, same flag names, same BEADS_TEST_REPO_ROOT
# resolution.
#
# Usage: run-go-test-binary.sh BIN PKGDIR [go-test-style flags] [-- custom args]
#
#   BIN     - path to the compiled test binary (go test -c output)
#   PKGDIR  - the package directory the binary was built from. `go test`
#             always runs with the package directory as its working
#             directory, and some tests (relative testdata paths, etc.) rely
#             on that; a prebuilt binary run from an arbitrary cwd would
#             silently behave differently.
#
# Recognized go-test-style flags, mapped to the binary's -test.* equivalents:
#   -run PATTERN        -> -test.run PATTERN
#   -skip PATTERN        -> -test.skip PATTERN
#   -count N              -> -test.count N
#   -v                      -> -test.v
#   -timeout DURATION  -> -test.timeout DURATION
#   -parallel N          -> -test.parallel N
#
# `go test` also injects two flags `cmd/go` adds to every test binary
# invocation, which a bare binary never gets on its own: a default
# `-test.timeout=10m0s` when the caller doesn't pass `-timeout`, and an
# unconditional `-test.paniconexit0` (since Go 1.14, so a panic inside the
# test binary still exits non-zero through the normal runtime panic path).
# Both are added below so the prebuilt path can't silently run with no
# deadline or swallow a panic's exit code relative to the native job it
# twins.
#
# Anything after a literal `--` is passed through to the binary unchanged
# (e.g. -required-suite=doc-freshness, -required-host, -expected-goos
# windows): those are flags the test binary itself defines with the `flag`
# package, not `go test` flags.
#
# BEADS_TEST_REPO_ROOT is exported (to GITHUB_WORKSPACE, or the caller's
# override if already set) because scripts tests locate the repository
# through runtime.Caller, which reports the build host's path in a
# cross-built binary. internal/testutil/bazeltest honors
# BEADS_TEST_REPO_ROOT when it names a directory holding go.mod -- see that
# package's doc comment for the full resolution order.
#
# This re-export is not fighting scripts/ci/lib/test-env.sh's
# beads_test_env_enter, which unsets any BEADS_TEST_REPO_ROOT it finds
# already set (so a leaked dev-shell override can't point a CI run's
# source-scan guards at the wrong checkout): the two never race. When this
# script is chained after test.sh's BEADS_TEST_PREBUILT_TEST_BINARY
# short-circuit, beads_test_env_enter's unset has already run by the time
# control reaches here, so the line below's `${BEADS_TEST_REPO_ROOT:-...}`
# always falls through to GITHUB_WORKSPACE -- exactly the value a CI runner
# needs. When this script instead runs standalone (the "-prebuilt" jobs'
# direct `run-go-test-binary.sh ...` steps, with no test.sh and no
# beads_test_env_enter in between), there is nothing to have unset it, and
# the same GITHUB_WORKSPACE fallback applies for the same reason.
set -euo pipefail

if [[ $# -lt 2 ]]; then
    echo "usage: $0 BIN PKGDIR [go-test-style flags] [-- custom args]" >&2
    exit 2
fi

BIN="$1"
PKGDIR="$2"
shift 2

if [[ ! -x "$BIN" ]]; then
    echo "test binary not found or not executable: $BIN" >&2
    exit 1
fi
if [[ ! -d "$PKGDIR" ]]; then
    echo "package directory not found: $PKGDIR" >&2
    exit 1
fi

# Resolve BIN and PKGDIR to absolute paths before we `cd`, since relative
# paths from the caller's cwd would otherwise stop resolving once we change
# directory.
BIN="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")"
PKGDIR_ABS="$(cd "$PKGDIR" && pwd)"

TEST_ARGS=()
PASSTHROUGH=()
seen_dashdash=0
timeout_given=0
while [[ $# -gt 0 ]]; do
    if [[ "$seen_dashdash" == "1" ]]; then
        PASSTHROUGH+=("$1")
        shift
        continue
    fi
    case "$1" in
        --)
            seen_dashdash=1
            shift
            ;;
        -run)
            TEST_ARGS+=(-test.run "$2")
            shift 2
            ;;
        -skip)
            TEST_ARGS+=(-test.skip "$2")
            shift 2
            ;;
        -count)
            TEST_ARGS+=(-test.count "$2")
            shift 2
            ;;
        -count=*)
            TEST_ARGS+=(-test.count "${1#-count=}")
            shift
            ;;
        -timeout)
            TEST_ARGS+=(-test.timeout "$2")
            timeout_given=1
            shift 2
            ;;
        -parallel)
            TEST_ARGS+=(-test.parallel "$2")
            shift 2
            ;;
        -v)
            TEST_ARGS+=(-test.v)
            shift
            ;;
        *)
            echo "unrecognized flag before '--': $1 (pass custom test flags after a literal --)" >&2
            exit 2
            ;;
    esac
done

# Match cmd/go's own defaults (see the comment above the flag table): a
# bare test binary has no deadline and doesn't force a non-zero exit code on
# panic unless told to.
if [[ "$timeout_given" == "0" ]]; then
    TEST_ARGS+=(-test.timeout 10m)
fi
TEST_ARGS+=(-test.paniconexit0)

export BEADS_TEST_REPO_ROOT="${BEADS_TEST_REPO_ROOT:-${GITHUB_WORKSPACE:-}}"
if [[ -z "$BEADS_TEST_REPO_ROOT" ]]; then
    echo "BEADS_TEST_REPO_ROOT could not be resolved: neither it nor GITHUB_WORKSPACE is set" >&2
    exit 1
fi

# PASSTHROUGH is usually empty. Expand it with the ${arr[@]+...} guard: under
# `set -u`, bash < 4.4 (macOS's /bin/bash is 3.2) treats a bare expansion of
# an empty array as an unbound variable and aborts before the exec.
echo "Running: $BIN (cwd=$PKGDIR_ABS) ${TEST_ARGS[*]} ${PASSTHROUGH[*]+${PASSTHROUGH[*]}}" >&2
cd "$PKGDIR_ABS"
exec "$BIN" "${TEST_ARGS[@]}" ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}
