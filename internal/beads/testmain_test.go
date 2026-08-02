package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "beads-internal-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create test temp dir: %v\n", err)
		os.Exit(1)
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
	doltserver.SweepOrphanedTestServers(root)

	integrationCleanup()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
