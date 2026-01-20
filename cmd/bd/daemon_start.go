package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/rpc"
)

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the background daemon",
	Long: `Start the background daemon that automatically syncs issues with git remote.

The daemon will:
- Poll for changes at configurable intervals (default: 5 seconds)
- Export pending database changes to JSONL
- Auto-commit changes if --auto-commit flag set
- Auto-push commits if --auto-push flag set
- Pull remote changes periodically
- Auto-import when remote changes detected

Examples:
  bd daemon start                    # Start with defaults
  bd daemon start --auto-commit      # Enable auto-commit
  bd daemon start --auto-push        # Enable auto-push (implies --auto-commit)
  bd daemon start --foreground       # Run in foreground (for systemd/supervisord)
  bd daemon start --local            # Local-only mode (no git sync)`,
	Run: func(cmd *cobra.Command, args []string) {
		interval, _ := cmd.Flags().GetDuration("interval")
		autoCommit, _ := cmd.Flags().GetBool("auto-commit")
		autoPush, _ := cmd.Flags().GetBool("auto-push")
		autoPull, _ := cmd.Flags().GetBool("auto-pull")
		localMode, _ := cmd.Flags().GetBool("local")
		logFile, _ := cmd.Flags().GetString("log")
		foreground, _ := cmd.Flags().GetBool("foreground")
		logLevel, _ := cmd.Flags().GetString("log-level")
		logJSON, _ := cmd.Flags().GetBool("log-json")

		// NOTE: Only load daemon auto-settings from the database in foreground mode.
		//
		// In background mode, `bd daemon start` spawns a child process to run the
		// daemon loop. Opening the database here in the parent process can briefly
		// hold Dolt's LOCK file long enough for the child to time out and fall back
		// to read-only mode (100ms lock timeout), which can break startup.
		//
		// In background mode, auto-settings are loaded in the actual daemon process
		// (the BD_DAEMON_FOREGROUND=1 child spawned by startDaemon).
		if foreground {
			autoCommit, autoPush, autoPull = loadDaemonAutoSettings(cmd, autoCommit, autoPush, autoPull)
		}

		if interval <= 0 {
			fmt.Fprintf(os.Stderr, "Error: interval must be positive (got %v)\n", interval)
			os.Exit(1)
		}

		pidFile, err := getPIDFilePath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Skip daemon-running check if we're the forked child (BD_DAEMON_FOREGROUND=1)
		if os.Getenv("BD_DAEMON_FOREGROUND") != "1" {
			// Check if daemon is already running
			if isRunning, pid := isDaemonRunning(pidFile); isRunning {
				// Check if running daemon has compatible version
				socketPath := getSocketPathForPID(pidFile)
				if client, err := rpc.TryConnectWithTimeout(socketPath, 1*time.Second); err == nil && client != nil {
					health, healthErr := client.Health()
					_ = client.Close()

					// If we can check version and it's compatible, exit
					if healthErr == nil && health.Compatible {
						fmt.Fprintf(os.Stderr, "Error: daemon already running (PID %d, version %s)\n", pid, health.Version)
						fmt.Fprintf(os.Stderr, "Use 'bd daemon stop' to stop it first\n")
						os.Exit(1)
					}

					// Version mismatch - auto-stop old daemon
					if healthErr == nil && !health.Compatible {
						fmt.Fprintf(os.Stderr, "Warning: daemon version mismatch (daemon: %s, client: %s)\n", health.Version, Version)
						fmt.Fprintf(os.Stderr, "Stopping old daemon and starting new one...\n")
						stopDaemon(pidFile)
					}
				} else {
					fmt.Fprintf(os.Stderr, "Error: daemon already running (PID %d)\n", pid)
					fmt.Fprintf(os.Stderr, "Use 'bd daemon stop' to stop it first\n")
					os.Exit(1)
				}
			}
		}

		// Validate --local mode constraints
		if localMode {
			if autoCommit {
				fmt.Fprintf(os.Stderr, "Error: --auto-commit cannot be used with --local mode\n")
				fmt.Fprintf(os.Stderr, "Hint: --local mode runs without git, so commits are not possible\n")
				os.Exit(1)
			}
			if autoPush {
				fmt.Fprintf(os.Stderr, "Error: --auto-push cannot be used with --local mode\n")
				fmt.Fprintf(os.Stderr, "Hint: --local mode runs without git, so pushes are not possible\n")
				os.Exit(1)
			}
		}

		// Validate we're in a git repo (skip in local mode)
		if !localMode && !isGitRepo() {
			fmt.Fprintf(os.Stderr, "Error: not in a git repository\n")
			fmt.Fprintf(os.Stderr, "Hint: run 'git init' to initialize a repository, or use --local for local-only mode\n")
			os.Exit(1)
		}

		// Check for upstream if auto-push enabled
		if autoPush && !gitHasUpstream() {
			fmt.Fprintf(os.Stderr, "Error: no upstream configured (required for --auto-push)\n")
			fmt.Fprintf(os.Stderr, "Hint: git push -u origin <branch-name>\n")
			os.Exit(1)
		}

		// Warn if starting daemon in a git worktree
		if dbPath == "" {
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				dbPath = foundDB
			}
		}
		if dbPath != "" {
			warnWorktreeDaemon(dbPath)
		}

		// Start daemon
		if localMode {
			fmt.Printf("Starting bd daemon in LOCAL mode (interval: %v, no git sync)\n", interval)
		} else {
			fmt.Printf("Starting bd daemon (interval: %v, auto-commit: %v, auto-push: %v, auto-pull: %v)\n",
				interval, autoCommit, autoPush, autoPull)
		}
		if logFile != "" {
			fmt.Printf("Logging to: %s\n", logFile)
		}

		startDaemon(interval, autoCommit, autoPush, autoPull, localMode, foreground, logFile, pidFile, logLevel, logJSON)
	},
}

func init() {
	daemonStartCmd.Flags().Duration("interval", 5*time.Second, "Sync check interval")
	daemonStartCmd.Flags().Bool("auto-commit", false, "Automatically commit changes")
	daemonStartCmd.Flags().Bool("auto-push", false, "Automatically push commits")
	daemonStartCmd.Flags().Bool("auto-pull", false, "Automatically pull from remote")
	daemonStartCmd.Flags().Bool("local", false, "Run in local-only mode (no git required, no sync)")
	daemonStartCmd.Flags().String("log", "", "Log file path (default: .beads/daemon.log)")
	daemonStartCmd.Flags().Bool("foreground", false, "Run in foreground (don't daemonize)")
	daemonStartCmd.Flags().String("log-level", "info", "Log level (debug, info, warn, error)")
	daemonStartCmd.Flags().Bool("log-json", false, "Output logs in JSON format")
}
