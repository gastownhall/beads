//go:build cgo

package main

import (
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/testutil"
)

func init() {
	beforeTestsHook = startTestDoltServer
}

// startTestDoltServer starts a dedicated Dolt SQL server in a temp directory
// on a dynamic port using the shared testutil helper. This prevents tests
// from creating testdb_* databases on the production Dolt server.
// Returns a cleanup function that stops the server and removes the temp dir.
func startTestDoltServer() func() {
	srv, cleanup := testutil.StartTestDoltServer("beads-test-dolt-*")
	if srv != nil {
		testDoltServerPort = srv.Port
		// Set BEADS_DOLT_PORT so that code paths using applyConfigDefaults
		// (e.g., bd init) connect to the test server instead of port 1.
		os.Setenv("BEADS_DOLT_PORT", fmt.Sprintf("%d", srv.Port))
	}
	return func() {
		testDoltServerPort = 0
		os.Unsetenv("BEADS_DOLT_PORT")
		cleanup()
	}
}
