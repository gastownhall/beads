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
set -euo pipefail
export GO_TEST_RUN_FROM_BAZEL=1
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
