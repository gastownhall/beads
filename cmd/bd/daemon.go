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
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/syncbranch"
)

var daemonCmd = &cobra.Command{
	Use:     "daemon",
	GroupID: "sync",
	Short:   "Manage background sync daemon",
	Long: `Manage the background daemon that automatically syncs issues with git remote.

The daemon will:
- Poll for changes at configurable intervals (default: 5 seconds)
- Export pending database changes to JSONL
- Auto-commit changes if --auto-commit flag set
- Auto-push commits if --auto-push flag set
- Pull remote changes periodically
- Auto-import when remote changes detected

Common operations:
  bd daemon start                Start the daemon (background)
  bd daemon start --foreground   Start in foreground (for systemd/supervisord)
  bd daemon stop                 Stop current workspace daemon
  bd daemon status               Show daemon status
  bd daemon status --all         Show all daemons with health check
  bd daemon logs                 View daemon logs
  bd daemon restart              Restart daemon
  bd daemon killall              Stop all running daemons

Run 'bd daemon --help' to see all subcommands.`,
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
		autoPull, _ := cmd.Flags().GetBool("auto-pull")
		localMode, _ := cmd.Flags().GetBool("local")
		logFile, _ := cmd.Flags().GetString("log")
		foreground, _ := cmd.Flags().GetBool("foreground")
		logLevel, _ := cmd.Flags().GetString("log-level")
		logJSON, _ := cmd.Flags().GetBool("log-json")
		federation, _ := cmd.Flags().GetBool("federation")

		// If no operation flags provided, show help
		if !start && !stop && !stopAll && !status && !health && !metrics {
			_ = cmd.Help()
			return
		}

		// Show deprecation warnings for flag-based actions (skip in JSON mode for agent ergonomics)
		if !jsonOutput {
			if start {
				fmt.Fprintf(os.Stderr, "Warning: --start is deprecated, use 'bd daemon start' instead\n")
			}
			if stop {
				fmt.Fprintf(os.Stderr, "Warning: --stop is deprecated, use 'bd daemon stop' instead\n")
			}
			if stopAll {
				fmt.Fprintf(os.Stderr, "Warning: --stop-all is deprecated, use 'bd daemon killall' instead\n")
			}
			if status {
				fmt.Fprintf(os.Stderr, "Warning: --status is deprecated, use 'bd daemon status' instead\n")
			}
			if health {
				fmt.Fprintf(os.Stderr, "Warning: --health is deprecated, use 'bd daemon status --all' instead\n")
			}
		}

		// If auto-commit/auto-push flags weren't explicitly provided, read from config
		// GH#871: Read from config.yaml first (team-shared), then fall back to SQLite (legacy)
		// (skip if --stop, --status, --health, --metrics)
		if start && !stop && !status && !health && !metrics {
			// Load auto-commit/push/pull defaults from env vars, config, or sync-branch
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

		// Guard: refuse to start daemon with Dolt backend (unless --federation)
		// This matches guardDaemonStartForDolt which guards the 'bd daemon start' subcommand.
		if !federation {
			if err := guardDaemonStartForDolt(cmd, args); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
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
						fmt.Fprintf(os.Stderr, "Use 'bd daemon stop' to stop it first\n")
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
		// When sync-branch is configured, check that branch's upstream instead of current HEAD.
		// This fixes compatibility with jj/jujutsu which always operates in detached HEAD mode.
		if autoPush {
			hasUpstream := false
			if syncBranch := syncbranch.GetFromYAML(); syncBranch != "" {
				// sync-branch configured: check that branch's upstream
				hasUpstream = gitBranchHasUpstream(syncBranch)
			} else {
				// No sync-branch: check current HEAD's upstream (original behavior)
				hasUpstream = gitHasUpstream()
			}
			if !hasUpstream {
				fmt.Fprintf(os.Stderr, "Error: no upstream configured (required for --auto-push)\n")
				fmt.Fprintf(os.Stderr, "Hint: git push -u origin <branch-name>\n")
				os.Exit(1)
			}
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
			fmt.Printf("Starting bd daemon (interval: %v, auto-commit: %v, auto-push: %v, auto-pull: %v)\n",
				interval, autoCommit, autoPush, autoPull)
		}
		if logFile != "" {
			fmt.Printf("Logging to: %s\n", logFile)
		}

		federationPort, _ := cmd.Flags().GetInt("federation-port")
		remotesapiPort, _ := cmd.Flags().GetInt("remotesapi-port")
		startDaemon(interval, autoCommit, autoPush, autoPull, localMode, foreground, logFile, pidFile, logLevel, logJSON, federation, federationPort, remotesapiPort)
	},
}

func init() {
	// Register subcommands (preferred interface)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	// Note: stop, restart, logs, killall, list, health subcommands are registered in daemons.go

	// Legacy flags (deprecated - use subcommands instead)
	daemonCmd.Flags().Bool("start", false, "Start the daemon (deprecated: use 'bd daemon start')")
	daemonCmd.Flags().Duration("interval", 5*time.Second, "Sync check interval")
	daemonCmd.Flags().Bool("auto-commit", false, "Automatically commit changes")
	daemonCmd.Flags().Bool("auto-push", false, "Automatically push commits")
	daemonCmd.Flags().Bool("auto-pull", false, "Automatically pull from remote (default: true when sync.branch configured)")
	daemonCmd.Flags().Bool("local", false, "Run in local-only mode (no git required, no sync)")
	daemonCmd.Flags().Bool("stop", false, "Stop running daemon (deprecated: use 'bd daemon stop')")
	daemonCmd.Flags().Bool("stop-all", false, "Stop all running bd daemons (deprecated: use 'bd daemon killall')")
	daemonCmd.Flags().Bool("status", false, "Show daemon status (deprecated: use 'bd daemon status')")
	daemonCmd.Flags().Bool("health", false, "Check daemon health (deprecated: use 'bd daemon status --all')")
	daemonCmd.Flags().Bool("metrics", false, "Show detailed daemon metrics")
	daemonCmd.Flags().String("log", "", "Log file path (default: .beads/daemon.log)")
	daemonCmd.Flags().Bool("foreground", false, "Run in foreground (don't daemonize)")
	daemonCmd.Flags().String("log-level", "info", "Log level (debug, info, warn, error)")
	daemonCmd.Flags().Bool("log-json", false, "Output logs in JSON format (structured logging)")
	daemonCmd.Flags().Bool("federation", false, "Enable federation mode (runs dolt sql-server with remotesapi)")
	daemonCmd.Flags().Int("federation-port", 3306, "MySQL port for federation mode dolt sql-server")
	daemonCmd.Flags().Int("remotesapi-port", 8080, "remotesapi port for peer-to-peer sync in federation mode")
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
func runDaemonLoop(interval time.Duration, autoCommit, autoPush, autoPull, localMode bool, logPath, pidFile, logLevel string, logJSON, federation bool, federationPort, remotesapiPort int) {
	level := parseLogLevel(logLevel)
	logF, log := setupDaemonLogger(logPath, logJSON, level)
	defer func() { _ = logF.Close() }()

	// Set up signal-aware context for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Top-level panic recovery to ensure clean shutdown and diagnostics
	defer func() {
		if r := recover(); r != nil {
			log.Error("daemon crashed", "panic", r)

			// Capture stack trace
			stackBuf := make([]byte, 4096)
			stackSize := runtime.Stack(stackBuf, false)
			stackTrace := string(stackBuf[:stackSize])
			log.Error("stack trace", "trace", stackTrace)

			var beadsDir string
			if dbPath != "" {
				beadsDir = filepath.Dir(dbPath)
			} else if foundDB := beads.FindDatabasePath(); foundDB != "" {
				beadsDir = filepath.Dir(foundDB)
			}

			if beadsDir != "" {
				crashReport := fmt.Sprintf("Daemon crashed at %s\n\nPanic: %v\n\nStack trace:\n%s\n",
					time.Now().Format(time.RFC3339), r, stackTrace)
				log.Error("crash report", "report", crashReport)
			}

			// Clean up PID file
			_ = os.Remove(pidFile)

			log.Info("daemon terminated after panic")
		}
	}()

	// Determine database path first (needed for lock file metadata)
	daemonDBPath := dbPath
	if daemonDBPath == "" {
		if foundDB := beads.FindDatabasePath(); foundDB != "" {
			daemonDBPath = foundDB
		} else {
			log.Error("no beads database found")
			log.Info("hint: run 'bd init' to create a database or set BEADS_DB environment variable")
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
	backend := factory.GetBackendFromConfig(beadsDir)
	if backend == "" {
		backend = configfile.BackendSQLite
	}

	// Daemon is not supported with dolt backend - refuse to start
	if backend == configfile.BackendDolt {
		errMsg := `DAEMON NOT SUPPORTED WITH DOLT BACKEND

The bd daemon is designed for SQLite backend only.
With dolt backend, connect directly to the dolt sql-server.

The daemon will now exit.`
		log.Error(errMsg)

		// Write error to file so user can see it without checking logs
		errFile := filepath.Join(beadsDir, "daemon-error")
		// nolint:gosec // G306: Error file needs to be readable for debugging
		if err := os.WriteFile(errFile, []byte(errMsg), 0644); err != nil {
			log.Warn("could not write daemon-error file", "error", err)
		}
		return
	}

	// Reset backoff on daemon start (fresh start, but preserve NeedsManualSync hint)
	if !localMode {
		ResetBackoffOnDaemonStart(beadsDir)
	}

	// Check for multiple .db files (ambiguity error) - SQLite only.
	// Dolt is directory-backed so this check is irrelevant and can be misleading.
	if backend == configfile.BackendSQLite {
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
					log.Warn("could not write daemon-error file", "error", err)
				}

				return // Use return instead of os.Exit to allow defers to run
			}
		}
	}

	// Validate using canonical name (SQLite only).
	// Dolt uses a directory-backed store (typically .beads/dolt), so the "beads.db"
	// basename invariant does not apply.
	if backend == configfile.BackendSQLite {
		dbBaseName := filepath.Base(daemonDBPath)
		if dbBaseName != beads.CanonicalDatabaseName {
			log.Error("non-canonical database name", "name", dbBaseName, "expected", beads.CanonicalDatabaseName)
			log.Info("run 'bd init' to migrate to canonical name")
			return // Use return instead of os.Exit to allow defers to run
		}
	}

	log.Info("using database", "path", daemonDBPath)

	// Clear any previous daemon-error file on successful startup
	errFile := filepath.Join(beadsDir, "daemon-error")
	if err := os.Remove(errFile); err != nil && !os.IsNotExist(err) {
		log.Warn("could not remove daemon-error file", "error", err)
	}

	// Start dolt sql-server if federation mode is enabled and backend is dolt
	var doltServer *DoltServerHandle
	factoryOpts := factory.Options{}
	if federation && backend != configfile.BackendDolt {
		log.Warn("federation mode requires dolt backend, ignoring --federation flag")
		federation = false
	}
	if federation && backend == configfile.BackendDolt {
		if !DoltServerAvailable() {
			log.Error("federation mode requires CGO; use pre-built binaries from GitHub releases")
			return
		}
		log.Info("starting dolt sql-server for federation mode")

		doltPath := filepath.Join(beadsDir, "dolt")
		serverLogFile := filepath.Join(beadsDir, "dolt-server.log")

		// Use provided ports or defaults
		sqlPort := federationPort
		if sqlPort == 0 {
			sqlPort = DoltDefaultSQLPort
		}
		remotePort := remotesapiPort
		if remotePort == 0 {
			remotePort = DoltDefaultRemotesAPIPort
		}

		var err error
		doltServer, err = StartDoltServer(ctx, doltPath, serverLogFile, sqlPort, remotePort)
		if err != nil {
			log.Error("failed to start dolt sql-server", "error", err)
			return
		}
		defer func() {
			log.Info("stopping dolt sql-server")
			if err := doltServer.Stop(); err != nil {
				log.Warn("error stopping dolt sql-server", "error", err)
			}
		}()

		log.Info("dolt sql-server started",
			"sql_port", doltServer.SQLPort(),
			"remotesapi_port", doltServer.RemotesAPIPort())

		// Configure factory to use server mode
		factoryOpts.ServerMode = true
		factoryOpts.ServerHost = doltServer.Host()
		factoryOpts.ServerPort = doltServer.SQLPort()
	}

	store, err := factory.NewFromConfigWithOptions(ctx, beadsDir, factoryOpts)
	if err != nil {
		log.Error("cannot open database", "error", err)
		return // Use return instead of os.Exit to allow defers to run
	}
	defer func() { _ = store.Close() }()

	// Enable freshness checking for SQLite backend to detect external database file modifications
	// (e.g., when git merge replaces the database file)
	// Dolt doesn't need this since it handles versioning natively.
	if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
		sqliteStore.EnableFreshnessChecking()
		log.Info("database opened", "path", store.Path(), "backend", "sqlite", "freshness_checking", true)
	} else if federation {
		log.Info("database opened", "path", store.Path(), "backend", "dolt", "mode", "federation/server")
	} else {
		log.Info("database opened", "path", store.Path(), "backend", "dolt", "mode", "embedded")
	}

	// Auto-upgrade .beads/.gitignore if outdated
	gitignoreCheck := doctor.CheckGitignore()
	if gitignoreCheck.Status == "warning" || gitignoreCheck.Status == "error" {
		log.Info("upgrading .beads/.gitignore")
		if err := doctor.FixGitignore(); err != nil {
			log.Warn("failed to upgrade .gitignore", "error", err)
		} else {
			log.Info("successfully upgraded .beads/.gitignore")
		}
	}

	// Hydrate from multi-repo if configured (SQLite only)
	if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
		if results, err := sqliteStore.HydrateFromMultiRepo(ctx); err != nil {
			log.Error("multi-repo hydration failed", "error", err)
			return // Use return instead of os.Exit to allow defers to run
		} else if results != nil {
			log.Info("multi-repo hydration complete")
			for repo, count := range results {
				log.Info("hydrated issues", "repo", repo, "count", count)
			}
		}
	}

	// Validate database fingerprint (skip in local mode - no git available)
	if localMode {
		log.Info("skipping fingerprint validation (local mode)")
	} else if err := validateDatabaseFingerprint(ctx, store, &log); err != nil {
		if os.Getenv("BEADS_IGNORE_REPO_MISMATCH") != "1" {
			log.Error("repository fingerprint validation failed", "error", err)
			// Write error to daemon-error file so user sees it instead of just "daemon took too long"
			errFile := filepath.Join(beadsDir, "daemon-error")
			// nolint:gosec // G306: Error file needs to be readable for debugging
			if writeErr := os.WriteFile(errFile, []byte(err.Error()), 0644); writeErr != nil {
				log.Warn("could not write daemon-error file", "error", writeErr)
			}
			return // Use return instead of os.Exit to allow defers to run
		}
		log.Warn("repository mismatch ignored (BEADS_IGNORE_REPO_MISMATCH=1)")
	}

	// GH#1258: Warn at startup if sync-branch == current-branch (misconfiguration)
	// This is a one-time warning - per-operation skipping is handled by shouldSkipDueToSameBranch()
	// Skip check in local mode (no sync-branch is used)
	if !localMode {
		warnIfSyncBranchMisconfigured(ctx, store, log)
	}

	// Validate schema version matches daemon version
	versionCtx := context.Background()
	dbVersion, err := store.GetMetadata(versionCtx, "bd_version")
	if err != nil && err.Error() != "metadata key not found: bd_version" {
		log.Error("failed to read database version", "error", err)
		return // Use return instead of os.Exit to allow defers to run
	}

	if dbVersion != "" && dbVersion != Version {
		log.Warn("database schema version mismatch", "db_version", dbVersion, "daemon_version", Version)
		log.Info("auto-upgrading database to daemon version")

		// Auto-upgrade database to daemon version
		// The daemon operates on its own database, so it should always use its own version
		if err := store.SetMetadata(versionCtx, "bd_version", Version); err != nil {
			log.Error("failed to update database version", "error", err)

			// Allow override via environment variable for emergencies
			if os.Getenv("BEADS_IGNORE_VERSION_MISMATCH") != "1" {
				return // Use return instead of os.Exit to allow defers to run
			}
			log.Warn("proceeding despite version update failure (BEADS_IGNORE_VERSION_MISMATCH=1)")
		} else {
			log.Info("database version updated", "version", Version)
		}
	} else if dbVersion == "" {
		// Old database without version metadata - set it now
		log.Warn("database missing version metadata", "setting_to", Version)
		if err := store.SetMetadata(versionCtx, "bd_version", Version); err != nil {
			log.Error("failed to set database version", "error", err)
			return // Use return instead of os.Exit to allow defers to run
		}
	}

	// Get workspace path (.beads directory) - beadsDir already defined above
	// Get actual workspace root (parent of .beads)
	workspacePath := filepath.Dir(beadsDir)
	// Use short socket path to avoid Unix socket path length limits (macOS: 104 chars)
	socketPath, err := rpc.EnsureSocketDir(rpc.ShortSocketPath(workspacePath))
	if err != nil {
		log.Error("failed to create socket directory", "error", err)
		return
	}
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
	server.SetConfig(autoCommit, autoPush, autoPull, localMode, interval.String(), daemonMode)

	// Register daemon in global registry
	registry, err := daemon.NewRegistry()
	if err != nil {
		log.Warn("failed to create registry", "error", err)
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
			log.Warn("failed to register daemon", "error", err)
		} else {
			log.Info("registered in global registry")
		}
		// Ensure we unregister on exit
		defer func() {
			if err := registry.Unregister(workspacePath, os.Getpid()); err != nil {
				log.Warn("failed to unregister daemon", "error", err)
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
	log.Info("monitoring parent process", "pid", parentPID)

	// daemonMode already determined above for SetConfig
	switch daemonMode {
	case "events":
		log.Info("using event-driven mode")
		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.Error("JSONL path not found, cannot use event-driven mode")
			log.Info("falling back to polling mode")
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
			runEventDrivenLoop(ctx, cancel, server, serverErrChan, store, jsonlPath, doExport, doAutoImport, autoPull, parentPID, log)
		}
	case "poll":
		log.Info("using polling mode", "interval", interval)
		runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, parentPID, log)
	default:
		log.Warn("unknown BEADS_DAEMON_MODE, defaulting to poll", "mode", daemonMode, "valid", "poll, events")
		runEventLoop(ctx, cancel, ticker, doSync, server, serverErrChan, parentPID, log)
	}
}

// loadDaemonAutoSettings loads daemon sync mode settings.
//
// # Two Sync Modes
//
// Read/Write Mode (full sync):
//
//	daemon.auto-sync: true  (or BEADS_AUTO_SYNC=true)
//
// Enables auto-commit, auto-push, AND auto-pull. Full bidirectional sync
// with team. Eliminates need for manual `bd sync`. This is the default
// when sync-branch is configured.
//
// Read-Only Mode:
//
//	daemon.auto-pull: true  (or BEADS_AUTO_PULL=true)
//
// Only enables auto-pull (receive updates from team). Does NOT auto-publish
// your changes. Useful for experimental work or manual review before sharing.
//
// # Precedence
//
// 1. auto-sync=true → Read/Write mode (all three ON, no exceptions)
// 2. auto-sync=false → Write-side OFF, auto-pull can still be enabled
// 3. auto-sync not set → Legacy compat mode:
//   - If either BEADS_AUTO_COMMIT/daemon.auto_commit or BEADS_AUTO_PUSH/daemon.auto_push
//     is enabled, treat as auto-sync=true (full read/write)
//   - Otherwise check auto-pull for read-only mode
//
// 4. Fallback: all default to true when sync-branch configured
//
// Note: The individual auto-commit/auto-push settings are deprecated.
// Use auto-sync for read/write mode, auto-pull for read-only mode.
func loadDaemonAutoSettings(cmd *cobra.Command, autoCommit, autoPush, autoPull bool) (bool, bool, bool) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return autoCommit, autoPush, autoPull
	}

	ctx := context.Background()
	store, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		return autoCommit, autoPush, autoPull
	}
	defer func() { _ = store.Close() }()

	// Check if sync-branch is configured (used for defaults)
	syncBranch, _ := store.GetConfig(ctx, "sync.branch")
	hasSyncBranch := syncBranch != ""

	// Check unified auto-sync setting first (controls auto-commit + auto-push)
	unifiedAutoSync := ""
	if envVal := os.Getenv("BEADS_AUTO_SYNC"); envVal != "" {
		unifiedAutoSync = envVal
	} else if configVal, _ := store.GetConfig(ctx, "daemon.auto-sync"); configVal != "" {
		unifiedAutoSync = configVal
	}

	// Handle unified auto-sync setting
	if unifiedAutoSync != "" {
		enabled := unifiedAutoSync == "true" || unifiedAutoSync == "1"
		if enabled {
			// auto-sync=true: MASTER CONTROL, forces all three ON
			// Individual CLI flags are ignored - you said "full sync"
			autoCommit = true
			autoPush = true
			autoPull = true
			return autoCommit, autoPush, autoPull
		}
		// auto-sync=false: Write-side (commit/push) locked OFF
		// Only auto-pull can be individually enabled (for read-only mode)
		autoCommit = false
		autoPush = false
		// Auto-pull can still be enabled via CLI flag or individual config
		if cmd.Flags().Changed("auto-pull") {
			// Use the CLI flag value (already in autoPull)
		} else if envVal := os.Getenv("BEADS_AUTO_PULL"); envVal != "" {
			autoPull = envVal == "true" || envVal == "1"
		} else if configVal, _ := store.GetConfig(ctx, "daemon.auto-pull"); configVal != "" {
			autoPull = configVal == "true"
		} else if configVal, _ := store.GetConfig(ctx, "daemon.auto_pull"); configVal != "" {
			autoPull = configVal == "true"
		} else if hasSyncBranch {
			// Default auto-pull to true when sync-branch configured
			autoPull = true
		} else {
			autoPull = false
		}
		return autoCommit, autoPush, autoPull
	}

	// No unified setting - check legacy individual settings for backward compat
	// If either legacy auto-commit or auto-push is enabled, treat as auto-sync=true
	legacyCommit := false
	legacyPush := false

	// Check legacy auto-commit (env var or config)
	if envVal := os.Getenv("BEADS_AUTO_COMMIT"); envVal != "" {
		legacyCommit = envVal == "true" || envVal == "1"
	} else if configVal, _ := store.GetConfig(ctx, "daemon.auto_commit"); configVal != "" {
		legacyCommit = configVal == "true"
	}

	// Check legacy auto-push (env var or config)
	if envVal := os.Getenv("BEADS_AUTO_PUSH"); envVal != "" {
		legacyPush = envVal == "true" || envVal == "1"
	} else if configVal, _ := store.GetConfig(ctx, "daemon.auto_push"); configVal != "" {
		legacyPush = configVal == "true"
	}

	// If either legacy write-side option is enabled, enable full auto-sync
	// (backward compat: user wanted writes, so give them full sync)
	if legacyCommit || legacyPush {
		autoCommit = true
		autoPush = true
		autoPull = true
		return autoCommit, autoPush, autoPull
	}

	// Neither legacy write option enabled - check auto-pull for read-only mode
	if !cmd.Flags().Changed("auto-pull") {
		if envVal := os.Getenv("BEADS_AUTO_PULL"); envVal != "" {
			autoPull = envVal == "true" || envVal == "1"
		} else if configVal, _ := store.GetConfig(ctx, "daemon.auto-pull"); configVal != "" {
			autoPull = configVal == "true"
		} else if configVal, _ := store.GetConfig(ctx, "daemon.auto_pull"); configVal != "" {
			autoPull = configVal == "true"
		} else if hasSyncBranch {
			// Default auto-pull to true when sync-branch configured
			autoPull = true
		}
	}

	// Fallback: if sync-branch configured and no explicit settings, default to full sync
	if hasSyncBranch && !cmd.Flags().Changed("auto-commit") && !cmd.Flags().Changed("auto-push") {
		autoCommit = true
		autoPush = true
		autoPull = true
	}

	return autoCommit, autoPush, autoPull
}
