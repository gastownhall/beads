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
const DoltDockerImage = "dolthub/dolt-sql-server:2.2.0"

// RequireDoltBinary ensures the `dolt` CLI binary is available. The test is
// skipped locally when dolt is missing but fatally fails under GitHub Actions
// (GITHUB_ACTIONS=true). CI is expected to install dolt; a missing binary
// there means the workflow is broken, not that the test should be skipped.
func RequireDoltBinary(t *testing.T) {
	t.Helper()
	if hasTestSkipForDoltBinary("dolt") {
		t.Skip("skipping: Dolt tests skipped (BEADS_TEST_SKIP=dolt)")
	}
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

// ambientDoltEnvVars covers the environment variables that decide which Dolt
// endpoint dolt.New resolves, whether it auto-starts a server, and where that
// server's data directory lives: the endpoint overrides consulted by
// applyConfigDefaults (internal/storage/dolt/store.go), the lifecycle switches
// consulted by the store factory and the auto-start path
// (internal/storage/dolt/open.go), and the data-dir override consulted first
// by projectDoltDirPath (internal/doltserver/physical_root.go).
// BEADS_DOLT_SERVER_PORT takes precedence over the legacy BEADS_DOLT_PORT
// spelling, so clearing only the legacy name is not enough.
//
// It is not asserted to be exhaustive: new env reads in internal/storage/dolt
// or internal/doltserver may need to be added here.
var ambientDoltEnvVars = []string{
	"BEADS_DOLT_SERVER_PORT",
	"BEADS_DOLT_PORT",
	"BEADS_DOLT_SERVER_HOST",
	"BEADS_DOLT_SERVER_SOCKET",
	"BEADS_DOLT_SERVER_MODE",
	"BEADS_DOLT_SHARED_SERVER",
	"BEADS_SHARED_SERVER_DIR",
	"BEADS_DOLT_AUTO_START",
	"BEADS_DOLT_DATA_DIR",
}

// ClearAmbientDoltEnv clears the ambientDoltEnvVars overrides, so a test that
// constructs its own store with AutoStart reaches that store, in its own tree,
// and nothing else. Use it before calling dolt.New in any test that expects an
// isolated local server.
//
// This matters for safety as well as determinism: a package whose TestMain
// sets BEADS_TEST_SERVER=1 will happily issue CREATE DATABASE plus schema and
// data writes against whatever endpoint it resolves, so an ambient override
// pointing at a developer's real Dolt server would be written to by running
// the suite — and an ambient data-dir override would have that server lay down
// .dolt/ and its databases in a directory the test never chose.
//
// t.Setenv restores the previous values when the test finishes. It also panics
// if the test or any parallel ancestor called t.Parallel, so this helper is
// not callable from a parallel test.
func ClearAmbientDoltEnv(t *testing.T) {
	t.Helper()
	for _, name := range ambientDoltEnvVars {
		t.Setenv(name, "")
	}
}
