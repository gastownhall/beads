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

	"github.com/steveyegge/beads/internal/rpc"
)

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
func showDaemonStatus(pidFile string, global bool) {
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		scope := "local"
		if global {
			scope = "global"
		}
		
		var started string
		if info, err := os.Stat(pidFile); err == nil {
			started = info.ModTime().Format("2006-01-02 15:04:05")
		}

		var logPath string
		if lp, err := getLogFilePath("", global); err == nil {
			if _, err := os.Stat(lp); err == nil {
				logPath = lp
			}
		}

		if jsonOutput {
			status := map[string]interface{}{
				"running": true,
				"pid":     pid,
				"scope":   scope,
			}
			if started != "" {
				status["started"] = started
			}
			if logPath != "" {
				status["log_path"] = logPath
			}
			outputJSON(status)
			return
		}

		fmt.Printf("Daemon is running (PID %d, %s)\n", pid, scope)
		if started != "" {
			fmt.Printf("  Started: %s\n", started)
		}
		if logPath != "" {
			fmt.Printf("  Log: %s\n", logPath)
		}
	} else {
		if jsonOutput {
			outputJSON(map[string]interface{}{"running": false})
			return
		}
		fmt.Println("Daemon is not running")
	}
}

// showDaemonHealth displays daemon health information
func showDaemonHealth(global bool) {
	var socketPath string
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot get home directory: %v\n", err)
			os.Exit(1)
		}
		socketPath = filepath.Join(home, ".beads", "bd.sock")
	} else {
		beadsDir, err := ensureBeadsDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		socketPath = filepath.Join(beadsDir, "bd.sock")
	}

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
func showDaemonMetrics(global bool) {
	var socketPath string
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot get home directory: %v\n", err)
			os.Exit(1)
		}
		socketPath = filepath.Join(home, ".beads", "bd.sock")
	} else {
		beadsDir, err := ensureBeadsDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		socketPath = filepath.Join(beadsDir, "bd.sock")
	}

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

// migrateToGlobalDaemon migrates from local to global daemon
func migrateToGlobalDaemon() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get home directory: %v\n", err)
		os.Exit(1)
	}

	localPIDFile := filepath.Join(".beads", "daemon.pid")
	globalPIDFile := filepath.Join(home, ".beads", "daemon.pid")

	// Check if local daemon is running
	localRunning, localPID := isDaemonRunning(localPIDFile)
	if !localRunning {
		fmt.Println("No local daemon is running")
	} else {
		fmt.Printf("Stopping local daemon (PID %d)...\n", localPID)
		stopDaemon(localPIDFile)
	}

	// Check if global daemon is already running
	globalRunning, globalPID := isDaemonRunning(globalPIDFile)
	if globalRunning {
		fmt.Printf("Global daemon already running (PID %d)\n", globalPID)
		return
	}

	// Start global daemon
	fmt.Println("Starting global daemon...")
	binPath, err := os.Executable()
	if err != nil {
		binPath = os.Args[0]
	}

	cmd := exec.Command(binPath, "daemon", "--global") // #nosec G204 - bd daemon command from trusted binary
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.Stdin = devNull
		defer func() { _ = devNull.Close() }()
	}

	configureDaemonProcess(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to start global daemon: %v\n", err)
		os.Exit(1)
	}

	go func() { _ = cmd.Wait() }()

	// Wait for daemon to be ready
	time.Sleep(2 * time.Second)

	if isRunning, pid := isDaemonRunning(globalPIDFile); isRunning {
		fmt.Printf("Global daemon started successfully (PID %d)\n", pid)
		fmt.Println()
		fmt.Println("Migration complete! The global daemon will now serve all your beads repositories.")
		fmt.Println("Set BEADS_PREFER_GLOBAL_DAEMON=1 in your shell to make this permanent.")
	} else {
		fmt.Fprintf(os.Stderr, "Error: global daemon failed to start\n")
		os.Exit(1)
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

	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
			fmt.Println("Daemon stopped")
			return
		}
	}

	fmt.Fprintf(os.Stderr, "Warning: daemon did not stop after 5 seconds, forcing termination\n")

	// Check one more time before killing the process to avoid a race.
	if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
		fmt.Println("Daemon stopped")
		return
	}
	if err := process.Kill(); err != nil {
		// Ignore "process already finished" errors
		if !strings.Contains(err.Error(), "process already finished") {
			fmt.Fprintf(os.Stderr, "Error killing process: %v\n", err)
		}
	}
	_ = os.Remove(pidFile)
	fmt.Println("Daemon killed")
}

// startDaemon starts the daemon in background
func startDaemon(interval time.Duration, autoCommit, autoPush bool, logFile, pidFile string, global bool) {
	logPath, err := getLogFilePath(logFile, global)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if os.Getenv("BD_DAEMON_FOREGROUND") == "1" {
		runDaemonLoop(interval, autoCommit, autoPush, logPath, pidFile, global)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot resolve executable path: %v\n", err)
		os.Exit(1)
	}

	args := []string{"daemon",
		"--interval", interval.String(),
	}
	if autoCommit {
		args = append(args, "--auto-commit")
	}
	if autoPush {
		args = append(args, "--auto-push")
	}
	if logFile != "" {
		args = append(args, "--log", logFile)
	}
	if global {
		args = append(args, "--global")
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
	lock, err := acquireDaemonLock(beadsDir, dbPath)
	if err != nil {
		if err == ErrDaemonLocked {
			log.log("Daemon already running (lock held), exiting")
		} else {
			log.log("Error acquiring daemon lock: %v", err)
		}
		return nil, err
	}

	myPID := os.Getpid()
	// #nosec G304 - controlled path from config
	if data, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid == myPID {
			// PID file is correct, continue
		} else {
			log.log("PID file has wrong PID (expected %d, got %d), overwriting", myPID, pid)
			_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", myPID)), 0600)
		}
	} else {
		log.log("PID file missing after lock acquisition, creating")
		_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", myPID)), 0600)
	}

	return lock, nil
}
