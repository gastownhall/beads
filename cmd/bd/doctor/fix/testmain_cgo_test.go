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
	os.Exit(testMainInner(m))
}

// testMainInner holds TestMain's body so its defer runs before the process
// exits — os.Exit skips deferred calls, so TestMain itself must never defer
// anything (be-5kkk6).
func testMainInner(m *testing.M) int {
	os.Setenv("BEADS_TEST_MODE", "1")
	// AD-01 (be-c5p): allow doctor/fix tests through the dolt.New
	// database-name firewall when they connect to the spawned test server.
	os.Setenv("BEADS_TEST_SERVER", "1")
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
	}

	// Clear out the roots of earlier runs of this suite whose process is
	// gone, before claiming one of our own. A `go test -timeout` panic skips
	// every defer here AND the post-run sweep, so the servers such a run
	// started outlive every cleanup this process installs and nothing ever
	// looks at that run's tree again (wy-j2zc8q). Roots with no owner marker,
	// and roots whose owner is still running, are left untouched.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	// Pin t.TempDir() under a suite-owned root so the sweep below can reap
	// AutoStart leftovers whose directory cleanup failed because the live
	// child still holds the tree (gastownhall/beads#5631). Must never be a
	// shared/global temp dir (see SweepOrphanedTestServers).
	root, pinErr := testutil.PinSuiteTempRoot(suiteRootPrefix + "*")
	if pinErr != nil {
		fmt.Fprintf(os.Stderr, "FATAL: suite temp root: %v\n", pinErr)
		return 1
	}
	suiteTempRoot = root
	defer os.RemoveAll(root)

	// Claim the root for this process so the NEXT run can tell our debris
	// from a concurrent run's live tree.
	if err := doltserver.WriteSuiteOwnerMarker(root); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not claim suite temp root %s: %v\n", root, err)
	}

	code := m.Run()

	// Best-effort reap of any dolt sql-server left running under this
	// suite's own temp root (e.g. a SIGKILLed run) — see
	// gastownhall/beads mybd-q6cz / #5631.
	swept := doltserver.SweepOrphanedTestServers(root)
	code = doltserver.ApplyLeakPolicy("cmd/bd/doctor/fix", code, swept)
	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	os.Unsetenv("BEADS_TEST_SERVER")
	return code
}
