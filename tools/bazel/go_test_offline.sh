#!/usr/bin/env bash
# Runs an existing go_test binary (go_test_variant.sh's contract: the first
# argument is its runfiles path, the rest go to the binary unchanged) inside a
# fresh user and network namespace with only loopback up, as namespace root.
# It is the unprivileged equivalent of `sudo unshare --net` plus `ip link set
# lo up`: the binary and every process it starts (bd, its loopback proxy, dolt
# sql-server) can bind and dial 127.0.0.1 and nothing else, which proves the
# code under test needs no outbound network. The workers allow unprivileged
# user namespaces; a host that does not fails here rather than running the
# test with the network it is meant to be without.
#
# The rbe-west fork tier forbids nested namespaces (unshare fails with ENOSPC)
# but already runs every action loopback only. There, and anywhere else
# unshare is unavailable, the binary runs as is, but only after the same
# outbound probe proves the network is absent; a host with network and no
# namespaces still fails.
#
# Usage: go_test_offline.sh [--shard-manifest=<manifest>] <binary> [binary args...]
#
# --shard-manifest=<manifest> (a runfiles path, like <binary>) makes the
# manifest the target's whole test selection, so a narrow selection can be
# sharded: Go's own sharding deals out every test in the binary, not the
# -test.run matches, so most shards would run none. The manifest has one
# "<shard> <TestName>" line per test, shards numbered 1..N with none empty, a
# name once; '#' starts a comment. Under Bazel sharding (TEST_TOTAL_SHARDS >
# 1) N must equal the target's shard_count: Bazel shard k (TEST_SHARD_INDEX
# k-1) runs exactly the tests on the "k ..." lines, with Bazel's sharding
# variables cleared so the binary does not shard them again. Unsharded, the
# binary runs every listed test. Either way the wrapper passes
# -test.run=^(...)$ and refuses (exit 2) a -test.run among the binary's args,
# which would replace the selection in every shard. The selection is made
# before, and changes nothing in, the isolation below.
set -euo pipefail
export GO_TEST_RUN_FROM_BAZEL=1
if [[ "${1:-}" == --shard-manifest=* ]]; then
	manifest="${1#--shard-manifest=}"
	shift
	if [[ -z "$manifest" || $# -eq 0 ]]; then
		echo "usage: go_test_offline.sh [--shard-manifest=<manifest>] <binary> [binary args...]" >&2
		exit 2
	fi
	bin="$1"
	shift
	for arg in "$@"; do
		case "$arg" in
		-test.run | -test.run=* | --test.run | --test.run=*)
			echo "go_test_offline: $manifest selects this target's tests; refusing $arg among the binary's args, which would replace that selection in every shard" >&2
			exit 2
			;;
		esac
	done

	# No associative array (bash >= 4), as in go_test_pinned_shard.sh: the
	# "listed twice" check is a membership test on the "|"-joined names,
	# which the name pattern keeps free of "|".
	declare -a tests_by_shard=()
	shards=0
	all=""
	lineno=0
	while IFS= read -r line || [[ -n "$line" ]]; do
		lineno=$((lineno + 1))
		line="${line%%#*}"
		read -r shard name extra <<<"$line" || true
		[[ -z "${shard:-}" ]] && continue
		if ! [[ "$shard" =~ ^[1-9][0-9]*$ && "${name:-}" =~ ^Test[A-Za-z0-9_]*$ && -z "${extra:-}" ]]; then
			echo "go_test_offline: $manifest:$lineno: want \"<shard> <TestName>\", got: $line" >&2
			exit 1
		fi
		if [[ "|$all|" == *"|$name|"* ]]; then
			echo "go_test_offline: $manifest:$lineno: $name is listed twice" >&2
			exit 1
		fi
		tests_by_shard[shard]="${tests_by_shard[shard]:+${tests_by_shard[shard]}|}$name"
		all="${all:+$all|}$name"
		((shard > shards)) && shards="$shard"
	done <"$manifest"
	if ((shards == 0)); then
		echo "go_test_offline: $manifest lists no tests" >&2
		exit 1
	fi
	for ((s = 1; s <= shards; s++)); do
		if [[ -z "${tests_by_shard[s]:-}" ]]; then
			echo "go_test_offline: $manifest: shard $s has no tests (shards must be numbered 1..N with none empty)" >&2
			exit 1
		fi
	done

	selected="$all"
	total="${TEST_TOTAL_SHARDS:-1}"
	if ((total > 1)); then
		if ((total != shards)); then
			echo "go_test_offline: $manifest has $shards shards; the target's shard_count ($total) must equal it" >&2
			exit 1
		fi
		index="${TEST_SHARD_INDEX:?TEST_TOTAL_SHARDS is set but TEST_SHARD_INDEX is not}"
		if ! [[ "$index" =~ ^[0-9]+$ ]] || ((index >= total)); then
			echo "go_test_offline: TEST_SHARD_INDEX=$index is not a shard of $total" >&2
			exit 1
		fi
		if [[ -n "${TEST_SHARD_STATUS_FILE:-}" ]]; then
			touch "$TEST_SHARD_STATUS_FILE"
		fi
		unset TEST_TOTAL_SHARDS TEST_SHARD_INDEX TEST_SHARD_STATUS_FILE
		selected="${tests_by_shard[index + 1]}"
	fi
	set -- "$bin" "-test.run=^(${selected})\$" "$@"
fi
if ! unshare --user --map-root-user --net -- true 2>/dev/null; then
	if timeout 3 bash -c "exec 3<>/dev/tcp/1.1.1.1/443" 2>/dev/null; then
		echo "go_test_offline: no network namespace available (unshare failed) and outbound network is reachable" >&2
		exit 1
	fi
	bin="$1"
	shift
	exec "./$bin" "$@"
fi
exec unshare --user --map-root-user --net -- bash -euc '
ip link set lo up
# Prove the namespace really has no outbound network before trusting
# anything the test reports.
if timeout 3 bash -c "exec 3<>/dev/tcp/1.1.1.1/443" 2>/dev/null; then
	echo "go_test_offline: outbound network unexpectedly available inside the namespace" >&2
	exit 1
fi
bin="$1"
shift
exec "./$bin" "$@"
' go_test_offline "$@"
