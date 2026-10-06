package testutil

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// DoltDockerImage is the Docker image used for Dolt test containers.
const DoltDockerImage = "dolthub/dolt-sql-server:2.4.1"

// EnvRequireDoltContainer, set to "1", turns an unavailable Dolt test server
// (either backend, see BEADS_TEST_DOLT_SERVER) into a failure instead of a
// skip: in RequireDoltContainer and StartIsolatedDoltContainer(Handle), and in
// every TestMain through DoltUnavailableForTestMain. Lanes that exist to run
// the Dolt server suites set it so they cannot pass green having run nothing
// (and, under Bazel, have that vacuous pass cached and shared).
const EnvRequireDoltContainer = "BEADS_TEST_REQUIRE_DOLT_CONTAINER"

// DoltUnavailableForTestMain handles an EnsureDoltContainerForTestMain error
// in a TestMain and reports whether the TestMain must exit non-zero. By
// default it prints the historical warning and returns false, so the package
// runs and its Dolt tests skip. With BEADS_TEST_REQUIRE_DOLT_CONTAINER=1 it
// prints a FATAL line and returns true.
func DoltUnavailableForTestMain(err error) bool {
	if os.Getenv(EnvRequireDoltContainer) == "1" {
		fmt.Fprintf(os.Stderr, "FATAL: %v, but %s=1; this lane must not skip its Dolt tests\n", err, EnvRequireDoltContainer)
		return true
	}
	fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	return false
}

// RequireDoltBinary ensures the `dolt` CLI binary is available, and honors
// BEADS_TEST_SKIP=dolt for tests that also depend on the shared
// containerized Dolt SQL server. The test is skipped locally when dolt is
// missing but fatally fails under GitHub Actions (GITHUB_ACTIONS=true) and
// under bazel test, which always provides the pinned dolt. CI
// is expected to install dolt; a missing binary there means the workflow is
// broken, not that the test should be skipped.
func RequireDoltBinary(t *testing.T) {
	t.Helper()
	if hasTestSkipForDoltBinary("dolt") {
		t.Skip("skipping: Dolt tests skipped (BEADS_TEST_SKIP=dolt)")
	}
	requireDoltBinaryPresent(t)
}

// RequireDoltCLIOnly ensures the `dolt` CLI binary is available, WITHOUT
// honoring BEADS_TEST_SKIP=dolt. Use this for tests that shell out to the
// local `dolt` CLI directly and have no dependency on the shared
// containerized Dolt SQL server — BEADS_TEST_SKIP=dolt is a blanket switch
// meant to exclude tests that need that server, so it must not also skip
// tests that only need the CLI binary.
func RequireDoltCLIOnly(t *testing.T) {
	t.Helper()
	requireDoltBinaryPresent(t)
}

// requireDoltBinaryPresent checks for the `dolt` CLI binary and fails or
// skips as appropriate. See RequireDoltBinary and RequireDoltCLIOnly.
func requireDoltBinaryPresent(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("dolt binary missing under GITHUB_ACTIONS: %v — the CI workflow must install dolt (see .github/workflows/ci.yml)", err)
		}
		// Under Bazel the test wrapper (tools/bazel/test_env.sh) puts the
		// pinned dolt from runfiles first on PATH. A skip here would be a
		// vacuous PASS that the shared remote cache then serves to everyone.
		if bazeltest.IsBazel() {
			t.Fatalf("dolt binary missing under bazel test: %v — tools/bazel/test_env.sh must put //tools/bazel:dolt on PATH", err)
		}
		t.Skipf("dolt binary not found: %v", err)
	}
}

func hasTestSkipForDoltBinary(service string) bool {
	for _, s := range strings.Split(os.Getenv("BEADS_TEST_SKIP"), ",") {
		if strings.TrimSpace(s) == service {
			return true
		}
	}
	return false
}

// FindFreePort finds an available TCP port by binding to :0.
func FindFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// WaitForServer polls until the server accepts TCP connections on the given port.
func WaitForServer(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		// #nosec G704 -- addr is always loopback (127.0.0.1) with a test-selected local port.
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
