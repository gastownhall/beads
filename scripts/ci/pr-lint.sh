#!/usr/bin/env bash
# Required PR formatting and Go lint contract.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"

cd "$REPO_ROOT"

ci_time "gofmt check" -- ./scripts/ci/fmt-check.sh

# The checkout-owned Go driver is the single authority for native and
# Windows/non-CGO lint arguments. Keep this wrapper as the supported direct
# Bash/Make entrypoint and aggregate timing boundary.
ci_time "golangci-lint (native + windows/non-CGO)" -- \
    go run -mod=readonly -tags=gms_pure_go ./scripts/pr-lint
