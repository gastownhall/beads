//go:build !windows

package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// startDoltServerInternal uses file locks (flock) for cross-process port
// coordination, eliminating the race condition where FindFreePort() releases
// a socket and another concurrent test process grabs the same port before
// dolt can bind it.
//
// Protocol:
//  1. Pick a port via FindFreePort
//  2. Acquire LOCK_EX on /tmp/beads-test-dolt-<port>.lock
//  3. Under lock: if port is already listening, downgrade to LOCK_SH (reuse)
//  4. Otherwise: start dolt server, wait for ready, downgrade to LOCK_SH
//  5. Hold LOCK_SH for lifetime of test binary (prevents premature cleanup)
//
// See cleanupDoltServerInternal for the shutdown protocol.
func startDoltServerInternal(tmpDirPrefix string) error {
	// Reap zombie test servers from previous crashed test runs.
	reapStaleDoltServers(1 * time.Hour)

	// Pick a port.
	port, err := FindFreePort()
	if err != nil {
		return fmt.Errorf("finding free port: %w", err)
	}
	doltTestPort = strconv.Itoa(port)

	lockPath := LockFilePathForPort(doltTestPort)
	pidPath := PidFilePathForPort(doltTestPort)

	// Open the lock file (kept open for the lifetime of the test binary).
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0666) //nolint:gosec // test infrastructure
	if err != nil {
		return fmt.Errorf("opening lock file %s: %w", lockPath, err)
	}

	// Acquire exclusive lock for the startup phase. If another process is
	// starting a server on the same port, we block here until it's done.
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		_ = lockFile.Close()
		return fmt.Errorf("acquiring startup lock: %w", err)
	}

	// Under the exclusive lock: check if a server is already running on this
	// port (started by another process that held the lock before us).
	if WaitForServer(port, 2*time.Second) {
		// Server already running. Downgrade to shared lock — signals "I'm using it".
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_SH); err != nil {
			_ = lockFile.Close()
			return fmt.Errorf("downgrading to shared lock: %w", err)
		}
		doltLockFile = lockFile
		doltSingletonSrv = &TestDoltServer{Port: port}
		return nil
	}

	// No server running — prepare dolt data directory and start the server.
	tmpDir, dbDir, doltEnv, err := prepareDoltDir(tmpDirPrefix)
	if err != nil {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		return err
	}

	verbose := os.Getenv("BEADS_TEST_DOLT_VERBOSE") == "1"

	serverCmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", doltTestPort,
		"--no-auto-commit",
	)
	serverCmd.Dir = dbDir
	serverCmd.Env = doltEnv
	if !verbose {
		serverCmd.Stderr = nil
		serverCmd.Stdout = nil
	}

	if err := serverCmd.Start(); err != nil {
		_ = os.RemoveAll(tmpDir)
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		return fmt.Errorf("starting dolt sql-server: %w", err)
	}

	// Write PID file so any last-exiting process can clean up.
	// Format: "PID\nTMP_DIR\n" (tmpDir contains dbDir)
	pidContent := fmt.Sprintf("%d\n%s\n", serverCmd.Process.Pid, tmpDir)
	if err := os.WriteFile(pidPath, []byte(pidContent), 0666); err != nil { //nolint:gosec // test infrastructure
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = os.RemoveAll(tmpDir)
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		return fmt.Errorf("writing PID file: %w", err)
	}

	// Create the server struct early so the monitor goroutine can use it.
	srv := &TestDoltServer{
		Port:    port,
		cmd:     serverCmd,
		tmpDir:  tmpDir,
		pidFile: pidPath,
		crashed: make(chan struct{}),
	}

	// Monitor goroutine: detect server exits and populate crashed channel.
	go func() {
		waitErr := serverCmd.Wait()
		srv.exitOnce.Do(func() {
			srv.exitErr = waitErr
			close(srv.crashed)
			fmt.Fprintf(os.Stderr, "WARN: test dolt server (port %d) exited: %v\n", port, waitErr)
		})
	}()

	// Wait for server to accept connections.
	deadline := time.Now().Add(serverStartTimeout)
	for time.Now().Before(deadline) {
		if WaitForServer(port, time.Second) {
			// Server is ready. Downgrade to shared lock.
			if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_SH); err != nil {
				_ = lockFile.Close()
				return fmt.Errorf("downgrading to shared lock: %w", err)
			}
			doltLockFile = lockFile
			doltWeStarted = true

			// Install signal handler so cleanup runs even when defer doesn't
			// (e.g. Ctrl+C during test run).
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				cleanupDoltServerInternal()
				os.Exit(1)
			}()

			doltSingletonSrv = srv
			return nil
		}

		// Check if process exited (port bind failure, etc).
		select {
		case <-srv.crashed:
			_ = os.Remove(pidPath)
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
			return fmt.Errorf("dolt sql-server exited prematurely: %v", srv.exitErr)
		default:
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Timed out — kill and clean up.
	srv.cleanup()
	_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	_ = lockFile.Close()
	return fmt.Errorf("dolt sql-server did not become ready within %s", serverStartTimeout)
}

// cleanupMu serializes cleanup calls within the same process.
var cleanupMu sync.Mutex

// cleanupDoltServerInternal uses the flock shutdown protocol:
//
//  1. Try to upgrade from LOCK_SH to LOCK_EX (non-blocking).
//  2. If LOCK_EX succeeds: no other process holds LOCK_SH, so we're the last
//     user. Read the PID file and kill the server.
//  3. If LOCK_EX fails (EWOULDBLOCK): another process still holds LOCK_SH.
//     Skip cleanup — the last process to exit will handle it.
func cleanupDoltServerInternal() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()

	defer func() {
		if doltLockFile != nil {
			_ = syscall.Flock(int(doltLockFile.Fd()), syscall.LOCK_UN)
			_ = doltLockFile.Close()
			doltLockFile = nil
		}
		if doltPortSetByUs {
			_ = os.Unsetenv("BEADS_DOLT_PORT")
		}
	}()

	if doltLockFile == nil || doltTestPort == "" {
		// No lock held — just do basic cleanup on the singleton if we own it.
		if doltSingletonSrv != nil && doltWeStarted {
			doltSingletonSrv.cleanup()
		}
		return
	}

	pidPath := PidFilePathForPort(doltTestPort)

	// Try to acquire exclusive lock (non-blocking). If another process
	// holds LOCK_SH, this fails with EWOULDBLOCK — the server is still in use.
	err := syscall.Flock(int(doltLockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		// Another process is using the server. Don't kill it.
		return
	}
	// We got LOCK_EX — we're the last process. Kill the server.

	if doltSingletonSrv != nil && doltSingletonSrv.cmd != nil {
		// We own the process — use TestDoltServer.cleanup() which handles
		// kill, wait, and file removal properly.
		doltSingletonSrv.cleanup()
	} else {
		// We reused a server started by another process — kill via PID file.
		killFromPIDFile(pidPath)
	}

	_ = os.Remove(LockFilePathForPort(doltTestPort))
}

// killFromPIDFile reads a PID file and kills the server process.
// Used when we reused a server started by another process (no cmd handle).
func killFromPIDFile(pidPath string) {
	data, err := os.ReadFile(pidPath) //nolint:gosec // G304: test infrastructure path
	if err != nil {
		return
	}

	lines := strings.SplitN(string(data), "\n", 3)
	if len(lines) < 1 {
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return
	}

	// Kill the server process.
	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Kill()
		_, _ = proc.Wait()
	}

	// Clean up data directory from PID file.
	if len(lines) >= 2 {
		tmpDir := strings.TrimSpace(lines[1])
		if tmpDir != "" {
			_ = os.RemoveAll(tmpDir)
		}
	}

	_ = os.Remove(pidPath)
}

// reapStaleDoltServers finds and kills dolt sql-server processes that:
//   - Have a --data-dir or working directory containing test prefixes
//   - Have been running for longer than maxAge
//
// This prevents zombie test servers from accumulating when test processes
// are SIGKILL'd (e.g., go test -timeout expiration) and cleanup never runs.
func reapStaleDoltServers(maxAge time.Duration) {
	// Use ps to find dolt sql-server processes.
	// Format: PID ELAPSED ARGS
	out, err := exec.Command("ps", "-eo", "pid,etime,args").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "dolt sql-server") {
			continue
		}
		// Only kill test servers, not production (port 3307)
		if strings.Contains(line, "-P 3307") || strings.Contains(line, "--port 3307") {
			continue
		}
		// Must look like a test server (temp dir patterns)
		isTest := false
		for _, marker := range []string{"beads-test-dolt", "dolt-test-server", "beads-root-test", "beads-integration-test"} {
			if strings.Contains(line, marker) {
				isTest = true
				break
			}
		}
		if !isTest {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		elapsed := parseElapsed(fields[1])
		if elapsed < maxAge {
			continue
		}
		// Kill the stale test server
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	}
}

// parseElapsed converts ps etime format (HH:MM:SS or MM:SS or DD-HH:MM:SS) to duration.
func parseElapsed(s string) time.Duration {
	var days, hours, mins, secs int

	// Handle DD-HH:MM:SS format
	if idx := strings.Index(s, "-"); idx >= 0 {
		_, _ = fmt.Sscanf(s[:idx], "%d", &days)
		s = s[idx+1:]
	}

	parts := strings.Split(s, ":")
	switch len(parts) {
	case 3:
		_, _ = fmt.Sscanf(parts[0], "%d", &hours)
		_, _ = fmt.Sscanf(parts[1], "%d", &mins)
		_, _ = fmt.Sscanf(parts[2], "%d", &secs)
	case 2:
		_, _ = fmt.Sscanf(parts[0], "%d", &mins)
		_, _ = fmt.Sscanf(parts[1], "%d", &secs)
	}

	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second
}
