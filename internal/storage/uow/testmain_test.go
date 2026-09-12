package uow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// suiteRootPrefix is this suite's os.MkdirTemp pattern without its random
// tail. SweepDeadSuiteRoots globs for it, so the two must not drift.
const suiteRootPrefix = "beads-storage-uow-tests-"

// suiteTempRoot is the TestMain-owned temp directory that every t.TempDir()
// in this package lands under, so SweepOrphanedTestServers has a root it can
// vouch for when it reaps leaked dolt sql-servers.
var suiteTempRoot string

// TestMain gives this package the suite-lifecycle it was missing.
//
// The integration tests here (newTestUOWProvider) start a real proxied
// `dolt sql-server` whose data directory is a t.TempDir(). Cleanup was purely
// per-test — proxy.Shutdown in a t.Cleanup — and there was no TestMain at
// all, so a run killed by `go test -timeout` (which panics the binary and
// skips every Cleanup) left the server running with its data directory
// deleted out from under it, forever. Observed live on a dev box as a
// TestUOWDependencyEditorContract server still serving a temp dir that had
// been gone for hours (wy-j2zc8q).
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
	// the storeRootDir a provider serves — is nested under something the
	// post-run sweep may vouch for. Same pattern as cmd/bd/doctor's TestMain.
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
	swept := doltserver.SweepOrphanedTestServers(root)
	return doltserver.ApplyLeakPolicy("internal/storage/uow", code, swept)
}

// TestTempDirLandsUnderSuiteSweepRoot guards the pinning above: if t.TempDir()
// ever stops landing under suiteTempRoot, the post-run sweep silently loses
// its scope and leaked sql-servers become unreapable again.
func TestTempDirLandsUnderSuiteSweepRoot(t *testing.T) {
	if suiteTempRoot == "" {
		t.Fatal("TestMain did not pin suiteTempRoot; leaked dolt sql-server processes cannot be swept")
	}
	dir := t.TempDir()
	if !pathUnderRoot(dir, suiteTempRoot) {
		t.Fatalf("t.TempDir() %q is not under suiteTempRoot %q", dir, suiteTempRoot)
	}
}

// pathUnderRoot reports whether dir is root or nested under it, comparing
// symlink-resolved paths (macOS hands out /var paths that resolve to
// /private/var).
func pathUnderRoot(dir, root string) bool {
	dir = evalOrSelf(dir)
	root = evalOrSelf(root)
	if dir == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(dir, strings.TrimRight(root, sep)+sep)
}

func evalOrSelf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
