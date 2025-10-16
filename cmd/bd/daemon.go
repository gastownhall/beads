package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
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
Use --status to check if daemon is running.`,
	Run: func(cmd *cobra.Command, args []string) {
		stop, _ := cmd.Flags().GetBool("stop")
		status, _ := cmd.Flags().GetBool("status")
		interval, _ := cmd.Flags().GetDuration("interval")
		autoCommit, _ := cmd.Flags().GetBool("auto-commit")
		autoPush, _ := cmd.Flags().GetBool("auto-push")
		logFile, _ := cmd.Flags().GetString("log")

		if interval <= 0 {
			fmt.Fprintf(os.Stderr, "Error: interval must be positive (got %v)\n", interval)
			os.Exit(1)
		}

		pidFile, err := getPIDFilePath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if status {
			showDaemonStatus(pidFile)
			return
		}

		if stop {
			stopDaemon(pidFile)
			return
		}

		// Check if daemon is already running
		if isRunning, pid := isDaemonRunning(pidFile); isRunning {
			fmt.Fprintf(os.Stderr, "Error: daemon already running (PID %d)\n", pid)
			fmt.Fprintf(os.Stderr, "Use 'bd daemon --stop' to stop it first\n")
			os.Exit(1)
		}

		// Validate we're in a git repo
		if !isGitRepo() {
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
		fmt.Printf("Starting bd daemon (interval: %v, auto-commit: %v, auto-push: %v)\n",
			interval, autoCommit, autoPush)
		if logFile != "" {
			fmt.Printf("Logging to: %s\n", logFile)
		}

		startDaemon(interval, autoCommit, autoPush, logFile, pidFile)
	},
}

func init() {
	daemonCmd.Flags().Duration("interval", 5*time.Minute, "Sync check interval")
	daemonCmd.Flags().Bool("auto-commit", false, "Automatically commit changes")
	daemonCmd.Flags().Bool("auto-push", false, "Automatically push commits")
	daemonCmd.Flags().Bool("stop", false, "Stop running daemon")
	daemonCmd.Flags().Bool("status", false, "Show daemon status")
	daemonCmd.Flags().String("log", "", "Log file path (default: .beads/daemon.log)")
	rootCmd.AddCommand(daemonCmd)
}

func ensureBeadsDir() (string, error) {
	var beadsDir string
	if dbPath != "" {
		beadsDir = filepath.Dir(dbPath)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home directory: %w", err)
		}
		beadsDir = filepath.Join(home, ".beads")
	}

	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		return "", fmt.Errorf("cannot create beads directory: %w", err)
	}

	return beadsDir, nil
}

func getPIDFilePath() (string, error) {
	beadsDir, err := ensureBeadsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(beadsDir, "daemon.pid"), nil
}

func getLogFilePath(userPath string) (string, error) {
	if userPath != "" {
		return userPath, nil
	}
	beadsDir, err := ensureBeadsDir()
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

func showDaemonStatus(pidFile string) {
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		fmt.Printf("✓ Daemon is running (PID %d)\n", pid)
		
		if info, err := os.Stat(pidFile); err == nil {
			fmt.Printf("  Started: %s\n", info.ModTime().Format("2006-01-02 15:04:05"))
		}
		
		logPath, err := getLogFilePath("")
		if err == nil {
			if _, err := os.Stat(logPath); err == nil {
				fmt.Printf("  Log: %s\n", logPath)
			}
		}
	} else {
		fmt.Println("✗ Daemon is not running")
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

func startDaemon(interval time.Duration, autoCommit, autoPush bool, logFile, pidFile string) {
	logPath, err := getLogFilePath(logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if os.Getenv("BD_DAEMON_FOREGROUND") == "1" {
		runDaemonLoop(interval, autoCommit, autoPush, logPath, pidFile)
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

	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "BD_DAEMON_FOREGROUND=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	
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

func runDaemonLoop(interval time.Duration, autoCommit, autoPush bool, logPath, pidFile string) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

		if err := exportToJSONL(syncCtx, jsonlPath); err != nil {
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

		if err := importFromJSONL(syncCtx, jsonlPath); err != nil {
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
			return
		case <-ctx.Done():
			log("Context cancelled, shutting down")
			return
		}
	}
}
