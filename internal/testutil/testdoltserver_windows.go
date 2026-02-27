//go:build windows

package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// startDoltServerInternal is the Windows fallback without file locking.
// On Windows, syscall.Flock is not available, so we use the older retry-based
// approach. This is racier than the Unix flock approach but provides basic
// functionality.
func startDoltServerInternal(tmpDirPrefix string) error {
	port, err := FindFreePort()
	if err != nil {
		return fmt.Errorf("finding free port: %w", err)
	}
	doltTestPort = strconv.Itoa(port)

	pidPath := PidFilePathForPort(doltTestPort)

	// Check if a server is already running on the port.
	if WaitForServer(port, 2*time.Second) {
		doltSingletonSrv = &TestDoltServer{Port: port}
		return nil
	}

	tmpDir, dbDir, doltEnv, err := prepareDoltDir(tmpDirPrefix)
	if err != nil {
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
		return fmt.Errorf("starting dolt sql-server: %w", err)
	}

	// Write PID file.
	pidContent := fmt.Sprintf("%d\n%s\n", serverCmd.Process.Pid, tmpDir)
	if err := os.WriteFile(pidPath, []byte(pidContent), 0666); err != nil { //nolint:gosec // test infrastructure
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("writing PID file: %w", err)
	}

	// Reap in background.
	exited := make(chan struct{})
	go func() {
		_ = serverCmd.Wait()
		close(exited)
	}()

	// Wait for server to accept connections.
	deadline := time.Now().Add(serverStartTimeout)
	for time.Now().Before(deadline) {
		if WaitForServer(port, time.Second) {
			doltWeStarted = true

			srv := &TestDoltServer{
				Port:    port,
				cmd:     serverCmd,
				tmpDir:  tmpDir,
				pidFile: pidPath,
				crashed: make(chan struct{}),
			}

			go func() {
				<-exited
				srv.exitOnce.Do(func() {
					srv.exitErr = fmt.Errorf("server exited unexpectedly")
					close(srv.crashed)
					fmt.Fprintf(os.Stderr, "WARN: test dolt server (port %d) exited unexpectedly\n", port)
				})
			}()

			doltSingletonSrv = srv
			return nil
		}

		select {
		case <-exited:
			_ = os.RemoveAll(tmpDir)
			_ = os.Remove(pidPath)
			return fmt.Errorf("dolt sql-server exited prematurely")
		default:
		}
		time.Sleep(500 * time.Millisecond)
	}

	_ = serverCmd.Process.Kill()
	<-exited
	_ = os.RemoveAll(tmpDir)
	_ = os.Remove(pidPath)
	return fmt.Errorf("dolt sql-server did not become ready within %s", serverStartTimeout)
}

// cleanupMu serializes cleanup calls within the same process.
var cleanupMu sync.Mutex

// cleanupDoltServerInternal is the Windows fallback cleanup.
// Without flock, we simply kill the server if we started it.
func cleanupDoltServerInternal() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()

	defer func() {
		if doltPortSetByUs {
			_ = os.Unsetenv("BEADS_DOLT_PORT")
		}
	}()

	if doltSingletonSrv == nil || !doltWeStarted {
		return
	}

	pidPath := PidFilePathForPort(doltTestPort)

	data, err := os.ReadFile(pidPath) //nolint:gosec // G304: test infrastructure path
	if err != nil {
		doltSingletonSrv.cleanup()
		return
	}

	lines := strings.SplitN(string(data), "\n", 3)
	if len(lines) < 1 {
		doltSingletonSrv.cleanup()
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		doltSingletonSrv.cleanup()
		return
	}

	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Kill()
		_, _ = proc.Wait()
	}

	if len(lines) >= 2 {
		tmpDir := strings.TrimSpace(lines[1])
		if tmpDir != "" {
			_ = os.RemoveAll(tmpDir)
		}
	}

	_ = os.Remove(pidPath)
}

// reapStaleDoltServers is a no-op on Windows (ps -eo not available).
func reapStaleDoltServers(_ time.Duration) {}
