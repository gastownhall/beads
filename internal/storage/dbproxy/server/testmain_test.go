package server_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// suiteRootPrefix is this suite's PinSuiteTempRoot pattern without its random
// tail. SweepDeadSuiteRoots globs for it, so the two must not drift. It is
// deliberately short: this package's unix-socket test builds a socket path
// under t.TempDir(), and sun_path is 108 bytes on linux / 104 on macOS.
const suiteRootPrefix = "beads-dbproxy-server-tests-"

// suiteTempRoot is the TestMain-owned temp directory that every t.TempDir()
// in this package lands under, so the sweeps have a root they can vouch for.
var suiteTempRoot string

// TestMain puts this package under the same suite-lifecycle contract as every
// other package that daemonizes a `dolt sql-server`.
//
// This suite starts real servers: newDoltServer (and the fixtures that build
// a DoltServer by hand) spawn `dolt sql-server` with cmd.Dir set to the
// rootDir, which is a t.TempDir(). Until now the package had no TestMain at
// all, which left two holes:
//
//   - A run killed by `go test -timeout`, CI cancel, or Ctrl-C skips every
//     t.Cleanup, so the server outlives the test binary. Its cwd survives too
//     (nothing deleted it), and this package claimed no suite root — so that
//     debris was reachable by neither arm of the sweep and simply accumulated.
//     Claiming a root here is what makes the NEXT run able to reap it.
//
//   - A server this suite leaks on a normal run has its cwd deleted by
//     t.TempDir's RemoveAll. A root-scoped post-run sweep detects that leak
//     and fails this package without consuming another live suite's evidence.
func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	// Clear out the roots of earlier runs of this suite whose process is
	// gone, before claiming one of our own. Roots with no owner marker, and
	// roots whose owner is still running (a parallel package under
	// scripts/test.sh, a second `go test`), are left untouched.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	// Pin TMPDIR under a suite-owned root so every t.TempDir() — including
	// each server's rootDir, which is its working directory — is nested under
	// something the sweeps may vouch for.
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

	// Best-effort reap of any dolt sql-server still running under this run's
	// own root — the backstop for a test whose Cleanup did not get to run.
	swept := doltserver.SweepSuiteTestServers(root)
	return doltserver.ApplyLeakPolicy("internal/storage/dbproxy/server", code, swept)
}

// TestTempDirLandsUnderSuiteSweepRoot guards the pinning above: if t.TempDir()
// ever stops landing under suiteTempRoot, the post-run sweep silently loses
// its scope and leaked sql-servers become unreapable again.
func TestTempDirLandsUnderSuiteSweepRoot(t *testing.T) {
	if suiteTempRoot == "" {
		t.Fatal("TestMain did not pin suiteTempRoot; leaked dolt sql-server processes cannot be swept")
	}
	dir := t.TempDir()
	if !testutil.PathUnderSuiteRoot(dir, suiteTempRoot) {
		t.Fatalf("t.TempDir() %q is not under suiteTempRoot %q", dir, suiteTempRoot)
	}
}
