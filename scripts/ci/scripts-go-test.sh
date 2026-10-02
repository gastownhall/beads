#!/usr/bin/env bash
# PR Core's `go test`, for the ./scripts/... packages only: the repository
# policy tests (workflow, Bazel and CI guards), several of which check part of
# their rules, or everything, under go test only. pr.yml runs this on every PR
# (scripts-go-checks), so they run before merge also where PR Core stands
# down for the Bazel lane (D2 step 3). Same flags and environment as
# scripts/ci/pr-core.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"
# shellcheck source=lib/test-env.sh
source "$REPO_ROOT/scripts/ci/lib/test-env.sh"

cd "$REPO_ROOT"

beads_test_env_enter

ci_time "scripts go test" -- \
    go test -p 4 -parallel 4 -race -short -timeout=30m -skip '^TestEmbedded' ./scripts/...
