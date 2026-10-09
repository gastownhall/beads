#!/usr/bin/env bash
# Runs one Bazel shard of an existing go_test binary through a PR Risk shard
# script (.github/scripts/embedded-test-shard.sh,
# embedded-storage-test-shard.sh, proxied-test-shard.sh,
# server-storage-test-shard.sh), so the Bazel target runs exactly the tests
# the CI job with the same shard number does: the same discovery, the same
# committed manifest and hash fallback, the same binary flags. Like
# go_test_variant.sh it reuses the go_test's binary instead of compiling the
# package again.
#
# Usage: go_test_manifest_shard.sh [--shard-offset=N --shard-total=M]
#          <shard script> <binary env var> <binary> [binary args...]
#   <shard script>, <binary>: runfiles paths ($(rootpath ...)), relative to
#     the workspace runfiles directory, which is the test's working directory
#     and where the script's source globs (cmd/bd/*_embedded_test.go, ...)
#     resolve.
#   <binary env var>: the variable the script reads the binary path from
#     (BEADS_TEST_CMD_BINARY, BEADS_TEST_EMBEDDED_TEST_BINARY or
#     BEADS_TEST_SERVER_TEST_BINARY); set to an
#     absolute path, since tests re-exec it from their package directory.
#   The remaining arguments (and any --test_arg) follow the script's own
#   binary flags.
#
# Bazel's shard k of n (shard_count must equal the manifest's shard total)
# becomes the script's shard k+1 of n. A manifest block larger than Bazel's
# 50-shard cap per rule is split over several targets: with --shard-offset=N
# --shard-total=M, shard k of n becomes the script's shard N+k+1 of M, so
# targets with offsets 0, n1, n1+n2, ... and shard counts summing to M run
# every shard of the block exactly once (check_shard_coverage.py checks
# that the targets' ranges tile 1..M). The binary itself must not shard again,
# so TEST_TOTAL_SHARDS/TEST_SHARD_INDEX are removed after this wrapper tells
# Bazel it supports sharding. The go_test's own `env` is not inherited (see
# go_test_variant.sh); the target repeats it.
set -euo pipefail
offset=0
part_total=""
while [[ "${1:-}" == --shard-* ]]; do
	case "$1" in
	--shard-offset=*) offset="${1#*=}" ;;
	--shard-total=*) part_total="${1#*=}" ;;
	*)
		echo "go_test_manifest_shard.sh: unknown flag $1" >&2
		exit 2
		;;
	esac
	shift
done
script="$1"
bin_var="$2"
bin="$3"
shift 3

total="${TEST_TOTAL_SHARDS:-1}"
index="${TEST_SHARD_INDEX:-0}"
if [[ -n "$part_total" ]]; then
	if ! [[ "$offset" =~ ^[0-9]+$ && "$part_total" =~ ^[0-9]+$ ]] || ((offset + total > part_total)); then
		echo "go_test_manifest_shard.sh: --shard-offset=$offset with $total shards exceeds --shard-total=$part_total" >&2
		exit 2
	fi
	index=$((offset + index))
	total="$part_total"
elif ((offset != 0)); then
	echo "go_test_manifest_shard.sh: --shard-offset needs --shard-total" >&2
	exit 2
fi
if [[ -n "${TEST_SHARD_STATUS_FILE:-}" ]]; then
	touch "$TEST_SHARD_STATUS_FILE"
fi
unset TEST_TOTAL_SHARDS TEST_SHARD_INDEX TEST_SHARD_STATUS_FILE

export GO_TEST_RUN_FROM_BAZEL=1
export "$bin_var=$PWD/$bin"
exec bash "$script" "$((index + 1))" "$total" "$@"
