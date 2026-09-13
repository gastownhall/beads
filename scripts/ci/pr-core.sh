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

# TIMEOUT is go test's PER-PACKAGE deadline — a hang backstop, not a
# performance budget: it costs nothing while packages pass. Without it this
# wrapper rode go test's 10m default, and cmd/bd has outgrown that even under
# -short: passing runs measure 414-441s, so a slow runner crosses 600s and
# panics with `test timed out` while naming whichever test was merely in
# flight and zero `--- FAIL` lines — a package-wide crawl, not a hang. Use the
# same 25m the repo already applies to this package's deadline in
# scripts/test.sh and main.yml's test matrix (wy-5b5fbl) rather than adding a
# third number to keep in step. Raise via TEST_TIMEOUT.
TIMEOUT="${TEST_TIMEOUT:-25m}"
GO_TEST_PKG_PARALLEL="${GO_TEST_PKG_PARALLEL:-4}"
GO_TEST_PARALLEL="${GO_TEST_PARALLEL:-4}"

ci_time "pr-core go test" -- \
    go test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" -timeout "$TIMEOUT" -race -short -skip '^TestEmbedded' ./...
