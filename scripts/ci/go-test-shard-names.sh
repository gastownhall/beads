#!/usr/bin/env bash
# Print top-level Go test names assigned to a stable test-name shard.
#
# Usage:
#   go-test-shard-names.sh <shard_number> <total_shards> <go package>
#
# Environment:
#   GO_TEST_SHARD_TAGS                build tags for go test -list, default: BEADS_BUILD_TAGS
#   GO_TEST_SHARD_EXCLUDE_TEST_REGEX  optional regex for test names to exclude

set -euo pipefail

SHARD_NUMBER="${1:?usage: $0 <shard_number> <total_shards> <go package>}"
TOTAL_SHARDS="${2:?usage: $0 <shard_number> <total_shards> <go package>}"
PACKAGE="${3:?usage: $0 <shard_number> <total_shards> <go package>}"

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

SHARD_INDEX=$((SHARD_NUMBER - 1))
GO_TEST_SHARD_TAGS="${GO_TEST_SHARD_TAGS:-$BEADS_BUILD_TAGS}"
GO_TEST_SHARD_EXCLUDE_TEST_REGEX="${GO_TEST_SHARD_EXCLUDE_TEST_REGEX:-}"

mapfile -t ALL_TESTS < <(
    GO_TEST_SHARD_TAGS="$GO_TEST_SHARD_TAGS" go run -tags=ci_tools ./scripts/ci/go-list-test-names "$PACKAGE" \
        | while IFS= read -r test_name; do
            if [[ -n "$GO_TEST_SHARD_EXCLUDE_TEST_REGEX" && "$test_name" =~ $GO_TEST_SHARD_EXCLUDE_TEST_REGEX ]]; then
                continue
            fi
            printf '%s\n' "$test_name"
        done \
        | sort -u
)

if ((${#ALL_TESTS[@]} == 0)); then
    echo "No top-level Go tests matched in package: $PACKAGE" >&2
    exit 1
fi

SELECTED_TESTS=()
for test_name in "${ALL_TESTS[@]}"; do
    hash="$(printf '%s' "$test_name" | cksum | cut -d ' ' -f 1)"
    if ((hash % TOTAL_SHARDS == SHARD_INDEX)); then
        SELECTED_TESTS+=("$test_name")
    fi
done

echo "Shard ${SHARD_NUMBER}/${TOTAL_SHARDS}: selected ${#SELECTED_TESTS[@]} of ${#ALL_TESTS[@]} test(s)" >&2
for test_name in "${SELECTED_TESTS[@]}"; do
    printf '%s\n' "$test_name"
done
