#!/usr/bin/env bash
# The vet checks `go test` runs on every package it tests (cmd/go's
# defaultVetFlags), over the whole module. rules_go's go_test runs none, so
# where PR Core's `go test ./...` stands down for the Bazel lane (D2 step 3)
# this is what keeps them pre-merge. pr.yml runs it on every PR
# (scripts-go-checks). scripts/pr_lanes_bazel_coverage_test.go keeps the list
# equal to the Go toolchain's.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"
# shellcheck source=lib/timing.sh
source "$REPO_ROOT/scripts/ci/lib/timing.sh"

cd "$REPO_ROOT"

GO_TEST_VET_FLAGS=(-atomic -bool -buildtags -directive -errorsas -ifaceassert -nilfunc -printf -slog -stringintconv -tests)

ci_time "go vet (go test's checks)" -- \
    go vet -tags gms_pure_go "${GO_TEST_VET_FLAGS[@]}" ./...
