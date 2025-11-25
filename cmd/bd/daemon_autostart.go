package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/rpc"
)

// Daemon start failure tracking for exponential backoff
var (
	lastDaemonStartAttempt time.Time
	daemonStartFailures    int
)

// shouldAutoStartDaemon checks if daemon auto-start is enabled
func shouldAutoStartDaemon() bool {
	// Check BEADS_NO_DAEMON first (escape hatch for single-user workflows)
	noDaemon := strings.ToLower(strings.TrimSpace(os.Getenv("BEADS_NO_DAEMON")))
	if noDaemon == "1" || noDaemon == "true" || noDaemon == "yes" || noDaemon == "on" {
		return false // Explicit opt-out
	}

	// Use viper to read from config file or BEADS_AUTO_START_DAEMON env var
	// Viper handles BEADS_AUTO_START_DAEMON automatically via BindEnv
	return config.GetBool("auto-start-daemon") // Defaults to true
}


// restartDaemonForVersionMismatch stops the old daemon and starts a new one
// Returns true if restart was successful
func restartDaemonForVersionMismatch() bool {
	pidFile, err := getPIDFilePath()
	if err != nil {
		debug.Logf("failed to get PID file path: %v", err)
		return false
	}

	socketPath := getSocketPath()

	// Check if daemon is running and stop it
	forcedKill := false
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		debug.Logf("stopping old daemon (PID %d)", pid)

		process, err := os.FindProcess(pid)
		if err != nil {
			debug.Logf("failed to find process: %v", err)
			return false
		}

		// Send stop signal
		if err := sendStopSignal(process); err != nil {
			debug.Logf("failed to signal daemon: %v", err)
			return false
		}

		// Wait for daemon to stop (up to 5 seconds)
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
				debug.Logf("old daemon stopped successfully")
				break
			}
		}

		// Force kill if still running
		if isRunning, _ := isDaemonRunning(pidFile); isRunning {
			debug.Logf("force killing old daemon")
			_ = process.Kill()
			forcedKill = true
		}
	}

	// Clean up stale socket and PID file after force kill or if not running
	if forcedKill || !isDaemonRunningQuiet(pidFile) {
		_ = os.Remove(socketPath)
		_ = os.Remove(pidFile)
	}

	// Start new daemon with current binary version
	exe, err := os.Executable()
	if err != nil {
		debug.Logf("failed to get executable path: %v", err)
		return false
	}

	args := []string{"daemon", "--start"}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "BD_DAEMON_FOREGROUND=1")

	// Set working directory to database directory so daemon finds correct DB
	if dbPath != "" {
		cmd.Dir = filepath.Dir(dbPath)
	}

	configureDaemonProcess(cmd)

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		defer func() { _ = devNull.Close() }()
	}

	if err := cmd.Start(); err != nil {
		debug.Logf("failed to start new daemon: %v", err)
		return false
	}

	// Reap the process to avoid zombies
	go func() { _ = cmd.Wait() }()

	// Wait for daemon to be ready using shared helper
	if waitForSocketReadiness(socketPath, 5*time.Second) {
		debug.Logf("new daemon started successfully")
		return true
	}

	debug.Logf("new daemon failed to become ready")
	return false
}

// isDaemonRunningQuiet checks if daemon is running without output
func isDaemonRunningQuiet(pidFile string) bool {
	isRunning, _ := isDaemonRunning(pidFile)
	return isRunning
}

// tryAutoStartDaemon attempts to start the daemon in the background
// Returns true if daemon was started successfully and socket is ready
func tryAutoStartDaemon(socketPath string) bool {
	if !canRetryDaemonStart() {
		debugLog("skipping auto-start due to recent failures")
		return false
	}

	if isDaemonHealthy(socketPath) {
		debugLog("daemon already running and healthy")
		return true
	}

	lockPath := socketPath + ".startlock"
	if !acquireStartLock(lockPath, socketPath) {
		return false
	}
	defer func() {
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			debugLog("failed to remove lock file: %v", err)
		}
	}()

	if handleExistingSocket(socketPath) {
		return true
	}

	socketPath = determineSocketPath(socketPath)
	return startDaemonProcess(socketPath)
}

func debugLog(msg string, args ...interface{}) {
	debug.Logf(msg, args...)
}

func isDaemonHealthy(socketPath string) bool {
	client, err := rpc.TryConnect(socketPath)
	if err == nil && client != nil {
		_ = client.Close()
		return true
	}
	return false
}

func acquireStartLock(lockPath, socketPath string) bool {
	// nolint:gosec // G304: lockPath is derived from secure beads directory
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		debugLog("another process is starting daemon, waiting for readiness")
		if waitForSocketReadiness(socketPath, 5*time.Second) {
			return true
		}
		return handleStaleLock(lockPath, socketPath)
	}

	_, _ = fmt.Fprintf(lockFile, "%d\n", os.Getpid())
	_ = lockFile.Close()
	return true
}

func handleStaleLock(lockPath, socketPath string) bool {
	lockPID, err := readPIDFromFile(lockPath)
	if err == nil && !isPIDAlive(lockPID) {
		debugLog("lock is stale (PID %d dead), removing and retrying", lockPID)
		_ = os.Remove(lockPath)
		return tryAutoStartDaemon(socketPath)
	}
	return false
}

func handleExistingSocket(socketPath string) bool {
	if _, err := os.Stat(socketPath); err != nil {
		return false
	}

	if canDialSocket(socketPath, 200*time.Millisecond) {
		debugLog("daemon started by another process")
		return true
	}

	pidFile := getPIDFileForSocket(socketPath)
	if pidFile != "" {
		if pid, err := readPIDFromFile(pidFile); err == nil && isPIDAlive(pid) {
			debugLog("daemon PID %d alive, waiting for socket", pid)
			return waitForSocketReadiness(socketPath, 5*time.Second)
		}
	}

	debugLog("socket is stale, cleaning up")
	_ = os.Remove(socketPath)
	if pidFile != "" {
		_ = os.Remove(pidFile)
	}
	return false
}

func determineSocketPath(socketPath string) string {
	return socketPath
}

func startDaemonProcess(socketPath string) bool {
	binPath, err := os.Executable()
	if err != nil {
		binPath = os.Args[0]
	}

	args := []string{"daemon", "--start"}

	cmd := exec.Command(binPath, args...)
	setupDaemonIO(cmd)

	if dbPath != "" {
		cmd.Dir = filepath.Dir(dbPath)
	}

	configureDaemonProcess(cmd)
	if err := cmd.Start(); err != nil {
		recordDaemonStartFailure()
		debugLog("failed to start daemon: %v", err)
		return false
	}

	go func() { _ = cmd.Wait() }()

	if waitForSocketReadiness(socketPath, 5*time.Second) {
		recordDaemonStartSuccess()
		return true
	}

	recordDaemonStartFailure()
	debugLog("daemon socket not ready after 5 seconds")
	return false
}

func setupDaemonIO(cmd *exec.Cmd) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.Stdin = devNull
		go func() {
			time.Sleep(1 * time.Second)
			_ = devNull.Close()
		}()
	}
}

// getPIDFileForSocket returns the PID file path for a given socket path
func getPIDFileForSocket(socketPath string) string {
	// PID file is in same directory as socket, named daemon.pid
	dir := filepath.Dir(socketPath)
	return filepath.Join(dir, "daemon.pid")
}

// readPIDFromFile reads a PID from a file
func readPIDFromFile(path string) (int, error) {
	// nolint:gosec // G304: path is derived from secure beads directory
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// isPIDAlive checks if a process with the given PID is running
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return isProcessRunning(pid)
}

// canDialSocket attempts a quick dial to the socket with a timeout
func canDialSocket(socketPath string, timeout time.Duration) bool {
	client, err := rpc.TryConnectWithTimeout(socketPath, timeout)
	if err != nil || client == nil {
		return false
	}
	_ = client.Close()
	return true
}

// waitForSocketReadiness waits for daemon socket to be ready by testing actual connections
//
//nolint:unparam // timeout is configurable even though current callers use 5s
func waitForSocketReadiness(socketPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if canDialSocket(socketPath, 200*time.Millisecond) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func canRetryDaemonStart() bool {
	if daemonStartFailures == 0 {
		return true
	}

	// Exponential backoff: 5s, 10s, 20s, 40s, 80s, 120s (capped at 120s)
	backoff := time.Duration(5*(1<<uint(daemonStartFailures-1))) * time.Second
	if backoff > 120*time.Second {
		backoff = 120 * time.Second
	}

	return time.Since(lastDaemonStartAttempt) > backoff
}

func recordDaemonStartSuccess() {
	daemonStartFailures = 0
}

func recordDaemonStartFailure() {
	lastDaemonStartAttempt = time.Now()
	daemonStartFailures++
	// No cap needed - backoff is capped at 120s in canRetryDaemonStart
}

// getSocketPath returns the daemon socket path based on the database location
// Returns local socket path (.beads/bd.sock relative to database)
func getSocketPath() string {
	return filepath.Join(filepath.Dir(dbPath), "bd.sock")
}

// emitVerboseWarning prints a one-line warning when falling back to direct mode
func emitVerboseWarning() {
	switch daemonStatus.FallbackReason {
	case FallbackConnectFailed:
		fmt.Fprintf(os.Stderr, "Warning: Daemon unreachable at %s. Running in direct mode. Hint: bd daemon --status\n", daemonStatus.SocketPath)
	case FallbackHealthFailed:
		fmt.Fprintf(os.Stderr, "Warning: Daemon unhealthy. Falling back to direct mode. Hint: bd daemon --health\n")
	case FallbackAutoStartDisabled:
		fmt.Fprintf(os.Stderr, "Warning: Auto-start disabled (BEADS_AUTO_START_DAEMON=false). Running in direct mode. Hint: bd daemon\n")
	case FallbackAutoStartFailed:
		fmt.Fprintf(os.Stderr, "Warning: Failed to auto-start daemon. Running in direct mode. Hint: bd daemon --status\n")
	case FallbackDaemonUnsupported:
		fmt.Fprintf(os.Stderr, "Warning: Daemon does not support this command yet. Running in direct mode. Hint: update daemon or use local mode.\n")
	case FallbackFlagNoDaemon:
		// Don't warn when user explicitly requested --no-daemon
		return
	}
}

func getDebounceDuration() time.Duration {
	duration := config.GetDuration("flush-debounce")
	if duration == 0 {
		// If parsing failed, use default
		return 5 * time.Second
	}
	return duration
}
