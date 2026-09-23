//go:build integration && !windows

package doltserver_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

const integrationSuiteRootPrefix = "beads-doltserver-tests-"

var integrationSuiteTempRoot string

// TestMain covers the integration-tagged tests in this file's package
// (lifecycle_integration_test.go, dirty_state_test.go, port_race_test.go,
// socket_integration_test.go), which call doltserver.Start directly against
// a t.TempDir()-backed .beads dir. Those are the most direct match for the
// leaked-server evidence in gastownhall/beads mybd-q6cz: a real embedded
// dolt sql-server, detached (Setpgid) so it survives its parent, started
// against a temp dir that a SIGKILLed test run's cleanup later deletes out
// from under it.
//
// Previously this package (unlike the others in this tree) had no TestMain
// at all, so BEADS_TEST_MODE was never set here and nothing swept orphans
// on exit — both gaps this closes.
//
// BEADS_TEST_PDEATHSIG=1 is set alongside BEADS_TEST_MODE because this
// TestMain's own process calls doltserver.Start directly (in-process, no
// exec boundary) and stays alive for the server's whole lifetime — exactly
// the case Pdeathsig is meant to protect. See procattr_linux.go for why
// this is a narrower, separate flag from BEADS_TEST_MODE.
func TestMain(m *testing.M) {
	os.Exit(runIntegrationTests(m))
}

func runIntegrationTests(m *testing.M) int {
	doltserver.SweepDeadSuiteRoots(os.TempDir(), integrationSuiteRootPrefix)
	root, err := testutil.PinSuiteTempRoot(integrationSuiteRootPrefix + "*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: suite temp root: %v\n", err)
		return 1
	}
	integrationSuiteTempRoot = root
	defer os.RemoveAll(root)
	if err := doltserver.WriteSuiteOwnerMarker(root); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not claim suite temp root %s: %v\n", root, err)
	}

	os.Setenv("BEADS_TEST_MODE", "1")
	os.Setenv("BEADS_TEST_PDEATHSIG", "1")

	code := m.Run()

	// Pinning both temporary-directory APIs above gives this sweep ownership
	// even after a test has deleted the leaked server's working directory.
	killed := doltserver.SweepSuiteTestServers(root)
	code = doltserver.ApplyLeakPolicy("internal/doltserver (integration)", code, killed)

	os.Unsetenv("BEADS_TEST_MODE")
	os.Unsetenv("BEADS_TEST_PDEATHSIG")
	return code
}

func TestTempDirLandsUnderSuiteSweepRoot(t *testing.T) {
	if integrationSuiteTempRoot == "" {
		t.Fatal("TestMain did not pin integrationSuiteTempRoot")
	}
	if dir := t.TempDir(); !testutil.PathUnderSuiteRoot(dir, integrationSuiteTempRoot) {
		t.Fatalf("t.TempDir() %q is outside suite root %q", dir, integrationSuiteTempRoot)
	}
	dir, err := os.MkdirTemp("", "guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if !testutil.PathUnderSuiteRoot(dir, integrationSuiteTempRoot) {
		t.Fatalf("os.MkdirTemp() %q is outside suite root %q", dir, integrationSuiteTempRoot)
	}
}
