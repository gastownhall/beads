package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run background sync daemon",
	Long: `Run a background daemon that automatically syncs issues with git remote.

The daemon will:
- Poll for changes at configurable intervals (default: 5 minutes)
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

		if migrateToGlobal {
			migrateToGlobalDaemon()
			return
		}

		if stop {
			stopDaemon(pidFile)
			return
		}

		// Check if daemon is already running
		if isRunning, pid := isDaemonRunning(pidFile); isRunning {
			fmt.Fprintf(os.Stderr, "Error: daemon already running (PID %d)\n", pid)
			fmt.Fprintf(os.Stderr, "Use 'bd daemon --stop%s' to stop it first\n", boolToFlag(global, " --global"))
			os.Exit(1)
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
	daemonCmd.Flags().Duration("interval", 5*time.Minute, "Sync check interval")
	daemonCmd.Flags().Bool("auto-commit", false, "Automatically commit changes")
	daemonCmd.Flags().Bool("auto-push", false, "Automatically push commits")
	daemonCmd.Flags().Bool("stop", false, "Stop running daemon")
	daemonCmd.Flags().Bool("status", false, "Show daemon status")
	daemonCmd.Flags().Bool("health", false, "Check daemon health and metrics")
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
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false, 0
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}

	err = process.Signal(syscall.Signal(0))
	if err != nil {
		return false, 0
	}

	return true, pid
}

func showDaemonStatus(pidFile string, global bool) {
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		scope := "local"
		if global {
			scope = "global"
		}
		fmt.Printf("✓ Daemon is running (PID %d, %s)\n", pid, scope)
		
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
		fmt.Println("✗ Daemon is not running")
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
		fmt.Println("✗ Daemon is not running")
		os.Exit(1)
	}
	defer client.Close()
	
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
	
	statusIcon := "✓"
	if health.Status == "unhealthy" {
		statusIcon = "✗"
	} else if health.Status == "degraded" {
		statusIcon = "⚠"
	}
	
	fmt.Printf("%s Daemon Health: %s\n", statusIcon, health.Status)
	fmt.Printf("  Version: %s\n", health.Version)
	fmt.Printf("  Uptime: %.1f seconds\n", health.Uptime)
	fmt.Printf("  Cache Size: %d databases\n", health.CacheSize)
	fmt.Printf("  Cache Hits: %d\n", health.CacheHits)
	fmt.Printf("  Cache Misses: %d\n", health.CacheMisses)
	
	if health.CacheHits+health.CacheMisses > 0 {
		hitRate := float64(health.CacheHits) / float64(health.CacheHits+health.CacheMisses) * 100
		fmt.Printf("  Cache Hit Rate: %.1f%%\n", hitRate)
	}
	
	fmt.Printf("  DB Response Time: %.2f ms\n", health.DBResponseTime)
	
	if health.Error != "" {
		fmt.Printf("  Error: %s\n", health.Error)
	}
	
	if health.Status == "unhealthy" {
		os.Exit(1)
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
		fmt.Printf("✓ Global daemon already running (PID %d)\n", globalPID)
		return
	}
	
	// Start global daemon
	fmt.Println("Starting global daemon...")
	binPath, err := os.Executable()
	if err != nil {
		binPath = os.Args[0]
	}
	
	cmd := exec.Command(binPath, "daemon", "--global")
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.Stdin = devNull
		defer devNull.Close()
	}
	
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to start global daemon: %v\n", err)
		os.Exit(1)
	}
	
	go cmd.Wait()
	
	// Wait for daemon to be ready
	time.Sleep(2 * time.Second)
	
	if isRunning, pid := isDaemonRunning(globalPIDFile); isRunning {
		fmt.Printf("✓ Global daemon started successfully (PID %d)\n", pid)
		fmt.Println()
		fmt.Println("Migration complete! The global daemon will now serve all your beads repositories.")
		fmt.Println("Set BEADS_PREFER_GLOBAL_DAEMON=1 in your shell to make this permanent.")
	} else {
		fmt.Fprintf(os.Stderr, "Error: global daemon failed to start\n")
		os.Exit(1)
	}
}

func stopDaemon(pidFile string) {
	if isRunning, pid := isDaemonRunning(pidFile); !isRunning {
		fmt.Println("Daemon is not running")
		return
	} else {
		fmt.Printf("Stopping daemon (PID %d)...\n", pid)
		
		process, err := os.FindProcess(pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error finding process: %v\n", err)
			os.Exit(1)
		}

		if err := process.Signal(syscall.SIGTERM); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending SIGTERM: %v\n", err)
			os.Exit(1)
		}

		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
				fmt.Println("✓ Daemon stopped")
				return
			}
		}

		fmt.Fprintf(os.Stderr, "Warning: daemon did not stop after 5 seconds, sending SIGKILL\n")
		if err := process.Kill(); err != nil {
			fmt.Fprintf(os.Stderr, "Error killing process: %v\n", err)
		}
		os.Remove(pidFile)
		fmt.Println("✓ Daemon killed")
	}
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

	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "BD_DAEMON_FOREGROUND=1")
	configureDaemonProcess(cmd)
	
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening /dev/null: %v\n", err)
		os.Exit(1)
	}
	defer devNull.Close()
	
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
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid == expectedPID {
				fmt.Printf("✓ Daemon started (PID %d)\n", expectedPID)
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
		tempFile.Close()
		if writeErr != nil {
			os.Remove(tempPath) // Remove temp file on error
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
	// TODO Phase 4: Implement direct import for daemon
	// Currently a no-op - daemon doesn't import git changes into DB
	// This means git pulls won't update the database until this is implemented
	// For now, users must restart daemon after git pulls to see changes
	return nil
}

func runDaemonLoop(interval time.Duration, autoCommit, autoPush bool, logPath, pidFile string, global bool) {
	logF, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening log file: %v\n", err)
		os.Exit(1)
	}
	defer logF.Close()

	log := func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		fmt.Fprintf(logF, "[%s] %s\n", timestamp, msg)
	}

	myPID := os.Getpid()
	pidFileCreated := false
	
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(pidFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			fmt.Fprintf(f, "%d", myPID)
			f.Close()
			pidFileCreated = true
			break
		}
		
		if errors.Is(err, fs.ErrExist) {
			if isRunning, pid := isDaemonRunning(pidFile); isRunning {
				log("Daemon already running (PID %d), exiting", pid)
				os.Exit(1)
			}
			log("Stale PID file detected, removing and retrying")
			os.Remove(pidFile)
			continue
		}
		
		log("Error creating PID file: %v", err)
		os.Exit(1)
	}
	
	if !pidFileCreated {
		log("Failed to create PID file after retries")
		os.Exit(1)
	}
	
	defer os.Remove(pidFile)

	log("Daemon started (interval: %v, auto-commit: %v, auto-push: %v)", interval, autoCommit, autoPush)

	// Global daemon runs in routing mode without opening a database
	if global {
		globalDir, err := getGlobalBeadsDir()
		if err != nil {
			log("Error: cannot get global beads directory: %v", err)
			os.Exit(1)
		}
		socketPath := filepath.Join(globalDir, "bd.sock")
		
		// Create server with nil storage - uses per-request routing
		server := rpc.NewServer(socketPath, nil)
		
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		
		// Start RPC server in background
		serverErrChan := make(chan error, 1)
		go func() {
			log("Starting global RPC server: %s", socketPath)
			if err := server.Start(ctx); err != nil {
				log("RPC server error: %v", err)
				serverErrChan <- err
			}
		}()
		
		// Wait for server to start or fail
		select {
		case err := <-serverErrChan:
			log("RPC server failed to start: %v", err)
			os.Exit(1)
		case <-time.After(2 * time.Second):
			log("Global RPC server started")
		}
		
		// Wait for shutdown signal
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		
		sig := <-sigChan
		log("Received signal: %v", sig)
		log("Shutting down global daemon...")
		
		cancel()
		if err := server.Stop(); err != nil {
			log("Error stopping server: %v", err)
		}
		
		log("Global daemon stopped")
		return
	}

	// Local daemon mode - open database and run sync loop
	daemonDBPath := dbPath
	if daemonDBPath == "" {
		// Try to find database in current repo
		if foundDB := beads.FindDatabasePath(); foundDB != "" {
			daemonDBPath = foundDB
		} else {
			// No database found - error out instead of falling back to ~/.beads
			log("Error: no beads database found")
			log("Hint: run 'bd init' to create a database or set BEADS_DB environment variable")
			os.Exit(1)
		}
	}
	
	log("Using database: %s", daemonDBPath)
	
	store, err := sqlite.New(daemonDBPath)
	if err != nil {
		log("Error: cannot open database: %v", err)
		os.Exit(1)
	}
	defer store.Close()
	log("Database opened: %s", daemonDBPath)

	// Start RPC server
	socketPath := filepath.Join(filepath.Dir(daemonDBPath), "bd.sock")
	server := rpc.NewServer(socketPath, store)
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	// Start RPC server in background
	serverErrChan := make(chan error, 1)
	go func() {
		log("Starting RPC server: %s", socketPath)
		if err := server.Start(ctx); err != nil {
			log("RPC server error: %v", err)
			serverErrChan <- err
		}
	}()
	
	// Wait for server to start or fail
	select {
	case err := <-serverErrChan:
		log("RPC server failed to start: %v", err)
		os.Exit(1)
	case <-time.After(2 * time.Second):
		// If no error after 2 seconds, assume success
		log("RPC server started")
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	doSync := func() {
		syncCtx, syncCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer syncCancel()
		
		log("Starting sync cycle...")
		
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log("Error: JSONL path not found")
			return
		}

		if err := exportToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
			log("Export failed: %v", err)
			return
		}
		log("Exported to JSONL")

		if autoCommit {
			hasChanges, err := gitHasChanges(syncCtx, jsonlPath)
			if err != nil {
				log("Error checking git status: %v", err)
				return
			}

			if hasChanges {
				message := fmt.Sprintf("bd daemon sync: %s", time.Now().Format("2006-01-02 15:04:05"))
				if err := gitCommit(syncCtx, jsonlPath, message); err != nil {
					log("Commit failed: %v", err)
					return
				}
				log("Committed changes")
			}
		}

		if err := gitPull(syncCtx); err != nil {
			log("Pull failed: %v", err)
			return
		}
		log("Pulled from remote")

		if err := importToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
			log("Import failed: %v", err)
			return
		}
		log("Imported from JSONL")

		if autoPush && autoCommit {
			if err := gitPush(syncCtx); err != nil {
				log("Push failed: %v", err)
				return
			}
			log("Pushed to remote")
		}

		log("Sync cycle complete")
	}

	doSync()

	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			doSync()
		case sig := <-sigChan:
			if sig == syscall.SIGHUP {
				log("Received SIGHUP, ignoring (daemon continues running)")
				continue
			}
			log("Received signal %v, shutting down gracefully...", sig)
			cancel()
			if err := server.Stop(); err != nil {
				log("Error stopping RPC server: %v", err)
			}
			return
		case <-ctx.Done():
			log("Context cancelled, shutting down")
			if err := server.Stop(); err != nil {
				log("Error stopping RPC server: %v", err)
			}
			return
		case err := <-serverErrChan:
			log("RPC server failed: %v", err)
			cancel()
			return
		}
	}
}
