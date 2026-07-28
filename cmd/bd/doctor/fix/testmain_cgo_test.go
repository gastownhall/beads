//go:build cgo

package fix

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestMain starts an isolated Dolt server so fix tests don't hit the
// production server on port 3307.
func TestMain(m *testing.M) {
	os.Setenv("BEADS_TEST_MODE", "1")
	// AD-01 (be-c5p): allow doctor/fix tests through the dolt.New
	// database-name firewall when they connect to the spawned test server.
	os.Setenv("BEADS_TEST_SERVER", "1")
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
	}

	// Suite-owned root for the orphan-server sweep below. Must never be a
	// shared/global temp dir (see SweepOrphanedTestServers) — this one is
	// unique to this test run and removed when it exits.
	suiteTempRoot, tempRootErr := os.MkdirTemp("", "beads-fix-tests-*")
	if tempRootErr != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to create suite temp root: %v\n", tempRootErr)
	}

	code := m.Run()

	// Best-effort reap of any dolt sql-server left running under this
	// suite's own temp root (e.g. a SIGKILLed run) — see
	// gastownhall/beads mybd-q6cz.
	doltserver.SweepOrphanedTestServers(suiteTempRoot)

	if suiteTempRoot != "" {
		os.RemoveAll(suiteTempRoot)
	}
	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	os.Unsetenv("BEADS_TEST_SERVER")
	os.Exit(code)
}
