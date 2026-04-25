#!/usr/bin/env bash
# embedded-test-shard.sh — run a shard of embedded dolt cmd/bd tests.
#
# Usage: embedded-test-shard.sh <shard_number> <total_shards>
#
# Discovers all TestEmbedded* top-level functions from cmd/bd/*_embedded_test.go.
# Tests listed in embedded-cmd-test-shards.txt for the requested shard count use
# that committed assignment; newly-added tests fall back to hash(name) % total.
# Runs the matching subset using the pre-built test binary at
# BEADS_TEST_CMD_BINARY (or /tmp/bd-cmd-test).
#
# Environment:
#   BEADS_TEST_EMBEDDED_DOLT=1    required (tests skip without it)
#   BEADS_TEST_BD_BINARY=<path>   optional pre-built bd binary (used by tests)
#   BEADS_TEST_CMD_BINARY=<path>  pre-built cmd/bd test binary (default: /tmp/bd-cmd-test)
#   BEADS_TEST_SHARD_LIST_ONLY=1  print selected tests without running them

set -euo pipefail

SHARD_NUMBER="${1:?usage: $0 <shard_number> <total_shards>}"
TOTAL_SHARDS="${2:?usage: $0 <shard_number> <total_shards>}"
shift 2

if ! [[ "$SHARD_NUMBER" =~ ^[0-9]+$ ]] || ! [[ "$TOTAL_SHARDS" =~ ^[0-9]+$ ]]; then
  echo "Shard number and total shards must be positive integers" >&2
  exit 1
fi
if (( TOTAL_SHARDS < 1 || SHARD_NUMBER < 1 || SHARD_NUMBER > TOTAL_SHARDS )); then
  echo "Invalid shard ${SHARD_NUMBER}/${TOTAL_SHARDS}" >&2
  exit 1
fi

SHARD_INDEX=$(( SHARD_NUMBER - 1 ))
MANIFEST="${BEADS_TEST_SHARD_MANIFEST:-.github/scripts/embedded-cmd-test-shards.txt}"

# Discover all top-level TestEmbedded* functions.
ALL_TESTS=$(grep -rh '^func TestEmbedded' cmd/bd/*_embedded_test.go \
  | sed 's/func \(TestEmbedded[A-Za-z0-9_]*\).*/\1/' \
  | sort -u)

if [ -z "$ALL_TESTS" ]; then
  echo "No TestEmbedded* functions found" >&2
  exit 1
fi

declare -A ALL_TEST_SET=()
while IFS= read -r name; do
  ALL_TEST_SET["$name"]=1
done <<< "$ALL_TESTS"

declare -A MANIFEST_SHARDS=()
if [ -f "$MANIFEST" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%%#*}"
    read -r manifest_total manifest_shard test_name extra <<< "$line"
    if [ -z "${manifest_total:-}" ]; then
      continue
    fi
    if [ -n "${extra:-}" ] || ! [[ "$manifest_total" =~ ^[0-9]+$ ]] || ! [[ "$manifest_shard" =~ ^[0-9]+$ ]] || ! [[ "$test_name" =~ ^TestEmbedded[A-Za-z0-9_]+$ ]]; then
      echo "Invalid shard manifest line: $line" >&2
      exit 1
    fi
    if (( manifest_total != TOTAL_SHARDS )); then
      continue
    fi
    if (( manifest_shard < 1 || manifest_shard > TOTAL_SHARDS )); then
      echo "Invalid shard ${manifest_shard}/${manifest_total} for $test_name in $MANIFEST" >&2
      exit 1
    fi
    if [ -n "${MANIFEST_SHARDS[$test_name]:-}" ]; then
      echo "Duplicate shard manifest entry for $test_name in $MANIFEST" >&2
      exit 1
    fi
    MANIFEST_SHARDS["$test_name"]="$manifest_shard"
  done < "$MANIFEST"
fi

for name in "${!MANIFEST_SHARDS[@]}"; do
  if [ -z "${ALL_TEST_SET[$name]:-}" ]; then
    echo "Shard manifest entry does not match a discovered test: $name" >&2
    exit 1
  fi
done

SHARD_TESTS=()
MANIFEST_COUNT=0
FALLBACK_COUNT=0
while IFS= read -r name; do
  assigned_shard="${MANIFEST_SHARDS[$name]:-}"
  if [ -n "$assigned_shard" ]; then
    if (( assigned_shard == SHARD_NUMBER )); then
      SHARD_TESTS+=("$name")
      MANIFEST_COUNT=$(( MANIFEST_COUNT + 1 ))
    fi
    continue
  fi

  # Use cksum for a portable numeric hash fallback for newly-added tests.
  hash=$(printf %s "$name" | cksum | cut -d ' ' -f 1)
  if (( hash % TOTAL_SHARDS == SHARD_INDEX )); then
    SHARD_TESTS+=("$name")
    FALLBACK_COUNT=$(( FALLBACK_COUNT + 1 ))
  fi
done <<< "$ALL_TESTS"

if [ ${#SHARD_TESTS[@]} -eq 0 ]; then
  echo "Shard ${SHARD_NUMBER}/${TOTAL_SHARDS}: no tests assigned (all hashed to other shards)"
  exit 0
fi

# Build the -run regex: "^(TestA|TestB|TestC)$"
RUN_REGEX="^($(IFS='|'; echo "${SHARD_TESTS[*]}"))$"

echo "Shard ${SHARD_NUMBER}/${TOTAL_SHARDS}: running ${#SHARD_TESTS[@]} test(s)"
echo "  manifest: ${MANIFEST_COUNT}, fallback: ${FALLBACK_COUNT}"
printf "  %s\n" "${SHARD_TESTS[@]}"
echo ""

# Use pre-built test binary if available, otherwise fall back to go test.
CMD_BINARY="${BEADS_TEST_CMD_BINARY:-/tmp/bd-cmd-test}"
if [ "${BEADS_TEST_SHARD_LIST_ONLY:-}" = "1" ]; then
  exit 0
fi

if [ -x "$CMD_BINARY" ]; then
  exec "$CMD_BINARY" -test.v -test.count=1 -test.timeout=20m \
    -test.run "$RUN_REGEX" \
    "$@"
else
  echo "Warning: pre-built test binary not found at $CMD_BINARY, falling back to go test"
  exec go test -tags=gms_pure_go -v -race -count=1 -timeout 20m \
    -run "$RUN_REGEX" \
    "$@" \
    ./cmd/bd/
fi
