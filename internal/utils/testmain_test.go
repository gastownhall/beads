//go:build cgo

package utils

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// DoltTestServerPort is the port of the isolated test Dolt server (0 = not running).
// Set by TestMain before tests run so that the store-backed tests connect to the test
// server instead of the production Dolt server on port 3307.
var DoltTestServerPort int

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	os.Setenv("BEADS_TEST_MODE", "1")
	// AD-01 (be-c5p): allow utils tests to connect to the test container.
	os.Setenv("BEADS_TEST_SERVER", "1")
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
		DoltTestServerPort = testutil.DoltContainerPortInt()
	}

	code := m.Run()

	DoltTestServerPort = 0
	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	return code
}
