//go:build cgo && unix

package main

// Managed-local entry point for the front-door capability matrix.
//
// Why this exists as a second entry point rather than a topology of the
// primary one: the managed-local fixture is gated on BEADS_TEST_PROXIED_LOCAL,
// and the only lane that sets it is //cmd/bd:bd_managed_local_test, which runs
// exactly the TestManagedLocalProxied* tests its shard manifest
// (cmd/bd/managed_local_shards.txt) lists. The primary matrix runs in the
// proxied-server tier, which does not set that gate, so managed-local would be
// silently skipped there and nowhere else.
//
// The test name is load-bearing: scripts/managed_local_lane_test.go requires
// every TestManagedLocalProxied* test to be listed in that manifest. The lane
// runs inside a loopback-only network namespace, so this entry point must
// build only topologies that need no Docker: embedded and managed-local.
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
