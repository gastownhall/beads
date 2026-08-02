#!/usr/bin/env bash
# Print the Go packages assigned to a stable package-test shard.
#
# Usage:
#   go-test-shard-packages.sh <shard_number> <total_shards> [go list patterns...]
#
# Environment:
#   GO_TEST_SHARD_TAGS           build tags for go list, default: BEADS_BUILD_TAGS
#   GO_TEST_SHARD_EXCLUDE_REGEX  optional regex for package paths to exclude

set -euo pipefail

SHARD_NUMBER="${1:?usage: $0 <shard_number> <total_shards> [go list patterns...]}"
TOTAL_SHARDS="${2:?usage: $0 <shard_number> <total_shards> [go list patterns...]}"
shift 2

if ! [[ "$SHARD_NUMBER" =~ ^[0-9]+$ ]] || ! [[ "$TOTAL_SHARDS" =~ ^[0-9]+$ ]]; then
    echo "Shard number and total shards must be positive integers" >&2
    exit 1
fi
if ((TOTAL_SHARDS < 1 || SHARD_NUMBER < 1 || SHARD_NUMBER > TOTAL_SHARDS)); then
    echo "Invalid shard ${SHARD_NUMBER}/${TOTAL_SHARDS}" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"

cd "$REPO_ROOT"

if (($# == 0)); then
    set -- ./...
fi

SHARD_INDEX=$((SHARD_NUMBER - 1))
GO_TEST_SHARD_TAGS="${GO_TEST_SHARD_TAGS:-$BEADS_BUILD_TAGS}"

GO_TEST_SHARD_EXCLUDE_REGEX="${GO_TEST_SHARD_EXCLUDE_REGEX:-}"

mapfile -t ALL_PACKAGES < <(
    go list -tags="$GO_TEST_SHARD_TAGS" "$@" \
        | while IFS= read -r pkg; do
            if [[ -n "$GO_TEST_SHARD_EXCLUDE_REGEX" && "$pkg" =~ $GO_TEST_SHARD_EXCLUDE_REGEX ]]; then
                continue
            fi
            printf '%s\n' "$pkg"
        done \
        | sort -u
)

if ((${#ALL_PACKAGES[@]} == 0)); then
    echo "No Go packages matched: $*" >&2
    exit 1
fi

SELECTED_PACKAGES=()
for pkg in "${ALL_PACKAGES[@]}"; do
    hash="$(printf '%s' "$pkg" | cksum | cut -d ' ' -f 1)"
    if ((hash % TOTAL_SHARDS == SHARD_INDEX)); then
        SELECTED_PACKAGES+=("$pkg")
    fi
done

echo "Shard ${SHARD_NUMBER}/${TOTAL_SHARDS}: selected ${#SELECTED_PACKAGES[@]} of ${#ALL_PACKAGES[@]} package(s)" >&2
for pkg in "${SELECTED_PACKAGES[@]}"; do
    printf '%s\n' "$pkg"
done
