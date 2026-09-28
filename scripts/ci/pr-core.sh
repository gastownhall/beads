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

# Without an explicit -timeout every package gets Go's 10m default, and ./cmd/bd
# has outgrown it: measured through this wrapper it takes 572s, which leaves 28s
# — under 5% — of margin. Runner variance eats that, and the package then dies on
# the alarm at 600.3s/600.7s (two consecutive runs on #6090; #6038 squeaked in
# just under). No single test is at fault, so the panic names whichever one was
# in flight and reads like a hang when it is really the whole package running out
# of clock. 30m is what every other -race lane here already gives this same code:
# main.yml's integration package and cmd/bd shards, ci-measurements.yml,
# nightly.yml. Splitting or de-slowing cmd/bd is the real fix and belongs on main
# — this only stops the clock from being the thing that fails.
ci_time "pr-core go test" -- \
    go test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" -race -short -timeout=30m -skip '^TestEmbedded' ./...
