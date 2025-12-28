package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/rpc"
)

// DaemonStatusResponse is returned for daemon status check
type DaemonStatusResponse struct {
	Running      bool   `json:"running"`
	PID          int    `json:"pid,omitempty"`
	Started      string `json:"started,omitempty"`
	LogPath      string `json:"log_path,omitempty"`
	AutoCommit   bool   `json:"auto_commit,omitempty"`
	AutoPush     bool   `json:"auto_push,omitempty"`
	LocalMode    bool   `json:"local_mode,omitempty"`
	SyncInterval string `json:"sync_interval,omitempty"`
	DaemonMode   string `json:"daemon_mode,omitempty"`
}

// isDaemonRunning checks if the daemon is currently running
func isDaemonRunning(pidFile string) (bool, int) {
	beadsDir := filepath.Dir(pidFile)
	return tryDaemonLock(beadsDir)
}

// formatUptime formats uptime seconds into a human-readable string
func formatUptime(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.1f seconds", seconds)
	}
	if seconds < 3600 {
		minutes := int(seconds / 60)
		secs := int(seconds) % 60
		return fmt.Sprintf("%dm %ds", minutes, secs)
	}
	if seconds < 86400 {
		hours := int(seconds / 3600)
		minutes := int(seconds/60) % 60
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	days := int(seconds / 86400)
	hours := int(seconds/3600) % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}

// showDaemonStatus displays the current daemon status
func showDaemonStatus(pidFile string) {
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		var started string
		if info, err := os.Stat(pidFile); err == nil {
			started = info.ModTime().Format("2006-01-02 15:04:05")
		}

		var logPath string
		if lp, err := getLogFilePath(""); err == nil {
			if _, err := os.Stat(lp); err == nil {
				logPath = lp
			}
		}

		// Try to get detailed status from daemon via RPC
		var rpcStatus *rpc.StatusResponse
		beadsDir := filepath.Dir(pidFile)
		socketPath := filepath.Join(beadsDir, "bd.sock")
		if client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second); err == nil && client != nil {
			if status, err := client.Status(); err == nil {
				rpcStatus = status
			}
			_ = client.Close()
		}

		if jsonOutput {
			status := DaemonStatusResponse{
				Running: true,
				PID:     pid,
				Started: started,
				LogPath: logPath,
			}
			// Add config from RPC status if available
			if rpcStatus != nil {
				status.AutoCommit = rpcStatus.AutoCommit
				status.AutoPush = rpcStatus.AutoPush
				status.LocalMode = rpcStatus.LocalMode
				status.SyncInterval = rpcStatus.SyncInterval
				status.DaemonMode = rpcStatus.DaemonMode
			}
			outputJSON(status)
			return
		}

		fmt.Printf("Daemon is running (PID %d)\n", pid)
		if started != "" {
			fmt.Printf("  Started: %s\n", started)
		}
		if logPath != "" {
			fmt.Printf("  Log: %s\n", logPath)
		}
		// Display config from RPC status if available
		if rpcStatus != nil {
			fmt.Printf("  Mode: %s\n", rpcStatus.DaemonMode)
			fmt.Printf("  Sync Interval: %s\n", rpcStatus.SyncInterval)
			fmt.Printf("  Auto-Commit: %v\n", rpcStatus.AutoCommit)
			fmt.Printf("  Auto-Push: %v\n", rpcStatus.AutoPush)
			fmt.Printf("  Auto-Pull: %v\n", rpcStatus.AutoPull)
			if rpcStatus.LocalMode {
				fmt.Printf("  Local Mode: %v (no git sync)\n", rpcStatus.LocalMode)
			}
		}
	} else {
		if jsonOutput {
			outputJSON(DaemonStatusResponse{Running: false})
			return
		}
		fmt.Println("Daemon is not running")
	}
}

// showDaemonHealth displays daemon health information
func showDaemonHealth() {
	beadsDir, err := ensureBeadsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	socketPath := filepath.Join(beadsDir, "bd.sock")

	client, err := rpc.TryConnect(socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}

	if client == nil {
		// Check if lock is held to provide better diagnostic message
		beadsDir := filepath.Dir(socketPath)
		running, _ := tryDaemonLock(beadsDir)
		if running {
			fmt.Println("Daemon lock is held but connection failed")
			fmt.Println("This may indicate a crashed daemon. Try: bd daemons killall")
		} else {
			fmt.Println("Daemon is not running")
			fmt.Println("Start with: bd daemon start")
		}
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()

	health, err := client.Health()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking health: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(health, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Printf("Daemon Health: %s\n", strings.ToUpper(health.Status))

	fmt.Printf("  Version: %s\n", health.Version)
	fmt.Printf("  Uptime: %s\n", formatUptime(health.Uptime))
	fmt.Printf("  DB Response Time: %.2f ms\n", health.DBResponseTime)

	if health.Error != "" {
		fmt.Printf("  Error: %s\n", health.Error)
	}

	if health.Status == "unhealthy" {
		os.Exit(1)
	}
}

// showDaemonMetrics displays daemon metrics
func showDaemonMetrics() {
	beadsDir, err := ensureBeadsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	socketPath := filepath.Join(beadsDir, "bd.sock")

	client, err := rpc.TryConnect(socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}

	if client == nil {
		fmt.Println("Daemon is not running")
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()

	metrics, err := client.Metrics()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching metrics: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(metrics, "", "  ")
		fmt.Println(string(data))
		return
	}

	// Human-readable output
	fmt.Printf("Daemon Metrics\n")
	fmt.Printf("==============\n\n")

	fmt.Printf("Uptime: %.1f seconds (%.1f minutes)\n", metrics.UptimeSeconds, metrics.UptimeSeconds/60)
	fmt.Printf("Timestamp: %s\n\n", metrics.Timestamp.Format(time.RFC3339))

	// Connection metrics
	fmt.Printf("Connection Metrics:\n")
	fmt.Printf("  Total: %d\n", metrics.TotalConns)
	fmt.Printf("  Active: %d\n", metrics.ActiveConns)
	fmt.Printf("  Rejected: %d\n\n", metrics.RejectedConns)

	// System metrics
	fmt.Printf("System Metrics:\n")
	fmt.Printf("  Memory Alloc: %d MB\n", metrics.MemoryAllocMB)
	fmt.Printf("  Memory Sys: %d MB\n", metrics.MemorySysMB)
	fmt.Printf("  Goroutines: %d\n\n", metrics.GoroutineCount)

	// Operation metrics
	if len(metrics.Operations) > 0 {
		fmt.Printf("Operation Metrics:\n")
		for _, op := range metrics.Operations {
			fmt.Printf("\n  %s:\n", op.Operation)
			fmt.Printf("    Total Requests: %d\n", op.TotalCount)
			fmt.Printf("    Successful: %d\n", op.SuccessCount)
			fmt.Printf("    Errors: %d\n", op.ErrorCount)

			if op.Latency.AvgMS > 0 {
				fmt.Printf("    Latency:\n")
				fmt.Printf("      Min: %.3f ms\n", op.Latency.MinMS)
				fmt.Printf("      Avg: %.3f ms\n", op.Latency.AvgMS)
				fmt.Printf("      P50: %.3f ms\n", op.Latency.P50MS)
				fmt.Printf("      P95: %.3f ms\n", op.Latency.P95MS)
				fmt.Printf("      P99: %.3f ms\n", op.Latency.P99MS)
				fmt.Printf("      Max: %.3f ms\n", op.Latency.MaxMS)
			}
		}
	}
}

// stopDaemon stops a running daemon
func stopDaemon(pidFile string) {
	isRunning, pid := isDaemonRunning(pidFile)
	if !isRunning {
		fmt.Println("Daemon is not running")
		return
	}

	fmt.Printf("Stopping daemon (PID %d)...\n", pid)

	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding process: %v\n", err)
		os.Exit(1)
	}

	if err := sendStopSignal(process); err != nil {
		fmt.Fprintf(os.Stderr, "Error signaling daemon: %v\n", err)
		os.Exit(1)
	}

	for i := 0; i < daemonShutdownAttempts; i++ {
		time.Sleep(daemonShutdownPollInterval)
		if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
			fmt.Println("Daemon stopped")
			return
		}
	}

	fmt.Fprintf(os.Stderr, "Warning: daemon did not stop after %v, forcing termination\n", daemonShutdownTimeout)

	// Check one more time before killing the process to avoid a race.
	if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
		fmt.Println("Daemon stopped")
		return
	}
	
	socketPath := getSocketPathForPID(pidFile)
	
	if err := process.Kill(); err != nil {
		// Ignore "process already finished" errors
		if !strings.Contains(err.Error(), "process already finished") {
			fmt.Fprintf(os.Stderr, "Error killing process: %v\n", err)
		}
	}
	
	// Clean up stale artifacts after forced kill
	_ = os.Remove(pidFile) // Best-effort cleanup, file may not exist
	
	// Also remove socket file if it exists
	if _, err := os.Stat(socketPath); err == nil {
		if err := os.Remove(socketPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove stale socket: %v\n", err)
		}
	}
	
	fmt.Println("Daemon killed")
}

// stopAllDaemons stops all running bd daemons
func stopAllDaemons() {
	// Discover all running daemons using the registry
	daemons, err := daemon.DiscoverDaemons(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering daemons: %v\n", err)
		os.Exit(1)
	}

	// Filter to only alive daemons
	var alive []daemon.DaemonInfo
	for _, d := range daemons {
		if d.Alive {
			alive = append(alive, d)
		}
	}

	if len(alive) == 0 {
		if jsonOutput {
			fmt.Println(`{"stopped": 0, "message": "No running daemons found"}`)
		} else {
			fmt.Println("No running daemons found")
		}
		return
	}

	if !jsonOutput {
		fmt.Printf("Found %d running daemon(s), stopping...\n", len(alive))
	}

	// Stop all daemons (with force=true for stubborn processes)
	results := daemon.KillAllDaemons(alive, true)

	if jsonOutput {
		output, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(output))
	} else {
		if results.Stopped > 0 {
			fmt.Printf("✓ Stopped %d daemon(s)\n", results.Stopped)
		}
		if results.Failed > 0 {
			fmt.Printf("✗ Failed to stop %d daemon(s):\n", results.Failed)
			for _, f := range results.Failures {
				fmt.Printf("  - PID %d (%s): %s\n", f.PID, f.Workspace, f.Error)
			}
		}
	}

	if results.Failed > 0 {
		os.Exit(1)
	}
}

// startDaemon starts the daemon (in foreground if requested, otherwise background)
func startDaemon(interval time.Duration, autoCommit, autoPush, autoPull, localMode, foreground bool, logFile, pidFile, logLevel string, logJSON bool) {
	logPath, err := getLogFilePath(logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Run in foreground if --foreground flag set or if we're the forked child process
	if foreground || os.Getenv("BD_DAEMON_FOREGROUND") == "1" {
		runDaemonLoop(interval, autoCommit, autoPush, autoPull, localMode, logPath, pidFile, logLevel, logJSON)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot resolve executable path: %v\n", err)
		os.Exit(1)
	}

	args := []string{"daemon", "--start",
		"--interval", interval.String(),
	}
	if autoCommit {
		args = append(args, "--auto-commit")
	}
	if autoPush {
		args = append(args, "--auto-push")
	}
	if autoPull {
		args = append(args, "--auto-pull")
	}
	if localMode {
		args = append(args, "--local")
	}
	if logFile != "" {
		args = append(args, "--log", logFile)
	}
	if logLevel != "" && logLevel != "info" {
		args = append(args, "--log-level", logLevel)
	}
	if logJSON {
		args = append(args, "--log-json")
	}

	cmd := exec.Command(exe, args...) // #nosec G204 - bd daemon command from trusted binary
	cmd.Env = append(os.Environ(), "BD_DAEMON_FOREGROUND=1")
	configureDaemonProcess(cmd)

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening /dev/null: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = devNull.Close() }()

	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
		os.Exit(1)
	}

	expectedPID := cmd.Process.Pid

	if err := cmd.Process.Release(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to release process: %v\n", err)
	}

	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		// #nosec G304 - controlled path from config
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid == expectedPID {
				fmt.Printf("Daemon started (PID %d)\n", expectedPID)
				return
			}
		}
	}

	fmt.Fprintf(os.Stderr, "Warning: daemon may have failed to start (PID file not confirmed)\n")
	fmt.Fprintf(os.Stderr, "Check log file: %s\n", logPath)
}

// setupDaemonLock acquires the daemon lock and writes PID file
func setupDaemonLock(pidFile string, dbPath string, log daemonLogger) (*DaemonLock, error) {
	beadsDir := filepath.Dir(pidFile)
	
	// Detect nested .beads directories (e.g., .beads/.beads/.beads/)
	cleanPath := filepath.Clean(beadsDir)
	if strings.Contains(cleanPath, string(filepath.Separator)+".beads"+string(filepath.Separator)+".beads") {
		log.Error("nested .beads directory detected", "path", cleanPath)
		log.Info("hint: do not run 'bd daemon' from inside .beads/ directory")
		log.Info("hint: use absolute paths for BEADS_DB or run from workspace root")
		return nil, fmt.Errorf("nested .beads directory detected")
	}
	
	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		if err == ErrDaemonLocked {
			log.Info("daemon already running (lock held), exiting")
		} else {
			log.Error("acquiring daemon lock", "error", err)
		}
		return nil, err
	}

	myPID := os.Getpid()
	// #nosec G304 - controlled path from config
	if data, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid == myPID {
			// PID file is correct, continue
		} else {
			log.Warn("PID file has wrong PID, overwriting", "expected", myPID, "got", pid)
			_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", myPID)), 0600)
		}
	} else {
		log.Info("PID file missing after lock acquisition, creating")
		_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", myPID)), 0600)
	}

	return lock, nil
}
