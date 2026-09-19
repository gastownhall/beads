//go:build cgo && unix

package main

// Managed-local entry point for the front-door capability matrix.
//
// Why this exists as a second entry point rather than a topology of the
// primary one: the managed-local fixture is gated on BEADS_TEST_PROXIED_LOCAL,
// and the only lane that sets it is .github/workflows/proxied-local-smoke.yml,
// which selects tests with -test.run "^TestManagedLocalProxied". The primary
// matrix runs in the proxied shard, which does not set that gate, so
// managed-local would be silently skipped there and nowhere else.
//
// Both the file name (matching the lane's `cmd/bd/proxied_local_*` path
// filter) and the test name are load-bearing. The smoke lane runs inside a
// loopback-only network namespace, so this entry point must build only
// topologies that need no Docker: embedded and managed-local.
//
// The table itself lives in topology_capability_matrix_test.go. This file
// adds no expectations of its own.

import "testing"

func TestManagedLocalProxiedCapabilityMatrix(t *testing.T) {
	runCapabilityMatrix(t, []topologySpec{
		embeddedTopology(),
		proxiedLocalTopology(),
	})
}
