#!/usr/bin/env bash
# Required fast PR Go test contract.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"
# shellcheck source=lib/test-env.sh
source "$REPO_ROOT/scripts/ci/lib/test-env.sh"

cd "$REPO_ROOT"

beads_test_env_enter

GO_TEST_PKG_PARALLEL="${GO_TEST_PKG_PARALLEL:-4}"
GO_TEST_PARALLEL="${GO_TEST_PARALLEL:-4}"

# -race requires cgo. .buildflags defaults CGO_ENABLED=0 for the fast no-CGO
# graph; this wrapper is the race contract and must enable cgo. Subprocess
# tests honor BEADS_TEST_BD_BINARY, which CI otherwise points at the pure-Go
# artifact that cannot open embedded Dolt.
export CGO_ENABLED=1
cgo_bd="${TMPDIR:-/tmp}/bd-pr-core-cgo"
ci_time "pr-core go build cgo bd" -- \
    go build -tags gms_pure_go -o "$cgo_bd" ./cmd/bd
export BEADS_TEST_BD_BINARY="$cgo_bd"

ci_time "pr-core go test" -- \
    go test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" -race -short -skip '^TestEmbedded' ./...
