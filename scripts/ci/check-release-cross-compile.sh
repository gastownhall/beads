#!/usr/bin/env bash
# Build the whole tree (not just ./cmd/bd) for every release target in the
# given group of scripts/ci/release-targets.txt, with CGO disabled.
#
# This is the F7a fold of pr.yml's check-release-target-cross-compilation job:
# one call per matrix leg (group), each building its targets sequentially in a
# single job/runner instead of one runner per target. Every target in the
# group runs even if an earlier one fails, so a PR touching two platforms at
# once sees both failures in one log instead of needing a re-run per leg.
#
# Usage: check-release-cross-compile.sh <group>
#
# Review N-1 (2026-10-03): the spec sketched a 2-wide parallel build per leg
# with a per-target GOCACHE. This builds sequentially with one shared GOCACHE
# instead - simpler, and safe because the targets in a group never touch the
# same GOCACHE entries destructively. Measured per-target time on an 8 vCPU
# Blacksmith runner is a couple of minutes, off the gate's critical path; if a
# future group's wall time regresses past ~4 minutes, revisit 2-wide.
#
# Coverage boundary, carried over from the pre-fold job: every target here
# builds CGO_ENABLED=0, but .goreleaser.yml builds bd-linux-amd64,
# bd-linux-arm64 and bd-windows-amd64 with CGO_ENABLED=1, and the darwin pair
# is built natively at CGO_ENABLED=1 by release.yml's goreleaser-macos job.
# Five of these eight targets are therefore a weaker proxy than the binary
# they stand for: the cgo half of the tree (beads_cgo.go ->
# internal/storage/embeddeddolt) is never compiled here, so a break confined
# to it still passes this check. Only android/arm64, windows/arm64 and
# freebsd/amd64 replicate their release configuration exactly.
set -euo pipefail

group="${1:?usage: check-release-cross-compile.sh <group>}"
manifest="$(dirname "${BASH_SOURCE[0]}")/release-targets.txt"

targets=()
while read -r goos goarch row_group; do
    [[ "$row_group" == "$group" ]] || continue
    targets+=("$goos/$goarch")
done < <(grep -v '^#' "$manifest" | grep -v '^[[:space:]]*$')

if [[ "${#targets[@]}" -eq 0 ]]; then
    echo "::error::no release targets found for group '$group' in $manifest"
    exit 1
fi

failed=()
for target in "${targets[@]}"; do
    goos="${target%/*}"
    goarch="${target#*/}"
    echo "::group::Build $goos/$goarch"
    if CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -tags gms_pure_go ./...; then
        echo "ok: $goos/$goarch"
    else
        echo "::error::cross-compilation failed for $goos/$goarch"
        failed+=("$goos/$goarch")
    fi
    echo "::endgroup::"
done

if [[ "${#failed[@]}" -gt 0 ]]; then
    echo "::error::cross-compilation failed for: ${failed[*]}"
    exit 1
fi

echo "cross-compilation passed for group '$group': ${targets[*]}"
