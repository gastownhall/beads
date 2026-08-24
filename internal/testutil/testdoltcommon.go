package testutil

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// DoltDockerImage is the Docker image used for Dolt test containers.
//
// Known limitations of this containerized (podman-rootless + testcontainers-go)
// real-server harness — both confirmed empirically, and neither reachable in
// production or on real GitHub Actions CI:
//
//   - Schema migration 0032 (drop_schema_migrations_applied_at) hangs
//     indefinitely over the sql-server wire protocol in this harness,
//     surfacing as "failed to create embedded DoltStore: failed to
//     initialize schema: context deadline exceeded" (~60s). Confirmed
//     specific to this harness's container port-forwarding path, not to
//     migration 0032's SQL itself: the identical statement completes
//     normally via the embedded/CLI engine and against a bare-host-process
//     dolt sql-server matching this fleet's real deployment shape. See
//     be-j3szz for the full evidence chain.
//   - Containers can leak because Ryuk (testcontainers-go's orphan-reaper
//     sidecar) is disabled for this harness (TESTCONTAINERS_RYUK_DISABLED=true,
//     a workaround for this host's broken dockerd/containerd — see be-hsa9t),
//     so there is no safety net when a test process exits without running
//     its cleanup (e.g. os.Exit called before a deferred
//     TerminateDoltContainer). See be-5kkk6 for a historical incident (101
//     leaked containers, swap exhaustion) and the testMainInner pattern this
//     repo now uses elsewhere to guarantee deferred cleanup actually runs.
const DoltDockerImage = "dolthub/dolt-sql-server:2.2.0"

// RequireDoltBinary ensures the `dolt` CLI binary is available, and honors
// BEADS_TEST_SKIP=dolt for tests that also depend on the shared
// containerized Dolt SQL server. The test is skipped locally when dolt is
// missing but fatally fails under GitHub Actions (GITHUB_ACTIONS=true). CI
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
