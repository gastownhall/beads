#!/usr/bin/env bash
# The Go tests the Bazel PR-core lane does not run or skips
# (tools/bazel/equivalence_allowlist.txt), under `go test` in PR Core's test
# environment. pr.yml runs this on every PR (scripts-go-checks):
# where PR Core stands down for the Bazel lane (D2 step 3) it is their only
# pre-merge run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/test-env.sh
source "$REPO_ROOT/scripts/ci/lib/test-env.sh"

cd "$REPO_ROOT"

beads_test_env_enter

# As in pr.yml's PR Core step.
export BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION=1

python3 tools/bazel/run_allowlisted_go_tests.py "$@"
