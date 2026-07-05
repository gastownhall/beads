//go:build integration && !windows

package doltserver_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

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
func TestMain(m *testing.M) {
	os.Setenv("BEADS_TEST_MODE", "1")

	code := m.Run()

	// No suite root passed: each test here owns its own t.TempDir(), not a
	// shared package-level root, so this relies solely on the cwdDeleted
	// signal (a leaked server's temp dir was already cleaned up out from
	// under it) rather than any cwd-under-root match.
	killed := doltserver.SweepOrphanedTestServers()
	if len(killed) > 0 {
		fmt.Fprintf(os.Stderr, "doltserver integration tests: swept %d orphaned dolt sql-server process(es)\n", len(killed))
	}

	os.Unsetenv("BEADS_TEST_MODE")
	os.Exit(code)
}
