//go:build !cgo

package doctor

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// CGO_ENABLED=0 doctor tests compile this TestMain instead of the cgo e2e
// one. Without it, tests that AutoStart a detached dolt sql-server leave
// the process running after t.TempDir() cleanup (gastownhall/beads#5631).
func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	root, err := testutil.PinSuiteTempRoot("beads-doctor-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: suite temp root: %v\n", err)
		return 1
	}
	suiteTempRoot = root
	defer os.RemoveAll(root)

	code := m.Run()
	doltserver.SweepOrphanedTestServers(root)
	return code
}
