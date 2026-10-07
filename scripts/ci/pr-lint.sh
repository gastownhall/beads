#!/usr/bin/env bash
# Required PR lint contract: nogo (//tools/nogo: go test's vet checks plus
# the golangci-lint linters .golangci.yml enables) under Bazel, natively and
# cross-configured for windows/amd64 and darwin/arm64. BD_LINT_TARGETS selects
# passes (default: native,windows,darwin). gofmt is a Bazel test
# (//scripts/repochecks:fmt_test).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"

cd "$REPO_ROOT"

# The checkout-owned Go driver is the single authority for the passes and
# their Bazel arguments. Keep this wrapper as the supported direct Bash/Make
# entrypoint and aggregate timing boundary.
ci_time "nogo (native + windows/darwin non-cgo)" -- \
    go run -mod=readonly -tags=gms_pure_go ./scripts/pr-lint
