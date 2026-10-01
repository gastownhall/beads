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

# The checkout-owned Go driver is the single authority for the native and
# cross-target lint arguments. Files guarded by //go:build windows && !cgo or
# //go:build darwin are invisible to the Linux runner, so the driver cross-lints
# those non-CGO targets from the same runner. Keep this wrapper as the supported
# direct Bash/Make entrypoint and aggregate timing boundary.
ci_time "golangci-lint (native + windows/darwin non-CGO)" -- \
    go run -mod=readonly -tags=gms_pure_go ./scripts/pr-lint
