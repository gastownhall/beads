//go:build integration

package beads

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/testutil"
)

func setupIntegrationTestMain(root string) (func(), error) {
	cleanup := func() {
		os.Unsetenv("BEADS_DOLT_PORT")
		os.Unsetenv("BEADS_TEST_BD_BINARY")
		os.Unsetenv("BEADS_TEST_MODE")
		os.Unsetenv("BEADS_TEST_SERVER")
	}

	os.Setenv("BEADS_TEST_MODE", "1")
	// AD-01 (be-c5p): allow integration tests to connect to the test container.
	os.Setenv("BEADS_TEST_SERVER", "1")

	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
		return cleanup, nil
	}

	binName := "bd"
	if runtime.GOOS == "windows" {
		binName = "bd.exe"
	}

	modRootCmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	modRootOut, err := modRootCmd.Output()
	if err != nil {
		testutil.TerminateDoltContainer()
		cleanup()
		return nil, fmt.Errorf("find module root: %w", err)
	}
	modRoot := strings.TrimSpace(string(modRootOut))

	testBDBinary := filepath.Join(root, binName)
	cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", testBDBinary, "./cmd/bd")
	cmd.Dir = modRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		testutil.TerminateDoltContainer()
		cleanup()
		return nil, fmt.Errorf("build bd binary: %w\n%s", err, out)
	}
	os.Setenv("BEADS_TEST_BD_BINARY", testBDBinary)

	return func() {
		testutil.TerminateDoltContainer()
		cleanup()
	}, nil
}
