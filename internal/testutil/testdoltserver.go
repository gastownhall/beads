package testutil

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const testPidDir = "/tmp"
const testPidPrefix = "beads-test-dolt-"

// TestDoltServer represents a running test dolt server instance.
type TestDoltServer struct {
	Port     int
	cmd      *exec.Cmd
	tmpDir   string
	pidFile  string
	crashed  chan struct{} // closed when server exits unexpectedly
	exitErr  error         // set before crashed is closed
	exitOnce sync.Once
}

// serverStartTimeout is the max time to wait for the test dolt server to accept connections.
const serverStartTimeout = 30 * time.Second

// Module-level singleton state. sync.Once ensures at most one server per test binary.
// Cross-process coordination uses file locks (see testdoltserver_unix.go).
var (
	doltServerOnce   sync.Once
	doltServerErr    error
	doltTestPort     string
	doltSingletonSrv *TestDoltServer
	// doltLockFile is held with LOCK_SH for the lifetime of the test binary (Unix only).
	doltLockFile *os.File
	// doltWeStarted tracks whether this process started the server (vs reusing).
	doltWeStarted bool
	// doltPortSetByUs tracks whether we set BEADS_DOLT_PORT (vs it being set externally).
	doltPortSetByUs bool
)

// StartTestDoltServer starts a dedicated Dolt SQL server in a temp directory
// on a dynamic port. Uses file locks for cross-process port coordination to
// prevent zombie server leaks from port allocation races.
//
// If BEADS_DOLT_PORT is already set in the environment (e.g. by an outer test
// runner or scripts/test.sh with BEADS_TEST_SHARED_SERVER=1), the existing
// server is reused and cleanup is a no-op.
//
// tmpDirPrefix is the os.MkdirTemp prefix (e.g. "beads-test-dolt-*").
// Returns the server (nil if dolt not installed) and a cleanup function.
func StartTestDoltServer(tmpDirPrefix string) (*TestDoltServer, func()) {
	// Reuse existing server if BEADS_DOLT_PORT is already set by an outer runner.
	// This avoids spawning redundant dolt processes when running go test ./...
	// with a pre-started shared server.
	//
	// FIREWALL: Never reuse the production Dolt server (port 3307) for tests.
	// This guard does NOT depend on BEADS_TEST_MODE — that env var may not be set yet
	// when TestMain calls StartTestDoltServer (it's set AFTER this returns).
	// Clown Shows #12-#18: every time this guard had a hole, production got polluted.
	if port := os.Getenv("BEADS_DOLT_PORT"); port != "" {
		p, err := strconv.Atoi(port)
		if err == nil && p == 3307 {
			// Port 3307 is ALWAYS production. Never reuse it, regardless of BEADS_TEST_MODE.
			fmt.Fprintf(os.Stderr, "WARN: BEADS_DOLT_PORT=%d is production — starting isolated test server\n", p)
		} else if err == nil && WaitForServer(p, 2*time.Second) {
			return &TestDoltServer{Port: p}, func() {}
		} else {
			fmt.Fprintf(os.Stderr, "WARN: BEADS_DOLT_PORT=%s set but server not reachable, starting new server\n", port)
		}
	}

	// Singleton: start at most one server per test binary.
	doltServerOnce.Do(func() {
		CleanStaleTestServers()

		if _, err := exec.LookPath("dolt"); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: dolt not found in PATH, skipping test server\n")
			// Leave doltServerErr as nil so we return (nil, noop) rather than error
			return
		}

		doltServerErr = startDoltServerInternal(tmpDirPrefix)
	})

	if doltServerErr != nil {
		fmt.Fprintf(os.Stderr, "WARN: test dolt server failed to start: %v\n", doltServerErr)
		return nil, func() {}
	}
	if doltSingletonSrv == nil {
		return nil, func() {}
	}

	return doltSingletonSrv, func() {
		cleanupDoltServerInternal()
	}
}

// IsCrashed returns true if the server process has exited unexpectedly.
// Returns false for reused servers (BEADS_DOLT_PORT) where we don't own the process.
func (s *TestDoltServer) IsCrashed() bool {
	if s == nil || s.crashed == nil {
		return false
	}
	select {
	case <-s.crashed:
		return true
	default:
		return false
	}
}

// CrashError returns the server's exit error if it crashed, nil otherwise.
func (s *TestDoltServer) CrashError() error {
	if s == nil || s.crashed == nil {
		return nil
	}
	select {
	case <-s.crashed:
		return s.exitErr
	default:
		return nil
	}
}

// cleanup stops the server, removes temp dir and PID file.
func (s *TestDoltServer) cleanup() {
	if s == nil {
		return
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		if s.crashed != nil {
			// Wait for monitor goroutine to finish (avoids double-Wait)
			<-s.crashed
		} else {
			_ = s.cmd.Wait()
		}
	}
	if s.tmpDir != "" {
		_ = os.RemoveAll(s.tmpDir)
	}
	if s.pidFile != "" {
		_ = os.Remove(s.pidFile)
	}
}

// prepareDoltDir creates a temp directory with dolt initialized and configured.
// Returns (tmpDir, dbDir, doltEnv, error).
func prepareDoltDir(tmpDirPrefix string) (string, string, []string, error) {
	tmpDir, err := os.MkdirTemp("", tmpDirPrefix)
	if err != nil {
		return "", "", nil, fmt.Errorf("creating temp dir: %w", err)
	}

	dbDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", nil, fmt.Errorf("creating data dir: %w", err)
	}

	doltEnv := append(os.Environ(), "DOLT_ROOT_PATH="+tmpDir)

	// Configure dolt user identity (required by dolt init).
	for _, args := range [][]string{
		{"dolt", "config", "--global", "--add", "user.name", "beads-test"},
		{"dolt", "config", "--global", "--add", "user.email", "test@beads.local"},
	} {
		cfgCmd := exec.Command(args[0], args[1:]...)
		cfgCmd.Env = doltEnv
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(tmpDir)
			return "", "", nil, fmt.Errorf("%s failed: %v\n%s", args[1], err, out)
		}
	}

	initCmd := exec.Command("dolt", "init")
	initCmd.Dir = dbDir
	initCmd.Env = doltEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", nil, fmt.Errorf("dolt init failed: %v\n%s", err, out)
	}

	return tmpDir, dbDir, doltEnv, nil
}

// CleanStaleTestServers kills orphaned test dolt servers from previous
// interrupted test runs by scanning PID files in /tmp, and removes
// orphaned temp directories left behind by crashed tests.
func CleanStaleTestServers() {
	cleanStalePIDFiles()
	cleanOrphanedTempDirs()
}

// cleanStalePIDFiles scans PID files in /tmp for dead or orphaned test servers.
// Handles both the current prefix (beads-test-dolt-) and the legacy prefix
// (dolt-test-server-) from before the rename.
func cleanStalePIDFiles() {
	prefixes := []string{testPidPrefix, "dolt-test-server-"}
	for _, prefix := range prefixes {
		pattern := filepath.Join(testPidDir, prefix+"*.pid")
		entries, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, pidFile := range entries {
			cleanPIDFile(pidFile)
		}
		// Also clean matching .lock files
		lockPattern := filepath.Join(testPidDir, prefix+"*.lock")
		lockEntries, _ := filepath.Glob(lockPattern)
		for _, lockFile := range lockEntries {
			_ = os.Remove(lockFile)
		}
	}
}

// cleanPIDFile handles a single PID file: removes if dead, kills if alive and is dolt.
func cleanPIDFile(pidFile string) {
	data, err := os.ReadFile(pidFile) //nolint:gosec // G304: path from test cleanup, not user input
	if err != nil {
		_ = os.Remove(pidFile)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(pidFile)
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidFile)
		return
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// Process is dead — clean up stale PID file
		_ = os.Remove(pidFile)
		return
	}
	// Process is alive — verify it's a dolt server before killing
	if isDoltTestProcess(pid) {
		_ = process.Signal(syscall.SIGKILL)
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(pidFile)
}

// cleanOrphanedTempDirs removes test temp directories whose owning server
// process is no longer running. Covers all prefixes used by StartTestDoltServer
// callers and test working dirs in the system temp directory.
func cleanOrphanedTempDirs() {
	tmpDir := os.TempDir()
	for _, prefix := range []string{
		// Server data dirs (one per StartTestDoltServer caller)
		"beads-test-dolt-",
		"beads-root-test-",
		"beads-integration-test-",
		"bd-regression-dolt-",
		"tracker-pkg-test-",
		"dolt-pkg-test-",
		"molecules-pkg-test-",
		"doctor-test-dolt-",
		"fix-test-dolt-",
		"protocol-test-dolt-",
		// Test working dirs
		"beads-bd-tests-",
		// Legacy prefix (no longer created)
		"dolt-test-server-",
	} {
		pattern := filepath.Join(tmpDir, prefix+"*")
		entries, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			info, err := os.Stat(entry)
			if err != nil || !info.IsDir() {
				continue
			}
			// Skip dirs modified in the last 5 minutes (may be in active use)
			if time.Since(info.ModTime()) < 5*time.Minute {
				continue
			}
			// Go module caches have read-only perms; fix before removal
			_ = filepath.Walk(entry, func(path string, fi os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if fi.IsDir() && fi.Mode()&0200 == 0 {
					_ = os.Chmod(path, fi.Mode()|0200)
				}
				return nil
			})
			_ = os.RemoveAll(entry)
		}
	}
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

// LockFilePathForPort returns the lock file path for a given port.
// Port-specific paths prevent contention between test binaries using different ports.
func LockFilePathForPort(port string) string {
	return filepath.Join(testPidDir, fmt.Sprintf("%s%s.lock", testPidPrefix, port))
}

// PidFilePathForPort returns the PID file path for a given port.
func PidFilePathForPort(port string) string {
	return filepath.Join(testPidDir, fmt.Sprintf("%s%s.pid", testPidPrefix, port))
}

// isDoltTestProcess verifies that a PID belongs to a dolt sql-server process.
func isDoltTestProcess(pid int) bool {
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	cmdline := strings.TrimSpace(string(output))
	return strings.Contains(cmdline, "dolt") && strings.Contains(cmdline, "sql-server")
}
