package testutil

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
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

// maxPortRetries is how many times to retry port allocation + server start on port conflict.
const maxPortRetries = 3

// StartTestDoltServer starts a dedicated Dolt SQL server in a temp directory
// on a dynamic port. Cleans up stale test servers first. Installs a signal
// handler so cleanup runs even when tests are interrupted with Ctrl+C.
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
	if port := os.Getenv("BEADS_DOLT_PORT"); port != "" {
		p, err := strconv.Atoi(port)
		if err == nil && WaitForServer(p, 2*time.Second) {
			return &TestDoltServer{Port: p}, func() {}
		}
		fmt.Fprintf(os.Stderr, "WARN: BEADS_DOLT_PORT=%s set but server not reachable, starting new server\n", port)
	}

	CleanStaleTestServers()

	if _, err := exec.LookPath("dolt"); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: dolt not found in PATH, skipping test server\n")
		return nil, func() {}
	}

	tmpDir, err := os.MkdirTemp("", tmpDirPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to create test dolt dir: %v\n", err)
		return nil, func() {}
	}

	dbDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to create test dolt data dir: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		return nil, func() {}
	}

	// Configure dolt user identity (required by dolt init).
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
			return nil, func() {}
		}
	}

	initCmd := exec.Command("dolt", "init")
	initCmd.Dir = dbDir
	initCmd.Env = doltEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: dolt init failed for test server: %v\n%s\n", err, out)
		_ = os.RemoveAll(tmpDir)
		return nil, func() {}
	}

	// Retry loop: FindFreePort releases the socket before dolt binds it,
	// creating a race window where another process can grab the port.
	var serverCmd *exec.Cmd
	var port int
	var pidFile string
	verbose := os.Getenv("BEADS_TEST_DOLT_VERBOSE") == "1"

	for attempt := 0; attempt < maxPortRetries; attempt++ {
		port, err = FindFreePort()
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: failed to find free port (attempt %d/%d): %v\n", attempt+1, maxPortRetries, err)
			continue
		}

		serverCmd = exec.Command("dolt", "sql-server",
			"-H", "127.0.0.1",
			"-P", fmt.Sprintf("%d", port),
			"--no-auto-commit",
		)
		serverCmd.Dir = dbDir
		serverCmd.Env = doltEnv
		if !verbose {
			serverCmd.Stderr = nil
			serverCmd.Stdout = nil
		}
		if err = serverCmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: failed to start test dolt server on port %d (attempt %d/%d): %v\n", port, attempt+1, maxPortRetries, err)
			continue
		}

		// Write PID file so stale cleanup can find orphans from interrupted runs
		pidFile = filepath.Join(testPidDir, fmt.Sprintf("%s%d.pid", testPidPrefix, port))
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(serverCmd.Process.Pid)), 0600)

		if WaitForServer(port, serverStartTimeout) {
			break // Server is ready
		}

		// Server failed to become ready — clean up this attempt and retry
		fmt.Fprintf(os.Stderr, "WARN: test dolt server did not become ready on port %d (attempt %d/%d)\n", port, attempt+1, maxPortRetries)
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = os.Remove(pidFile)
		serverCmd = nil
	}

	if serverCmd == nil {
		fmt.Fprintf(os.Stderr, "WARN: test dolt server failed to start after %d attempts, tests requiring dolt will be skipped\n", maxPortRetries)
		_ = os.RemoveAll(tmpDir)
		return nil, func() {}
	}

	srv := &TestDoltServer{
		Port:    port,
		cmd:     serverCmd,
		tmpDir:  tmpDir,
		pidFile: pidFile,
		crashed: make(chan struct{}),
	}

	// Monitor goroutine: detect unexpected server exits
	go func() {
		err := serverCmd.Wait()
		srv.exitOnce.Do(func() {
			srv.exitErr = err
			close(srv.crashed)
			fmt.Fprintf(os.Stderr, "WARN: test dolt server (port %d) exited: %v\n", port, err)
		})
	}()

	// Install signal handler so cleanup runs even when defer doesn't
	// (e.g. Ctrl+C during test run)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		srv.cleanup()
		os.Exit(1)
	}()

	cleanup := func() {
		signal.Stop(sigCh)
		srv.cleanup()
	}

	return srv, cleanup
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
