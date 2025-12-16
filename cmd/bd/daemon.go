package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage background sync daemon",
	Long: `Manage the background daemon that automatically syncs issues with git remote.

The daemon will:
- Poll for changes at configurable intervals (default: 5 seconds)
- Export pending database changes to JSONL
- Auto-commit changes if --auto-commit flag set
- Auto-push commits if --auto-push flag set
- Pull remote changes periodically
- Auto-import when remote changes detected

Common operations:
  bd daemon --start              Start the daemon (background)
  bd daemon --start --foreground Start in foreground (for systemd/supervisord)
  bd daemon --stop               Stop a running daemon
  bd daemon --stop-all           Stop ALL running bd daemons
  bd daemon --status             Check if daemon is running
  bd daemon --health             Check daemon health and metrics

Run 'bd daemon' with no flags to see available options.`,
	Run: func(cmd *cobra.Command, args []string) {
		start, _ := cmd.Flags().GetBool("start")
		stop, _ := cmd.Flags().GetBool("stop")
		stopAll, _ := cmd.Flags().GetBool("stop-all")
		status, _ := cmd.Flags().GetBool("status")
		health, _ := cmd.Flags().GetBool("health")
		metrics, _ := cmd.Flags().GetBool("metrics")
		interval, _ := cmd.Flags().GetDuration("interval")
		autoCommit, _ := cmd.Flags().GetBool("auto-commit")
		autoPush, _ := cmd.Flags().GetBool("auto-push")
		localMode, _ := cmd.Flags().GetBool("local")
		logFile, _ := cmd.Flags().GetString("log")
		foreground, _ := cmd.Flags().GetBool("foreground")

		// If no operation flags provided, show help
		if !start && !stop && !stopAll && !status && !health && !metrics {
			_ = cmd.Help()
			return
		}

		// If auto-commit/auto-push flags weren't explicitly provided, read from config
		// (skip if --stop, --status, --health, --metrics)
		if start && !stop && !status && !health && !metrics {
			if !cmd.Flags().Changed("auto-commit") {
				if dbPath := beads.FindDatabasePath(); dbPath != "" {
					ctx := context.Background()
					store, err := sqlite.New(ctx, dbPath)
					if err == nil {
						if configVal, err := store.GetConfig(ctx, "daemon.auto_commit"); err == nil && configVal == "true" {
							autoCommit = true
						}
						_ = store.Close()
					}
				}
			}
			if !cmd.Flags().Changed("auto-push") {
				if dbPath := beads.FindDatabasePath(); dbPath != "" {
					ctx := context.Background()
					store, err := sqlite.New(ctx, dbPath)
					if err == nil {
						if configVal, err := store.GetConfig(ctx, "daemon.auto_push"); err == nil && configVal == "true" {
							autoPush = true
						}
						_ = store.Close()
					}
				}
			}
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

		if status {
			showDaemonStatus(pidFile)
			return
		}

		if health {
			showDaemonHealth()
			return
		}

		if metrics {
			showDaemonMetrics()
			return
		}

		if stop {
			stopDaemon(pidFile)
			return
		}

		if stopAll {
			stopAllDaemons()
			return
		}

		// If we get here and --start wasn't provided, something is wrong
		// (should have been caught by help check above)
		if !start {
			fmt.Fprintf(os.Stderr, "Error: --start flag is required to start the daemon\n")
			fmt.Fprintf(os.Stderr, "Run 'bd daemon --help' to see available options\n")
			os.Exit(1)
		}

		// Skip daemon-running check if we're the forked child (BD_DAEMON_FOREGROUND=1)
		// because the check happens in the parent process before forking
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
						fmt.Fprintf(os.Stderr, "Use 'bd daemon --stop' to stop it first\n")
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
					fmt.Fprintf(os.Stderr, "Use 'bd daemon --stop' to stop it first\n")
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
		// Ensure dbPath is set for warning
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
			fmt.Printf("Starting bd daemon (interval: %v, auto-commit: %v, auto-push: %v)\n",
				interval, autoCommit, autoPush)
		}
		if logFile != "" {
			fmt.Printf("Logging to: %s\n", logFile)
		}

		startDaemon(interval, autoCommit, autoPush, localMode, foreground, logFile, pidFile)
	},
}

func init() {
	daemonCmd.Flags().Bool("start", false, "Start the daemon")
	daemonCmd.Flags().Duration("interval", 5*time.Second, "Sync check interval")
	daemonCmd.Flags().Bool("auto-commit", false, "Automatically commit changes")
	daemonCmd.Flags().Bool("auto-push", false, "Automatically push commits")
	daemonCmd.Flags().Bool("local", false, "Run in local-only mode (no git required, no sync)")
	daemonCmd.Flags().Bool("stop", false, "Stop running daemon")
	daemonCmd.Flags().Bool("stop-all", false, "Stop all running bd daemons")
	daemonCmd.Flags().Bool("status", false, "Show daemon status")
	daemonCmd.Flags().Bool("health", false, "Check daemon health and metrics")
	daemonCmd.Flags().Bool("metrics", false, "Show detailed daemon metrics")
	daemonCmd.Flags().String("log", "", "Log file path (default: .beads/daemon.log)")
	daemonCmd.Flags().Bool("foreground", false, "Run in foreground (don't daemonize)")
	daemonCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON format")
	rootCmd.AddCommand(daemonCmd)
}

// computeDaemonParentPID determines which parent PID the daemon should track.
// When BD_DAEMON_FOREGROUND=1 (used by startDaemon for background CLI launches),
// we return 0 to disable parent tracking, since the short-lived launcher
// process is expected to exit immediately after spawning the daemon.
// In all other cases we track the current OS parent PID.
func computeDaemonParentPID() int {
	if os.Getenv("BD_DAEMON_FOREGROUND") == "1" {
		// 0 means "not tracked" in checkParentProcessAlive
		return 0
	}
	return os.Getppid()
}
func runDaemonLoop(interval time.Duration, autoCommit, autoPush, localMode bool, logPath, pidFile string) {
	logF, log := setupDaemonLogger(logPath)
	defer func() { _ = logF.Close() }()

	// Set up signal-aware context for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Top-level panic recovery to ensure clean shutdown and diagnostics
	defer func() {
		if r := recover(); r != nil {
			log.log("PANIC: daemon crashed: %v", r)

			// Capture stack trace
			stackBuf := make([]byte, 4096)
			stackSize := runtime.Stack(stackBuf, false)
			stackTrace := string(stackBuf[:stackSize])
			log.log("Stack trace:\n%s", stackTrace)

			// Write crash report to daemon-error file for user visibility
			var beadsDir string
			if dbPath != "" {
				beadsDir = filepath.Dir(dbPath)
			} else if foundDB := beads.FindDatabasePath(); foundDB != "" {
				beadsDir = filepath.Dir(foundDB)
			}
			
			if beadsDir != "" {
				errFile := filepath.Join(beadsDir, "daemon-error")
				crashReport := fmt.Sprintf("Daemon crashed at %s\n\nPanic: %v\n\nStack trace:\n%s\n",
					time.Now().Format(time.RFC3339), r, stackTrace)
				// nolint:gosec // G306: Error file needs to be readable for debugging
				if err := os.WriteFile(errFile, []byte(crashReport), 0644); err != nil {
					log.log("Warning: could not write crash report: %v", err)
				}
			}
			
			// Clean up PID file
			_ = os.Remove(pidFile)
			
			log.log("Daemon terminated after panic")
		}
	}()

	// Determine database path first (needed for lock file metadata)
	daemonDBPath := dbPath
	if daemonDBPath == "" {
		if foundDB := beads.FindDatabasePath(); foundDB != "" {
			daemonDBPath = foundDB
		} else {
			log.log("Error: no beads database found")
			log.log("Hint: run 'bd init' to create a database or set BEADS_DB environment variable")
			return // Use return instead of os.Exit to allow defers to run
		}
	}

	lock, err := setupDaemonLock(pidFile, daemonDBPath, log)
	if err != nil {
		return // Use return instead of os.Exit to allow defers to run
	}
	defer func() { _ = lock.Close() }()
	defer func() { _ = os.Remove(pidFile) }()

	if localMode {
		log.log("Daemon started in LOCAL mode (interval: %v, no git sync)", interval)
	} else {
		log.log("Daemon started (interval: %v, auto-commit: %v, auto-push: %v)", interval, autoCommit, autoPush)
	}

	// Check for multiple .db files (ambiguity error)
	beadsDir := filepath.Dir(daemonDBPath)
	matches, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
	if err == nil && len(matches) > 1 {
		// Filter out backup files (*.backup-*.db, *.backup.db)
		var validDBs []string
		for _, match := range matches {
			baseName := filepath.Base(match)
			// Skip if it's a backup file (contains ".backup" in name)
			if !strings.Contains(baseName, ".backup") && baseName != "vc.db" {
				validDBs = append(validDBs, match)
			}
		}
		if len(validDBs) > 1 {
			errMsg := fmt.Sprintf("Error: Multiple database files found in %s:\n", beadsDir)
			for _, db := range validDBs {
				errMsg += fmt.Sprintf("  - %s\n", filepath.Base(db))
			}
			errMsg += fmt.Sprintf("\nBeads requires a single canonical database: %s\n", beads.CanonicalDatabaseName)
			errMsg += "Run 'bd init' to migrate legacy databases or manually remove old databases\n"
			errMsg += "Or run 'bd doctor' for more diagnostics"

			log.log(errMsg)

			// Write error to file so user can see it without checking logs
			errFile := filepath.Join(beadsDir, "daemon-error")
			// nolint:gosec // G306: Error file needs to be readable for debugging
			if err := os.WriteFile(errFile, []byte(errMsg), 0644); err != nil {
				log.log("Warning: could not write daemon-error file: %v", err)
			}

			return // Use return instead of os.Exit to allow defers to run
		}
	}

	// Validate using canonical name
	dbBaseName := filepath.Base(daemonDBPath)
	if dbBaseName != beads.CanonicalDatabaseName {
		log.log("Error: Non-canonical database name: %s", dbBaseName)
		log.log("Expected: %s", beads.CanonicalDatabaseName)
		log.log("")
		log.log("Run 'bd init' to migrate to canonical name")
		return // Use return instead of os.Exit to allow defers to run
	}

	log.log("Using database: %s", daemonDBPath)

	// Clear any previous daemon-error file on successful startup
	errFile := filepath.Join(beadsDir, "daemon-error")
	if err := os.Remove(errFile); err != nil && !os.IsNotExist(err) {
		log.log("Warning: could not remove daemon-error file: %v", err)
	}

	store, err := sqlite.New(ctx, daemonDBPath)
	if err != nil {
		log.log("Error: cannot open database: %v", err)
		return // Use return instead of os.Exit to allow defers to run
	}
	defer func() { _ = store.Close() }()

	// Enable freshness checking to detect external database file modifications
	// (e.g., when git merge replaces the database file)
	store.EnableFreshnessChecking()
	log.log("Database opened: %s (freshness checking enabled)", daemonDBPath)

	// Auto-upgrade .beads/.gitignore if outdated
	gitignoreCheck := doctor.CheckGitignore()
	if gitignoreCheck.Status == "warning" || gitignoreCheck.Status == "error" {
		log.log("Upgrading .beads/.gitignore...")
		if err := doctor.FixGitignore(); err != nil {
			log.log("Warning: failed to upgrade .gitignore: %v", err)
		} else {
			log.log("Successfully upgraded .beads/.gitignore")
		}
	}

	// Hydrate from multi-repo if configured
	if results, err := store.HydrateFromMultiRepo(ctx); err != nil {
		log.log("Error: multi-repo hydration failed: %v", err)
		return // Use return instead of os.Exit to allow defers to run
	} else if results != nil {
		log.log("Multi-repo hydration complete:")
		for repo, count := range results {
			log.log("  %s: %d issues", repo, count)
		}
	}

	// Validate database fingerprint (skip in local mode - no git available)
	if localMode {
		log.log("Skipping fingerprint validation (local mode)")
	} else if err := validateDatabaseFingerprint(ctx, store, &log); err != nil {
		if os.Getenv("BEADS_IGNORE_REPO_MISMATCH") != "1" {
			log.log("Error: %v", err)
			return // Use return instead of os.Exit to allow defers to run
		}
		log.log("Warning: repository mismatch ignored (BEADS_IGNORE_REPO_MISMATCH=1)")
	}

	// Validate schema version matches daemon version
	versionCtx := context.Background()
	dbVersion, err := store.GetMetadata(versionCtx, "bd_version")
	if err != nil && err.Error() != "metadata key not found: bd_version" {
		log.log("Error: failed to read database version: %v", err)
		return // Use return instead of os.Exit to allow defers to run
	}

	if dbVersion != "" && dbVersion != Version {
		log.log("Warning: Database schema version mismatch")
		log.log("  Database version: %s", dbVersion)
		log.log("  Daemon version: %s", Version)
		log.log("  Auto-upgrading database to daemon version...")

		// Auto-upgrade database to daemon version
		// The daemon operates on its own database, so it should always use its own version
		if err := store.SetMetadata(versionCtx, "bd_version", Version); err != nil {
			log.log("Error: failed to update database version: %v", err)

			// Allow override via environment variable for emergencies
			if os.Getenv("BEADS_IGNORE_VERSION_MISMATCH") != "1" {
				return // Use return instead of os.Exit to allow defers to run
			}
			log.log("Warning: Proceeding despite version update failure (BEADS_IGNORE_VERSION_MISMATCH=1)")
		} else {
			log.log("  Database version updated to %s", Version)
		}
	} else if dbVersion == "" {
		// Old database without version metadata - set it now
		log.log("Warning: Database missing version metadata, setting to %s", Version)
		if err := store.SetMetadata(versionCtx, "bd_version", Version); err != nil {
			log.log("Error: failed to set database version: %v", err)
			return // Use return instead of os.Exit to allow defers to run
		}
	}

	// Get workspace path (.beads directory) - beadsDir already defined above
	// Get actual workspace root (parent of .beads)
	workspacePath := filepath.Dir(beadsDir)
	socketPath := filepath.Join(beadsDir, "bd.sock")
	serverCtx, serverCancel := context.WithCancel(ctx)
	defer serverCancel()

	server, serverErrChan, err := startRPCServer(serverCtx, socketPath, store, workspacePath, daemonDBPath, log)
	if err != nil {
		return
	}

	// Choose event loop based on BEADS_DAEMON_MODE (need to determine early for SetConfig)
	daemonMode := os.Getenv("BEADS_DAEMON_MODE")
	if daemonMode == "" {
		daemonMode = "events" // Default to event-driven mode (production-ready as of v0.21.0)
	}

	// Set daemon configuration for status reporting
	server.SetConfig(autoCommit, autoPush, localMode, interval.String(), daemonMode)

	// Register daemon in global registry
	registry, err := daemon.NewRegistry()
	if err != nil {
		log.log("Warning: failed to create registry: %v", err)
	} else {
		entry := daemon.RegistryEntry{
			WorkspacePath: workspacePath,
			SocketPath:    socketPath,
			DatabasePath:  daemonDBPath,
			PID:           os.Getpid(),
			Version:       Version,
			StartedAt:     time.Now(),
		}
		if err := registry.Register(entry); err != nil {
			log.log("Warning: failed to register daemon: %v", err)
		} else {
			log.log("Registered in global registry")
		}
		// Ensure we unregister on exit
		defer func() {
			if err := registry.Unregister(workspacePath, os.Getpid()); err != nil {
				log.log("Warning: failed to unregister daemon: %v", err)
			}
		}()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Create sync function based on mode
	var doSync func()
	if localMode {
		doSync = createLocalSyncFunc(ctx, store, log)
	} else {
		doSync = createSyncFunc(ctx, store, autoCommit, autoPush, log)
	}
	doSync()

	// Get parent PID for monitoring (exit if parent dies)
	parentPID := computeDaemonParentPID()
	log.log("Monitoring parent process (PID %d)", parentPID)

	// daemonMode already determined above for SetConfig
	switch daemonMode {
	case "events":
		log.log("Using event-driven mode")
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.log("Error: JSONL path not found, cannot use event-driven mode")
			log.log("Falling back to polling mode")
			runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, parentPID, log)
		} else {
			// Event-driven mode uses separate export-only and import-only functions
			var doExport, doAutoImport func()
			if localMode {
				doExport = createLocalExportFunc(ctx, store, log)
				doAutoImport = createLocalAutoImportFunc(ctx, store, log)
			} else {
				doExport = createExportFunc(ctx, store, autoCommit, autoPush, log)
				doAutoImport = createAutoImportFunc(ctx, store, log)
			}
			runEventDrivenLoop(ctx, cancel, server, serverErrChan, store, jsonlPath, doExport, doAutoImport, parentPID, log)
		}
	case "poll":
		log.log("Using polling mode (interval: %v)", interval)
		runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, parentPID, log)
	default:
		log.log("Unknown BEADS_DAEMON_MODE: %s (valid: poll, events), defaulting to poll", daemonMode)
		runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, parentPID, log)
	}
}
