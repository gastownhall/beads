package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"gopkg.in/natefinch/lumberjack.v2"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run background sync daemon",
	Long: `Run a background daemon that automatically syncs issues with git remote.

The daemon will:
- Poll for changes at configurable intervals (default: 5 seconds)
- Export pending database changes to JSONL
- Auto-commit changes if --auto-commit flag set
- Auto-push commits if --auto-push flag set
- Pull remote changes periodically
- Auto-import when remote changes detected

Use --stop to stop a running daemon.
Use --status to check if daemon is running.
Use --health to check daemon health and metrics.`,
	Run: func(cmd *cobra.Command, args []string) {
		stop, _ := cmd.Flags().GetBool("stop")
		status, _ := cmd.Flags().GetBool("status")
		health, _ := cmd.Flags().GetBool("health")
		metrics, _ := cmd.Flags().GetBool("metrics")
		migrateToGlobal, _ := cmd.Flags().GetBool("migrate-to-global")
		interval, _ := cmd.Flags().GetDuration("interval")
		autoCommit, _ := cmd.Flags().GetBool("auto-commit")
		autoPush, _ := cmd.Flags().GetBool("auto-push")
		logFile, _ := cmd.Flags().GetString("log")
		global, _ := cmd.Flags().GetBool("global")

		if interval <= 0 {
			fmt.Fprintf(os.Stderr, "Error: interval must be positive (got %v)\n", interval)
			os.Exit(1)
		}

		pidFile, err := getPIDFilePath(global)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if status {
			showDaemonStatus(pidFile, global)
			return
		}

		if health {
			showDaemonHealth(global)
			return
		}

		if metrics {
			showDaemonMetrics(global)
			return
		}

		if migrateToGlobal {
			migrateToGlobalDaemon()
			return
		}

		if stop {
			stopDaemon(pidFile)
			return
		}

		// Skip daemon-running check if we're the forked child (BD_DAEMON_FOREGROUND=1)
		// because the check happens in the parent process before forking
		if os.Getenv("BD_DAEMON_FOREGROUND") != "1" {
			// Check if daemon is already running
			if isRunning, pid := isDaemonRunning(pidFile); isRunning {
			// Check if running daemon has compatible version
			socketPath := getSocketPathForPID(pidFile, global)
			if client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second); err == nil && client != nil {
				health, healthErr := client.Health()
				_ = client.Close()
				
				// If we can check version and it's compatible, exit
				if healthErr == nil && health.Compatible {
					fmt.Fprintf(os.Stderr, "Error: daemon already running (PID %d, version %s)\n", pid, health.Version)
					fmt.Fprintf(os.Stderr, "Use 'bd daemon --stop%s' to stop it first\n", boolToFlag(global, " --global"))
					os.Exit(1)
				}
				
				// Version mismatch - auto-stop old daemon
				if healthErr == nil && !health.Compatible {
					fmt.Fprintf(os.Stderr, "Warning: daemon version mismatch (daemon: %s, client: %s)\n", health.Version, Version)
					fmt.Fprintf(os.Stderr, "Stopping old daemon and starting new one...\n")
					stopDaemon(pidFile)
					// Continue with daemon startup
				}
			} else {
				// Can't check version - assume incompatible
				fmt.Fprintf(os.Stderr, "Error: daemon already running (PID %d)\n", pid)
				fmt.Fprintf(os.Stderr, "Use 'bd daemon --stop%s' to stop it first\n", boolToFlag(global, " --global"))
				os.Exit(1)
			}
		}
		}

		// Global daemon doesn't support auto-commit/auto-push (no sync loop)
		if global && (autoCommit || autoPush) {
			fmt.Fprintf(os.Stderr, "Error: --auto-commit and --auto-push are not supported with --global\n")
			fmt.Fprintf(os.Stderr, "Hint: global daemon runs in routing mode and doesn't perform background sync\n")
			fmt.Fprintf(os.Stderr, "      Use local daemon (without --global) for auto-commit/auto-push features\n")
			os.Exit(1)
		}

		// Validate we're in a git repo (skip for global daemon)
		if !global && !isGitRepo() {
			fmt.Fprintf(os.Stderr, "Error: not in a git repository\n")
			fmt.Fprintf(os.Stderr, "Hint: run 'git init' to initialize a repository\n")
			os.Exit(1)
		}

		// Check for upstream if auto-push enabled
		if autoPush && !gitHasUpstream() {
			fmt.Fprintf(os.Stderr, "Error: no upstream configured (required for --auto-push)\n")
			fmt.Fprintf(os.Stderr, "Hint: git push -u origin <branch-name>\n")
			os.Exit(1)
		}

		// Warn if starting daemon in a git worktree
		if !global {
			// Ensure dbPath is set for warning
			if dbPath == "" {
				if foundDB := beads.FindDatabasePath(); foundDB != "" {
					dbPath = foundDB
				}
			}
			if dbPath != "" {
				warnWorktreeDaemon(dbPath)
			}
		}

		// Start daemon
		scope := "local"
		if global {
			scope = "global"
		}
		fmt.Printf("Starting bd daemon (%s, interval: %v, auto-commit: %v, auto-push: %v)\n",
			scope, interval, autoCommit, autoPush)
		if logFile != "" {
			fmt.Printf("Logging to: %s\n", logFile)
		}

		startDaemon(interval, autoCommit, autoPush, logFile, pidFile, global)
	},
}

func init() {
	daemonCmd.Flags().Duration("interval", 5*time.Second, "Sync check interval")
	daemonCmd.Flags().Bool("auto-commit", false, "Automatically commit changes")
	daemonCmd.Flags().Bool("auto-push", false, "Automatically push commits")
	daemonCmd.Flags().Bool("stop", false, "Stop running daemon")
	daemonCmd.Flags().Bool("status", false, "Show daemon status")
	daemonCmd.Flags().Bool("health", false, "Check daemon health and metrics")
	daemonCmd.Flags().Bool("metrics", false, "Show detailed daemon metrics")
	daemonCmd.Flags().Bool("migrate-to-global", false, "Migrate from local to global daemon")
	daemonCmd.Flags().String("log", "", "Log file path (default: .beads/daemon.log)")
	daemonCmd.Flags().Bool("global", false, "Run as global daemon (socket at ~/.beads/bd.sock)")
	rootCmd.AddCommand(daemonCmd)
}

func getGlobalBeadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot get home directory: %w", err)
	}

	beadsDir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		return "", fmt.Errorf("cannot create global beads directory: %w", err)
	}

	return beadsDir, nil
}

func ensureBeadsDir() (string, error) {
	var beadsDir string
	if dbPath != "" {
		beadsDir = filepath.Dir(dbPath)
	} else {
		// Use public API to find database (same logic as other commands)
		if foundDB := beads.FindDatabasePath(); foundDB != "" {
			dbPath = foundDB // Store it for later use
			beadsDir = filepath.Dir(foundDB)
		} else {
			// No database found - error out instead of falling back to ~/.beads
			return "", fmt.Errorf("no database path configured (run 'bd init' or set BEADS_DB)")
		}
	}

	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		return "", fmt.Errorf("cannot create beads directory: %w", err)
	}

	return beadsDir, nil
}

func boolToFlag(condition bool, flag string) string {
	if condition {
		return flag
	}
	return ""
}

// getEnvInt reads an integer from environment variable with a default value
func getEnvInt(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return defaultValue
}

// getEnvBool reads a boolean from environment variable with a default value
func getEnvBool(key string, defaultValue bool) bool {
	if val := os.Getenv(key); val != "" {
		return val == "true" || val == "1"
	}
	return defaultValue
}

// getSocketPathForPID determines the socket path for a given PID file
func getSocketPathForPID(pidFile string, global bool) string {
	if global {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".beads", "bd.sock")
	}
	// Local daemon: socket is in same directory as PID file
	return filepath.Join(filepath.Dir(pidFile), "bd.sock")
}

func getPIDFilePath(global bool) (string, error) {
	var beadsDir string
	var err error

	if global {
		beadsDir, err = getGlobalBeadsDir()
	} else {
		beadsDir, err = ensureBeadsDir()
	}

	if err != nil {
		return "", err
	}
	return filepath.Join(beadsDir, "daemon.pid"), nil
}

func getLogFilePath(userPath string, global bool) (string, error) {
	if userPath != "" {
		return userPath, nil
	}

	var beadsDir string
	var err error

	if global {
		beadsDir, err = getGlobalBeadsDir()
	} else {
		beadsDir, err = ensureBeadsDir()
	}

	if err != nil {
		return "", err
	}
	return filepath.Join(beadsDir, "daemon.log"), nil
}

func isDaemonRunning(pidFile string) (bool, int) {
	beadsDir := filepath.Dir(pidFile)
	return tryDaemonLock(beadsDir)
}

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

func showDaemonStatus(pidFile string, global bool) {
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		scope := "local"
		if global {
			scope = "global"
		}
		fmt.Printf("Daemon is running (PID %d, %s)\n", pid, scope)

		if info, err := os.Stat(pidFile); err == nil {
			fmt.Printf("  Started: %s\n", info.ModTime().Format("2006-01-02 15:04:05"))
		}

		logPath, err := getLogFilePath("", global)
		if err == nil {
			if _, err := os.Stat(logPath); err == nil {
				fmt.Printf("  Log: %s\n", logPath)
			}
		}
	} else {
		fmt.Println("Daemon is not running")
	}
}

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

// exportToJSONLWithStore exports issues to JSONL using the provided store
func exportToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	if len(issues) == 0 {
		existingCount, err := countIssuesInJSONL(jsonlPath)
		if err != nil {
			// If we can't read the file, it might not exist yet, which is fine
			if !os.IsNotExist(err) {
				return fmt.Errorf("warning: failed to read existing JSONL: %w", err)
			}
		} else if existingCount > 0 {
			return fmt.Errorf("refusing to export empty database over non-empty JSONL file (database: 0 issues, JSONL: %d issues). This would result in data loss", existingCount)
		}
	}

	// Sort by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Populate dependencies for all issues
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range issues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range issues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get comments for %s: %w", issue.ID, err)
		}
		issue.Comments = comments
	}

	// Create temp file for atomic write
	dir := filepath.Dir(jsonlPath)
	base := filepath.Base(jsonlPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	// Use defer pattern for proper cleanup
	var writeErr error
	defer func() {
		_ = tempFile.Close()
		if writeErr != nil {
			_ = os.Remove(tempPath) // Remove temp file on error
		}
	}()

	// Write JSONL
	for _, issue := range issues {
		data, marshalErr := json.Marshal(issue)
		if marshalErr != nil {
			writeErr = fmt.Errorf("failed to marshal issue %s: %w", issue.ID, marshalErr)
			return writeErr
		}
		if _, writeErr = tempFile.Write(data); writeErr != nil {
			writeErr = fmt.Errorf("failed to write issue %s: %w", issue.ID, writeErr)
			return writeErr
		}
		if _, writeErr = tempFile.WriteString("\n"); writeErr != nil {
			writeErr = fmt.Errorf("failed to write newline: %w", writeErr)
			return writeErr
		}
	}

	// Close before rename
	if writeErr = tempFile.Close(); writeErr != nil {
		writeErr = fmt.Errorf("failed to close temp file: %w", writeErr)
		return writeErr
	}

	// Atomic rename
	if writeErr = os.Rename(tempPath, jsonlPath); writeErr != nil {
		writeErr = fmt.Errorf("failed to rename temp file: %w", writeErr)
		return writeErr
	}

	return nil
}

// importToJSONLWithStore imports issues from JSONL using the provided store
// Note: This cannot use the import command approach since we're in the daemon
// We need to implement direct import logic here
func importToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Read JSONL file
	file, err := os.Open(jsonlPath) // #nosec G304 - controlled path from config
	if err != nil {
		return fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer file.Close()
	
	// Parse all issues
	var issues []*types.Issue
	scanner := bufio.NewScanner(file)
	lineNum := 0
	
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		
		// Skip empty lines
		if line == "" {
			continue
		}
		
		// Parse JSON
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			// Log error but continue - don't fail entire import
			fmt.Fprintf(os.Stderr, "Warning: failed to parse JSONL line %d: %v\n", lineNum, err)
			continue
		}
		
		issues = append(issues, &issue)
	}
	
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}
	
	// Use existing import logic with auto-conflict resolution
	opts := ImportOptions{
		ResolveCollisions:    true,  // Auto-resolve ID conflicts
		DryRun:              false,
		SkipUpdate:          false,
		Strict:              false,
		SkipPrefixValidation: true,  // Skip prefix validation for auto-import
	}
	
	_, err = importIssuesCore(ctx, "", store, issues, opts)
	return err
}

type daemonLogger struct {
	logFunc func(string, ...interface{})
}

func (d *daemonLogger) log(format string, args ...interface{}) {
	d.logFunc(format, args...)
}

// validateDatabaseFingerprint checks that the database belongs to this repository
func validateDatabaseFingerprint(store storage.Storage, log *daemonLogger) error {
	ctx := context.Background()

	// Get stored repo ID
	storedRepoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil && err.Error() != "metadata key not found: repo_id" {
		return fmt.Errorf("failed to read repo_id: %w", err)
	}

	// If no repo_id, this is a legacy database - require explicit migration
	if storedRepoID == "" {
		return fmt.Errorf(`
LEGACY DATABASE DETECTED!

This database was created before version 0.17.5 and lacks a repository fingerprint.
To continue using this database, you must explicitly set its repository ID:

  bd migrate --update-repo-id

This ensures the database is bound to this repository and prevents accidental
database sharing between different repositories.

If this is a fresh clone, run:
  rm -rf .beads && bd init

Note: Auto-claiming legacy databases is intentionally disabled to prevent
silent corruption when databases are copied between repositories.
`)
	}

	// Validate repo ID matches current repository
	currentRepoID, err := beads.ComputeRepoID()
	if err != nil {
		log.log("Warning: could not compute current repository ID: %v", err)
		return nil
	}

	if storedRepoID != currentRepoID {
		return fmt.Errorf(`
DATABASE MISMATCH DETECTED!

This database belongs to a different repository:
  Database repo ID:  %s
  Current repo ID:   %s

This usually means:
  1. You copied a .beads directory from another repo (don't do this!)
  2. Git remote URL changed (run 'bd migrate --update-repo-id')
  3. Database corruption
  4. bd was upgraded and URL canonicalization changed

Solutions:
  - If remote URL changed: bd migrate --update-repo-id
  - If bd was upgraded: bd migrate --update-repo-id
  - If wrong database: rm -rf .beads && bd init
  - If correct database: BEADS_IGNORE_REPO_MISMATCH=1 bd daemon
    (Warning: This can cause data corruption across clones!)
`, storedRepoID[:8], currentRepoID[:8])
	}

	log.log("Repository fingerprint validated: %s", currentRepoID[:8])
	return nil
}

func setupDaemonLogger(logPath string) (*lumberjack.Logger, daemonLogger) {
	maxSizeMB := getEnvInt("BEADS_DAEMON_LOG_MAX_SIZE", 10)
	maxBackups := getEnvInt("BEADS_DAEMON_LOG_MAX_BACKUPS", 3)
	maxAgeDays := getEnvInt("BEADS_DAEMON_LOG_MAX_AGE", 7)
	compress := getEnvBool("BEADS_DAEMON_LOG_COMPRESS", true)

	logF := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		MaxAge:     maxAgeDays,
		Compress:   compress,
	}

	logger := daemonLogger{
		logFunc: func(format string, args ...interface{}) {
			msg := fmt.Sprintf(format, args...)
			timestamp := time.Now().Format("2006-01-02 15:04:05")
			_, _ = fmt.Fprintf(logF, "[%s] %s\n", timestamp, msg)
		},
	}

	return logF, logger
}

func setupDaemonLock(pidFile string, dbPath string, log daemonLogger) (io.Closer, error) {
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

func startRPCServer(ctx context.Context, socketPath string, store storage.Storage, workspacePath string, dbPath string, log daemonLogger) (*rpc.Server, chan error, error) {
	// Sync daemon version with CLI version
	rpc.ServerVersion = Version
	
	server := rpc.NewServer(socketPath, store, workspacePath, dbPath)
	serverErrChan := make(chan error, 1)

	go func() {
		log.log("Starting RPC server: %s", socketPath)
		if err := server.Start(ctx); err != nil {
			log.log("RPC server error: %v", err)
			serverErrChan <- err
		}
	}()

	select {
	case err := <-serverErrChan:
		log.log("RPC server failed to start: %v", err)
		return nil, nil, err
	case <-server.WaitReady():
		log.log("RPC server ready (socket listening)")
	case <-time.After(5 * time.Second):
		log.log("WARNING: Server didn't signal ready after 5 seconds (may still be starting)")
	}

	return server, serverErrChan, nil
}

func runGlobalDaemon(log daemonLogger) {
	globalDir, err := getGlobalBeadsDir()
	if err != nil {
		log.log("Error: cannot get global beads directory: %v", err)
		os.Exit(1)
	}
	socketPath := filepath.Join(globalDir, "bd.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, _, err := startRPCServer(ctx, socketPath, nil, globalDir, "", log)
	if err != nil {
		return
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	sig := <-sigChan
	log.log("Received signal: %v", sig)
	log.log("Shutting down global daemon...")

	cancel()
	if err := server.Stop(); err != nil {
		log.log("Error stopping server: %v", err)
	}

	log.log("Global daemon stopped")
}

func createSyncFunc(ctx context.Context, store storage.Storage, autoCommit, autoPush bool, log daemonLogger) func() {
	return func() {
		syncCtx, syncCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer syncCancel()

		log.log("Starting sync cycle...")

		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.log("Error: JSONL path not found")
			return
		}

		// Check for exclusive lock before processing database
		beadsDir := filepath.Dir(jsonlPath)
		skip, holder, err := types.ShouldSkipDatabase(beadsDir)
		if skip {
			if err != nil {
				log.log("Skipping database (lock check failed: %v)", err)
			} else {
				log.log("Skipping database (locked by %s)", holder)
			}
			return
		}
		if holder != "" {
			log.log("Removed stale lock (%s), proceeding with sync", holder)
		}

		// Integrity check: validate before export
		if err := validatePreExport(syncCtx, store, jsonlPath); err != nil {
			log.log("Pre-export validation failed: %v", err)
			return
		}

		// Check for duplicate IDs (database corruption)
		if err := checkDuplicateIDs(syncCtx, store); err != nil {
			log.log("Duplicate ID check failed: %v", err)
			return
		}

		// Check for orphaned dependencies (warns but doesn't fail)
		if orphaned, err := checkOrphanedDeps(syncCtx, store); err != nil {
			log.log("Orphaned dependency check failed: %v", err)
		} else if len(orphaned) > 0 {
			log.log("Found %d orphaned dependencies: %v", len(orphaned), orphaned)
		}

		if err := exportToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
			log.log("Export failed: %v", err)
			return
		}
		log.log("Exported to JSONL")

		if autoCommit {
			hasChanges, err := gitHasChanges(syncCtx, jsonlPath)
			if err != nil {
				log.log("Error checking git status: %v", err)
				return
			}

			if hasChanges {
				message := fmt.Sprintf("bd daemon sync: %s", time.Now().Format("2006-01-02 15:04:05"))
				if err := gitCommit(syncCtx, jsonlPath, message); err != nil {
					log.log("Commit failed: %v", err)
					return
				}
				log.log("Committed changes")
			}
		}

		if err := gitPull(syncCtx); err != nil {
		log.log("Pull failed: %v", err)
		return
		}
		log.log("Pulled from remote")

		// Count issues before import for validation
	beforeCount, err := countDBIssues(syncCtx, store)
	if err != nil {
		log.log("Failed to count issues before import: %v", err)
		return
	}

	if err := importToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
		log.log("Import failed: %v", err)
		return
	}
	log.log("Imported from JSONL")

	// Validate import didn't cause data loss
	afterCount, err := countDBIssues(syncCtx, store)
	if err != nil {
		log.log("Failed to count issues after import: %v", err)
		return
	}

	if err := validatePostImport(beforeCount, afterCount); err != nil {
		log.log("Post-import validation failed: %v", err)
		return
	}

		if autoPush && autoCommit {
			if err := gitPush(syncCtx); err != nil {
				log.log("Push failed: %v", err)
				return
			}
			log.log("Pushed to remote")
		}

		log.log("Sync cycle complete")
	}
}

func runEventLoop(ctx context.Context, cancel context.CancelFunc, ticker *time.Ticker, doSync func(), server *rpc.Server, serverErrChan chan error, log daemonLogger) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			doSync()
		case sig := <-sigChan:
			if isReloadSignal(sig) {
				log.log("Received reload signal, ignoring (daemon continues running)")
				continue
			}
			log.log("Received signal %v, shutting down gracefully...", sig)
			cancel()
			if err := server.Stop(); err != nil {
				log.log("Error stopping RPC server: %v", err)
			}
			return
		case <-ctx.Done():
			log.log("Context canceled, shutting down")
			if err := server.Stop(); err != nil {
				log.log("Error stopping RPC server: %v", err)
			}
			return
		case err := <-serverErrChan:
			log.log("RPC server failed: %v", err)
			cancel()
			if err := server.Stop(); err != nil {
				log.log("Error stopping RPC server: %v", err)
			}
			return
		}
	}
}

func runDaemonLoop(interval time.Duration, autoCommit, autoPush bool, logPath, pidFile string, global bool) {
	logF, log := setupDaemonLogger(logPath)
	defer func() { _ = logF.Close() }()

	// Determine database path first (needed for lock file metadata)
	daemonDBPath := ""
	if !global {
		daemonDBPath = dbPath
		if daemonDBPath == "" {
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				daemonDBPath = foundDB
			} else {
				log.log("Error: no beads database found")
				log.log("Hint: run 'bd init' to create a database or set BEADS_DB environment variable")
				os.Exit(1)
			}
		}
	}

	lock, err := setupDaemonLock(pidFile, daemonDBPath, log)
	if err != nil {
		os.Exit(1)
	}
	defer func() { _ = lock.Close() }()
	defer func() { _ = os.Remove(pidFile) }()

	log.log("Daemon started (interval: %v, auto-commit: %v, auto-push: %v)", interval, autoCommit, autoPush)

	if global {
		runGlobalDaemon(log)
		return
	}

	// Check for multiple .db files (ambiguity error)
	beadsDir := filepath.Dir(daemonDBPath)
	matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
	if err == nil && len(matches) > 1 {
		// Filter out backup files
		var validDBs []string
		for _, match := range matches {
			if filepath.Ext(filepath.Base(match)) != ".backup" {
				validDBs = append(validDBs, match)
			}
		}
		if len(validDBs) > 1 {
			log.log("Error: Multiple database files found in %s:", beadsDir)
			for _, db := range validDBs {
				log.log("  - %s", filepath.Base(db))
			}
			log.log("")
			log.log("Beads requires a single canonical database: %s", beads.CanonicalDatabaseName)
			log.log("Run 'bd init' to migrate legacy databases")
			os.Exit(1)
		}
	}

	// Validate using canonical name
	dbBaseName := filepath.Base(daemonDBPath)
	if dbBaseName != beads.CanonicalDatabaseName {
		log.log("Error: Non-canonical database name: %s", dbBaseName)
		log.log("Expected: %s", beads.CanonicalDatabaseName)
		log.log("")
		log.log("Run 'bd init' to migrate to canonical name")
		os.Exit(1)
	}

	log.log("Using database: %s", daemonDBPath)

	store, err := sqlite.New(daemonDBPath)
	if err != nil {
		log.log("Error: cannot open database: %v", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()
	log.log("Database opened: %s", daemonDBPath)

	// Validate database fingerprint
	if err := validateDatabaseFingerprint(store, &log); err != nil {
		if os.Getenv("BEADS_IGNORE_REPO_MISMATCH") != "1" {
			log.log("Error: %v", err)
			os.Exit(1)
		}
		log.log("Warning: repository mismatch ignored (BEADS_IGNORE_REPO_MISMATCH=1)")
	}

	// Validate schema version matches daemon version
	versionCtx := context.Background()
	dbVersion, err := store.GetMetadata(versionCtx, "bd_version")
	if err != nil && err.Error() != "metadata key not found: bd_version" {
		log.log("Error: failed to read database version: %v", err)
		os.Exit(1)
	}
	
	if dbVersion != "" && dbVersion != Version {
		log.log("Error: Database schema version mismatch")
		log.log("  Database version: %s", dbVersion)
		log.log("  Daemon version: %s", Version)
		log.log("")
		log.log("The database was created with a different version of bd.")
		log.log("This may cause compatibility issues.")
		log.log("")
		log.log("Options:")
		log.log("  1. Run 'bd migrate' to update the database to the current version")
		log.log("  2. Upgrade/downgrade bd to match database version: %s", dbVersion)
		log.log("  3. Set BEADS_IGNORE_VERSION_MISMATCH=1 to proceed anyway (not recommended)")
		log.log("")
		
		// Allow override via environment variable for emergencies
		if os.Getenv("BEADS_IGNORE_VERSION_MISMATCH") != "1" {
			os.Exit(1)
		}
		log.log("Warning: Proceeding despite version mismatch (BEADS_IGNORE_VERSION_MISMATCH=1)")
	} else if dbVersion == "" {
		// Old database without version metadata - set it now
		log.log("Warning: Database missing version metadata, setting to %s", Version)
		if err := store.SetMetadata(versionCtx, "bd_version", Version); err != nil {
			log.log("Error: failed to set database version: %v", err)
			os.Exit(1)
		}
	}

	// Get workspace path (.beads directory) - beadsDir already defined above
	// Get actual workspace root (parent of .beads)
	workspacePath := filepath.Dir(beadsDir)
	socketPath := filepath.Join(beadsDir, "bd.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, serverErrChan, err := startRPCServer(ctx, socketPath, store, workspacePath, daemonDBPath, log)
	if err != nil {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	doSync := createSyncFunc(ctx, store, autoCommit, autoPush, log)
	doSync()

	// Choose event loop based on BEADS_DAEMON_MODE
	daemonMode := os.Getenv("BEADS_DAEMON_MODE")
	if daemonMode == "" {
		daemonMode = "poll" // Default to polling for Phase 1
	}

	switch daemonMode {
	case "events":
		log.log("Using event-driven mode")
		// For Phase 1: event-driven mode uses full sync on both export and import events
		// TODO: Optimize to separate export-only and import-only triggers
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.log("Error: JSONL path not found, cannot use event-driven mode")
			log.log("Falling back to polling mode")
			runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, log)
		} else {
			runEventDrivenLoop(ctx, cancel, server, serverErrChan, store, jsonlPath, doSync, doSync, log)
		}
	case "poll":
		log.log("Using polling mode (interval: %v)", interval)
		runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, log)
	default:
		log.log("Unknown BEADS_DAEMON_MODE: %s (valid: poll, events), defaulting to poll", daemonMode)
		runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, log)
	}
}
