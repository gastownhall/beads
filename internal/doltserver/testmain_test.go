package doltserver_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
)

// TestMain isolates every direct package run from the operator's beads config
// and sweeps detached test servers. scripts/test.sh already provides this
// boundary; keeping it here makes `go test ./internal/doltserver` equally safe.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "beads-doltserver-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "doltserver tests: create isolated home: %v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", root)
	_ = os.Setenv("USERPROFILE", root)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	_ = os.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	_ = os.Setenv("BEADS_TEST_MODE", "1")
	_ = os.Setenv("BEADS_TEST_PDEATHSIG", "1")
	config.ResetForTesting()

	code := m.Run()

	if killed := doltserver.SweepOrphanedTestServers(); len(killed) > 0 {
		fmt.Fprintf(os.Stderr, "doltserver tests: swept %d orphaned dolt sql-server process(es)\n", len(killed))
	}
	config.ResetForTesting()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
