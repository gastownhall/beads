#!/usr/bin/env bash
# Test runner that automatically skips known broken tests

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SKIP_FILE="$REPO_ROOT/.test-skip"

# Canonical build flags (GOFLAGS=-tags=gms_pure_go, CGO_ENABLED=1).
# Opt-in ICU-path coverage remains available via scripts/test-icu-path.sh.
# shellcheck source=../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=ci/lib/test-env.sh
source "$REPO_ROOT/scripts/ci/lib/test-env.sh"

beads_test_env_enter

# Build skip pattern from .test-skip file
build_skip_pattern() {
    if [[ ! -f "$SKIP_FILE" ]]; then
        echo ""
        return
    fi

    # Read non-comment, non-empty lines and join with |
    local pattern=$(grep -v '^#' "$SKIP_FILE" | grep -v '^[[:space:]]*$' | paste -sd '|' -)
    echo "$pattern"
}

# Default values.
#
# TIMEOUT is go test's PER-PACKAGE deadline — a hang backstop, not a
# performance budget: it costs nothing while packages pass. The floor is set
# by cmd/bd, the slowest package: measured 2026-07-26 on a busy darwin fleet
# box (wy-4mtr0), its default suite is ~1090s of test time across ~1490 tests
# (mostly serial — subprocess tests that spawn bd + embedded Dolt per
# invocation, largely t.Setenv-bound so they cannot t.Parallel), giving
# ~18min wall WITH the prebuilt-binary fast path below already removing the
# in-test `go build` steps. 3m could never fit that, which made every
# full-suite run FAIL with a package deadline panic naming no failing test.
# 25m holds the measurement plus fleet-load headroom (the passing full-suite
# acceptance run clocked cmd/bd at 870s under -p 4 package concurrency).
# Raise via TEST_TIMEOUT; don't lower it below cmd/bd's measured runtime.
TIMEOUT="${TEST_TIMEOUT:-25m}"
GO_TEST_PKG_PARALLEL="${GO_TEST_PKG_PARALLEL:-4}"
GO_TEST_PARALLEL="${GO_TEST_PARALLEL:-4}"
SKIP_PATTERN=$(build_skip_pattern)
VERBOSE="${TEST_VERBOSE:-}"
RUN_PATTERN="${TEST_RUN:-}"
COVERAGE="${TEST_COVER:-}"
COVERPROFILE="${TEST_COVERPROFILE:-/tmp/beads.coverage.out}"
COVERPKG="${TEST_COVERPKG:-}"

# Parse arguments
PACKAGES=()
while [[ $# -gt 0 ]]; do
    case $1 in
        -v|--verbose)
            VERBOSE="-v"
            shift
            ;;
        -timeout)
            TIMEOUT="$2"
            shift 2
            ;;
        -run)
            RUN_PATTERN="$2"
            shift 2
            ;;
        -skip)
            # Allow additional skip patterns
            if [[ -n "$SKIP_PATTERN" ]]; then
                SKIP_PATTERN="$SKIP_PATTERN|$2"
            else
                SKIP_PATTERN="$2"
            fi
            shift 2
            ;;
        *)
            PACKAGES+=("$1")
            shift
            ;;
    esac
done

# Default to all packages if none specified
if [[ ${#PACKAGES[@]} -eq 0 ]]; then
    PACKAGES=("./...")
fi

# Prebuild bd once for subprocess-style tests (wy-4mtr0). cmd/bd has a dozen
# test helpers that otherwise each `go build` the full bd binary inside the
# test run — on a busy machine those link steps alone can blow the package
# deadline, and one helper used to silently fall back to a stale repo-root
# ./bd. CI already exports BEADS_TEST_BD_BINARY from a prebuilt artifact
# (.github/workflows/main.yml); this gives the local runner the same fast
# path. A caller-supplied BEADS_TEST_BD_BINARY always wins; skipped when the
# requested packages cannot include cmd/bd.
if [[ -z "${BEADS_TEST_BD_BINARY:-}" ]]; then
    case " ${PACKAGES[*]} " in
        # Any recursive pattern (./..., ./cmd/...) can expand to cmd/bd.
        *"..."* | *"cmd/bd"*)
            if [[ -n "${BEADS_TEST_ENV_ROOT:-}" ]]; then
                PREBUILT_BD_DIR="$BEADS_TEST_ENV_ROOT/prebuilt-bd"
            else
                PREBUILT_BD_DIR=$(mktemp -d "${TMPDIR:-/tmp}/beads-prebuilt-bd-XXXXXX")
            fi
            mkdir -p "$PREBUILT_BD_DIR"
            echo "Prebuilding bd for subprocess tests..." >&2
            PREBUILT_BD_BIN="$PREBUILT_BD_DIR/bd$(go env GOEXE)"
            if go build -o "$PREBUILT_BD_BIN" "$REPO_ROOT/cmd/bd"; then
                export BEADS_TEST_BD_BINARY="$PREBUILT_BD_BIN"
                echo "Prebuilt bd: $BEADS_TEST_BD_BINARY" >&2
            else
                echo "WARN: bd prebuild failed; tests will build their own binaries" >&2
            fi
            ;;
    esac
fi

# Optional: start a single shared Dolt test server for all packages.
# When BEADS_TEST_SHARED_SERVER=1, we start one dolt sql-server and export
# BEADS_DOLT_PORT so every test package reuses it instead of spawning its own.
# This reduces 8-16+ concurrent dolt processes down to 1.
if [[ "${BEADS_TEST_SHARED_SERVER:-}" == "1" && -z "${BEADS_DOLT_PORT:-}" ]]; then
    if command -v dolt &>/dev/null; then
        SHARED_DOLT_DIR=$(mktemp -d /tmp/beads-shared-test-dolt-XXXXXX)
        DOLT_ROOT_PATH="$SHARED_DOLT_DIR"
        export DOLT_ROOT_PATH

        dolt config --global --add user.name "beads-test" 2>/dev/null
        dolt config --global --add user.email "test@beads.local" 2>/dev/null

        SHARED_DB_DIR="$SHARED_DOLT_DIR/data"
        mkdir -p "$SHARED_DB_DIR"
        (cd "$SHARED_DB_DIR" && dolt init) >/dev/null 2>&1

        # Find a free port
        SHARED_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')

        dolt sql-server -H 127.0.0.1 -P "$SHARED_PORT" --no-auto-commit \
            --data-dir "$SHARED_DB_DIR" &>/dev/null &
        SHARED_DOLT_PID=$!

        # Wait for server to accept connections (up to 30s)
        for i in $(seq 1 60); do
            if nc -z 127.0.0.1 "$SHARED_PORT" 2>/dev/null; then
                break
            fi
            sleep 0.5
        done

        if nc -z 127.0.0.1 "$SHARED_PORT" 2>/dev/null; then
            export BEADS_DOLT_PORT="$SHARED_PORT"
            export BEADS_TEST_MODE=1
            echo "Shared test Dolt server started on port $SHARED_PORT (PID $SHARED_DOLT_PID)" >&2
            cleanup_shared_server() {
                kill "$SHARED_DOLT_PID" 2>/dev/null || true
                wait "$SHARED_DOLT_PID" 2>/dev/null || true
                rm -rf "$SHARED_DOLT_DIR"
            }
            trap 'cleanup_shared_server; beads_test_env_cleanup' EXIT
        else
            echo "WARN: shared Dolt server failed to start, falling back to per-package servers" >&2
            kill "$SHARED_DOLT_PID" 2>/dev/null || true
            rm -rf "$SHARED_DOLT_DIR"
        fi
    fi
fi

# Build go test command
CMD=(go test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" -timeout "$TIMEOUT")

if [[ -n "$VERBOSE" ]]; then
    CMD+=(-v)
fi

if [[ -n "$SKIP_PATTERN" ]]; then
    CMD+=(-skip "$SKIP_PATTERN")
fi

if [[ -n "$RUN_PATTERN" ]]; then
    CMD+=(-run "$RUN_PATTERN")
fi

if [[ -n "$COVERAGE" ]]; then
    CMD+=(-covermode=atomic -coverprofile "$COVERPROFILE")
    if [[ -n "$COVERPKG" ]]; then
        CMD+=(-coverpkg "$COVERPKG")
    fi
fi

CMD+=("${PACKAGES[@]}")

echo "Running: ${CMD[*]}" >&2
echo "Skipping: $SKIP_PATTERN" >&2
echo "" >&2

"${CMD[@]}"
status=$?

if [[ -n "$COVERAGE" ]]; then
    total=$(go tool cover -func="$COVERPROFILE" | awk '/^total:/ {print $NF}')
    echo "Total coverage: ${total} (profile: ${COVERPROFILE})" >&2
fi

exit $status
