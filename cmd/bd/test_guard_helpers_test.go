package main

import (
	"os"
	"testing"
)

// requireTestGuardDisabled skips destructive integration tests unless the
// BEADS_TEST_GUARD_DISABLE flag is set, mirroring the behavior enforced by the
// guard when running the full suite.
func requireTestGuardDisabled(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_GUARD_DISABLE") != "" {
		return
	}
	t.Skip("set BEADS_TEST_GUARD_DISABLE=1 to run this integration test")
}
