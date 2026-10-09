#!/usr/bin/env bash
# Runs one Bazel shard of an existing go_test binary with the suite's slowest
# top-level tests pinned to shards of their own, so no shard's wall time is
# set by where the binary's round-robin split happens to drop its long tests.
# Like go_test_variant.sh it reuses the go_test's binary instead of compiling
# the package again.
#
# Usage: go_test_pinned_shard.sh <manifest> <binary> [binary args...]
#   <manifest>, <binary>: runfiles paths ($(rootpath ...)), relative to the
#     workspace runfiles directory, which is the test's working directory.
#   The remaining arguments (and any --test_arg) go to the binary unchanged,
#     after this wrapper's own -test.run/-test.skip.
#
# The manifest (tools/bazel/pin_shards.py writes it from measured test.xml
# durations) has one "<pinned shard> <TestName>" line per pinned test, pinned
# shards numbered 1..P with none empty; '#' starts a comment. With Bazel's
# shard_count N > P:
#   Bazel shards 0..P-1   run exactly their pinned tests (-test.run), with the
#                         binary's own sharding switched off;
#   Bazel shards P..N-1   are the binary's own round-robin shards 0..N-P-1 over
#                         every top-level test, minus every pinned one
#                         (-test.skip).
# So every test runs in exactly one shard: a pinned test only in its pinned
# shard, any other test (one added since the manifest was written included)
# where the binary's round-robin puts it. A manifest name with no test runs
# nothing (scripts/pinned_shards_test.go rejects one). Unsharded (no
# TEST_TOTAL_SHARDS, e.g. --test_sharding_strategy=disabled) the binary runs
# every test.
#
# rules_go sets GO_TEST_RUN_FROM_BAZEL=1 only for the go_test itself; this
# exports it, as go_test_variant.sh does, so the binary still changes into its
# package directory. The go_test's `env` is not inherited; the target repeats
# it.
set -euo pipefail
manifest="$1"
bin="$2"
shift 2
export GO_TEST_RUN_FROM_BAZEL=1

total="${TEST_TOTAL_SHARDS:-1}"
if [[ "$total" -le 1 ]]; then
	exec "./$bin" "$@"
fi
index="${TEST_SHARD_INDEX:?TEST_TOTAL_SHARDS is set but TEST_SHARD_INDEX is not}"

declare -A shard_of=()
declare -a pinned_by_shard=()
pinned_shards=0
all=""
lineno=0
while IFS= read -r line || [[ -n "$line" ]]; do
	lineno=$((lineno + 1))
	line="${line%%#*}"
	read -r shard name extra <<<"$line" || true
	[[ -z "${shard:-}" ]] && continue
	if ! [[ "$shard" =~ ^[1-9][0-9]*$ && "${name:-}" =~ ^Test[A-Za-z0-9_]*$ && -z "${extra:-}" ]]; then
		echo "$manifest:$lineno: want \"<pinned shard> <TestName>\", got: $line" >&2
		exit 1
	fi
	if [[ -n "${shard_of[$name]:-}" ]]; then
		echo "$manifest:$lineno: $name is pinned twice" >&2
		exit 1
	fi
	shard_of[$name]="$shard"
	pinned_by_shard[shard]="${pinned_by_shard[shard]:+${pinned_by_shard[shard]}|}$name"
	all="${all:+$all|}$name"
	((shard > pinned_shards)) && pinned_shards="$shard"
done <"$manifest"

for ((s = 1; s <= pinned_shards; s++)); do
	if [[ -z "${pinned_by_shard[s]:-}" ]]; then
		echo "$manifest: pinned shard $s has no tests (shards must be numbered 1..P with none empty)" >&2
		exit 1
	fi
done
if ((pinned_shards == 0 || total <= pinned_shards)); then
	echo "$manifest pins $pinned_shards shards; the target's shard_count ($total) must be larger" >&2
	exit 1
fi

if [[ -n "${TEST_SHARD_STATUS_FILE:-}" ]]; then
	touch "$TEST_SHARD_STATUS_FILE"
fi
if ((index < pinned_shards)); then
	unset TEST_TOTAL_SHARDS TEST_SHARD_INDEX TEST_SHARD_STATUS_FILE
	exec "./$bin" "-test.run=^(${pinned_by_shard[index + 1]})\$" "$@"
fi
export TEST_TOTAL_SHARDS=$((total - pinned_shards))
export TEST_SHARD_INDEX=$((index - pinned_shards))
exec "./$bin" "-test.skip=^(${all})\$" "$@"
