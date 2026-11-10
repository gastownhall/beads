package doctor

import (
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// suiteTempRoot is the TestMain-owned temp directory that t.TempDir must
// land under so SweepSuiteTestServers can reap leaked sql-servers.
var suiteTempRoot string

// suiteRootPrefix is this suite's PinSuiteTempRoot pattern without its
// random tail. SweepDeadSuiteRoots globs for it, so the two must not drift.
const suiteRootPrefix = "beads-doctor-tests-"

func TestTempDirLandsUnderSuiteSweepRoot(t *testing.T) {
	if suiteTempRoot == "" {
		t.Fatal("TestMain did not pin suiteTempRoot; leaked dolt sql-server processes cannot be swept")
	}
	tmp := t.TempDir()
	if !testutil.PathUnderSuiteRoot(tmp, suiteTempRoot) {
		t.Fatalf("t.TempDir() %q is not under suiteTempRoot %q", tmp, suiteTempRoot)
	}
}
