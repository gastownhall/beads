package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// suiteRootPrefix is this suite's os.MkdirTemp pattern without its random
// tail. SweepDeadSuiteRoots globs for it, so the two must not drift.
const suiteRootPrefix = "beads-internal-tests-"

func TestMain(m *testing.M) {
	// Clear out the roots of earlier runs of this suite whose process is
	// gone, before claiming one of our own. A `go test -timeout` panic skips
	// every cleanup below AND the post-run sweep, so the servers such a run
	// started outlive everything this process installs and nothing ever looks
	// at that run's tree again (wy-j2zc8q). Roots with no owner marker, and
	// roots whose owner is still running, are left untouched.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	root, err := os.MkdirTemp("", suiteRootPrefix+"*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create test temp dir: %v\n", err)
		os.Exit(1)
	}

	// Claim the root for this process so the NEXT run can tell our debris
	// from a concurrent run's live tree.
	if err := doltserver.WriteSuiteOwnerMarker(root); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not claim suite temp root %s: %v\n", root, err)
	}

	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create test home dir: %v\n", err)
		os.RemoveAll(root)
		os.Exit(1)
	}
	gitConfig := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create test gitconfig: %v\n", err)
		os.RemoveAll(root)
		os.Exit(1)
	}

	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	_ = os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	_ = os.Setenv("GIT_CONFIG_GLOBAL", gitConfig)

	integrationCleanup, err := setupIntegrationTestMain(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to set up integration tests: %v\n", err)
		os.RemoveAll(root)
		os.Exit(1)
	}

	code := m.Run()

	// Best-effort reap of any dolt sql-server left running under this
	// suite's temp root (e.g. auto-started by the BEADS_TEST_BD_BINARY
	// this TestMain builds, if a SIGKILLed run left one behind) — see
	// gastownhall/beads mybd-q6cz.
	swept := doltserver.SweepOrphanedTestServers(root)
	code = doltserver.ApplyLeakPolicy("internal/beads", code, swept)

	integrationCleanup()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
