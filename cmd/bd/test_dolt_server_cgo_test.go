//go:build cgo

package main

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func init() {
	beforeTestsHook = startTestDoltServer
}

// startTestDoltServer starts a dedicated Dolt SQL server in a temp directory
// on a dynamic port. This prevents tests from creating testdb_* databases on
// the production Dolt server, which causes lock contention and crashes.
// Returns a cleanup function that stops the server and removes the temp dir.
func startTestDoltServer() func() {
	if _, err := exec.LookPath("dolt"); err != nil {
		// Dolt not installed — tests that need it will skip themselves.
		return func() {}
	}

	tmpDir, err := os.MkdirTemp("", "beads-test-dolt-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to create test dolt dir: %v\n", err)
		return func() {}
	}

	// Initialize a dolt data directory so the server has somewhere to store databases.
	dbDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to create test dolt data dir: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	// Configure dolt user identity (required by dolt init). Since TestMain
	// changes HOME to a temp dir, the global dolt config is gone.
	doltEnv := append(os.Environ(), "DOLT_ROOT_PATH="+tmpDir)
	for _, args := range [][]string{
		{"dolt", "config", "--global", "--add", "user.name", "beads-test"},
		{"dolt", "config", "--global", "--add", "user.email", "test@beads.local"},
	} {
		cfgCmd := exec.Command(args[0], args[1:]...)
		cfgCmd.Env = doltEnv
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: %s failed: %v\n%s\n", args[1], err, out)
			_ = os.RemoveAll(tmpDir)
			return func() {}
		}
	}

	initCmd := exec.Command("dolt", "init")
	initCmd.Dir = dbDir
	initCmd.Env = doltEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: dolt init failed for test server: %v\n%s\n", err, out)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	// Find a free port by binding to :0 and reading the assigned port.
	port, err := findFreePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to find free port for test dolt server: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	// Start the test Dolt server. Use short flags for compatibility across
	// dolt versions (-H, -P). Skip --user (removed in newer versions; the
	// server creates a root@localhost superuser by default).
	serverCmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", fmt.Sprintf("%d", port),
		"--no-auto-commit",
	)
	serverCmd.Dir = dbDir
	serverCmd.Env = doltEnv
	// Discard server logs to keep test output clean. Set BEADS_TEST_DOLT_VERBOSE=1
	// to see server logs when debugging test infrastructure issues.
	if os.Getenv("BEADS_TEST_DOLT_VERBOSE") != "1" {
		serverCmd.Stderr = nil
		serverCmd.Stdout = nil
	}
	if err := serverCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to start test dolt server: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	// Wait for server to accept connections.
	if !waitForServer(port, 10*time.Second) {
		fmt.Fprintf(os.Stderr, "WARN: test dolt server did not become ready on port %d\n", port)
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	// Set the shared test server port so newTestStore/newTestStoreWithPrefix connect here.
	testDoltServerPort = port

	return func() {
		testDoltServerPort = 0
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = os.RemoveAll(tmpDir)
	}
}

// findFreePort finds an available TCP port by binding to :0.
func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// waitForServer polls until the Dolt server accepts a MySQL connection.
func waitForServer(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/?timeout=1s", port)
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			if err := db.Ping(); err == nil {
				_ = db.Close()
				return true
			}
			_ = db.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
