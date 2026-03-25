#!/usr/bin/env bash
# embedded-test-shard.sh — run a shard of embedded dolt cmd/bd tests.
#
# Usage: embedded-test-shard.sh <shard_index> <total_shards> [extra go test flags...]
#
# Discovers all TestEmbedded* top-level functions from cmd/bd/*_embedded_test.go,
# assigns each to a shard via hash(name) % total, and runs the matching subset.
#
# Environment:
#   BEADS_TEST_EMBEDDED_DOLT=1    required (tests skip without it)
#   BEADS_TEST_BD_BINARY=<path>   optional pre-built bd binary

set -euo pipefail

SHARD_INDEX="${1:?usage: $0 <shard_index> <total_shards>}"
TOTAL_SHARDS="${2:?usage: $0 <shard_index> <total_shards>}"
shift 2

# Discover all top-level TestEmbedded* functions.
ALL_TESTS=$(grep -rh '^func TestEmbedded' cmd/bd/*_embedded_test.go \
  | sed 's/func \(TestEmbedded[A-Za-z0-9_]*\).*/\1/' \
  | sort -u)

if [ -z "$ALL_TESTS" ]; then
  echo "No TestEmbedded* functions found" >&2
  exit 1
fi

# Assign tests to this shard via hash(name) % total.
SHARD_TESTS=()
while IFS= read -r name; do
  # Use cksum for a portable numeric hash.
  hash=$(echo -n "$name" | cksum | awk '{print $1}')
  if (( hash % TOTAL_SHARDS == SHARD_INDEX )); then
    SHARD_TESTS+=("$name")
  fi
done <<< "$ALL_TESTS"

if [ ${#SHARD_TESTS[@]} -eq 0 ]; then
  echo "Shard ${SHARD_INDEX}/${TOTAL_SHARDS}: no tests assigned (all hashed to other shards)"
  exit 0
fi

# Build the -run regex: "^(TestA|TestB|TestC)$"
RUN_REGEX="^($(IFS='|'; echo "${SHARD_TESTS[*]}"))$"

echo "Shard ${SHARD_INDEX}/${TOTAL_SHARDS}: running ${#SHARD_TESTS[@]} test(s)"
printf "  %s\n" "${SHARD_TESTS[@]}"
echo ""

exec go test -tags embeddeddolt -v -race -count=1 -timeout 20m \
  -run "$RUN_REGEX" \
  "$@" \
  ./cmd/bd/
