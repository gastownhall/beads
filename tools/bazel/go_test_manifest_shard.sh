#!/usr/bin/env bash
# Runs one Bazel shard of an existing go_test binary through a PR Risk shard
# script (.github/scripts/embedded-test-shard.sh and
# embedded-storage-test-shard.sh), so the Bazel target runs exactly the tests
# the CI job with the same shard number does: the same discovery, the same
# committed manifest and hash fallback, the same binary flags. Like
# go_test_variant.sh it reuses the go_test's binary instead of compiling the
# package again.
#
# Usage: go_test_manifest_shard.sh <shard script> <binary env var> <binary>
#          [binary args...]
#   <shard script>, <binary>: runfiles paths ($(rootpath ...)), relative to
#     the workspace runfiles directory, which is the test's working directory
#     and where the script's source globs (cmd/bd/*_embedded_test.go, ...)
#     resolve.
#   <binary env var>: the variable the script reads the binary path from
#     (BEADS_TEST_CMD_BINARY or BEADS_TEST_EMBEDDED_TEST_BINARY); set to an
#     absolute path, since tests re-exec it from their package directory.
#   The remaining arguments (and any --test_arg) follow the script's own
#   binary flags.
#
# Bazel's shard k of n (shard_count must equal the manifest's shard total)
# becomes the script's shard k+1 of n. The binary itself must not shard again,
# so TEST_TOTAL_SHARDS/TEST_SHARD_INDEX are removed after this wrapper tells
# Bazel it supports sharding. The go_test's own `env` is not inherited (see
# go_test_variant.sh); the target repeats it.
set -euo pipefail
script="$1"
bin_var="$2"
bin="$3"
shift 3

total="${TEST_TOTAL_SHARDS:-1}"
index="${TEST_SHARD_INDEX:-0}"
if [[ -n "${TEST_SHARD_STATUS_FILE:-}" ]]; then
	touch "$TEST_SHARD_STATUS_FILE"
fi
unset TEST_TOTAL_SHARDS TEST_SHARD_INDEX TEST_SHARD_STATUS_FILE

export GO_TEST_RUN_FROM_BAZEL=1
export "$bin_var=$PWD/$bin"
exec bash "$script" "$((index + 1))" "$total" "$@"
