//go:build !cgo

package fix

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// CGO_ENABLED=0 fix tests compile this TestMain instead of the cgo one.
// Without it, FixMissingMetadata AutoStart leaves detached sql-servers
// running after t.TempDir() cleanup (gastownhall/beads#5631).
func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	// Clear out the roots of earlier runs of this suite whose process is
	// gone, before claiming one of our own. A `go test -timeout` panic skips
	// every defer here AND the post-run sweep, so the servers such a run
	// started outlive every cleanup this process installs and nothing ever
	// looks at that run's tree again (wy-j2zc8q). Roots with no owner marker,
	// and roots whose owner is still running, are left untouched.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	root, err := testutil.PinSuiteTempRoot(suiteRootPrefix + "*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: suite temp root: %v\n", err)
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
	doltserver.SweepOrphanedTestServers(root)
	return code
}
