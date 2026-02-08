package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/molecules"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/utils"
)

var (
	dbPath       string
	actor        string
	store        storage.Storage
	jsonOutput   bool
	daemonStatus DaemonStatus // Tracks daemon connection state for current command

	// Daemon mode
	daemonClient *rpc.Client // RPC client when daemon is running
	noDaemon     bool        // Force direct mode (no daemon)

	// Signal-aware context for graceful cancellation
	rootCtx    context.Context
	rootCancel context.CancelFunc

	// Auto-flush state
	autoFlushEnabled  = true // Can be disabled with --no-auto-flush
	flushMutex        sync.Mutex
	storeMutex        sync.Mutex // Protects store access from background goroutine
	storeActive       = false    // Tracks if store is available
	flushFailureCount = 0        // Consecutive flush failures
	lastFlushError    error      // Last flush error for debugging

	// Auto-flush manager (event-driven, fixes race condition)
	flushManager *FlushManager

	// Hook runner for extensibility
	hookRunner *hooks.Runner

	// skipFinalFlush is set by sync command when sync.branch mode completes successfully.
	// This prevents PersistentPostRun from re-exporting and dirtying the working directory.
	skipFinalFlush = false

	// Auto-import state
	autoImportEnabled = true // Can be disabled with --no-auto-import

	// Version upgrade tracking
	versionUpgradeDetected = false // Set to true if bd version changed since last run
	previousVersion        = ""    // The last bd version user had (empty = first run or unknown)
	upgradeAcknowledged    = false // Set to true after showing upgrade notification once per session
)
var (
	noAutoFlush     bool
	noAutoImport    bool
	sandboxMode     bool
	allowStale      bool          // Use --allow-stale: skip staleness check (emergency escape hatch)
	noDb            bool          // Use --no-db mode: load from JSONL, write back after each command
	readonlyMode    bool          // Read-only mode: block write operations (for worker sandboxes)
	storeIsReadOnly bool          // Track if store was opened read-only (for staleness checks)
	lockTimeout     time.Duration // SQLite busy_timeout (default 30s, 0 = fail immediately)
	profileEnabled  bool
	profileFile     *os.File
	traceFile       *os.File
	verboseFlag     bool // Enable verbose/debug output
	quietFlag       bool // Suppress non-essential output

	// Dolt auto-commit policy (flag/config). Values: off | on
	doltAutoCommit string

	// commandDidWrite is set when a command performs a write that should trigger
	// auto-flush. Used to decide whether to auto-commit Dolt after the command completes.
	// Thread-safe via atomic.Bool to avoid data races in concurrent flush operations.
	commandDidWrite atomic.Bool

	// commandDidExplicitDoltCommit is set when a command already created a Dolt commit
	// explicitly (e.g., bd sync in dolt-native mode, hook flows, bd vc commit).
	// This prevents a redundant auto-commit attempt in PersistentPostRun.
	commandDidExplicitDoltCommit bool

	// commandDidWriteTipMetadata is set when a command records a tip as "shown" by writing
	// metadata (tip_*_last_shown). This will be used to create a separate Dolt commit for
	// tip writes, even when the main command is read-only.
	commandDidWriteTipMetadata bool

	// commandTipIDsShown tracks which tip IDs were shown in this command (deduped).
	// This is used for tip-commit message formatting.
	commandTipIDsShown map[string]struct{}
)

// readOnlyCommands lists commands that only read from the database.
// These commands open SQLite in read-only mode to avoid modifying the
// database file (which breaks file watchers). See GH#804.
var readOnlyCommands = map[string]bool{
	"list":       true,
	"ready":      true,
	"show":       true,
	"stats":      true,
	"blocked":    true,
	"count":      true,
	"search":     true,
	"graph":      true,
	"duplicates": true,
	"comments":   true, // list comments (not add)
	"current":    true, // bd sync mode current
	// NOTE: "export" is NOT read-only - it writes to clear dirty issues and update jsonl_file_hash
}

// isReadOnlyCommand returns true if the command only reads from the database.
// This is used to open SQLite in read-only mode, preventing file modifications
// that would trigger file watchers. See GH#804.
func isReadOnlyCommand(cmdName string) bool {
	return readOnlyCommands[cmdName]
}

// getActorWithGit returns the actor for audit trails with git config fallback.
// Priority: --actor flag > BD_ACTOR env > BEADS_ACTOR env > git config user.name > $USER > "unknown"
// This provides a sensible default for developers: their git identity is used unless
// explicitly overridden
func getActorWithGit() string {
	// If actor is already set (from --actor flag), use it
	if actor != "" {
		return actor
	}

	// Check BD_ACTOR env var (primary env override)
	if bdActor := os.Getenv("BD_ACTOR"); bdActor != "" {
		return bdActor
	}

	// Check BEADS_ACTOR env var (alias for MCP/integration compatibility)
	if beadsActor := os.Getenv("BEADS_ACTOR"); beadsActor != "" {
		return beadsActor
	}

	// Try git config user.name - the natural default for a git-native tool
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if gitUser := strings.TrimSpace(string(out)); gitUser != "" {
			return gitUser
		}
	}

	// Fall back to system username
	if user := os.Getenv("USER"); user != "" {
		return user
	}

	return "unknown"
}

// getOwner returns the human owner for CV attribution.
// Priority: GIT_AUTHOR_EMAIL env > git config user.email > "" (empty)
// This is the foundation for HOP CV (curriculum vitae) chains per Decision 008.
// Unlike actor (which tracks who executed), owner tracks the human responsible.
func getOwner() string {
	// Check GIT_AUTHOR_EMAIL first - this is set during git commit operations
	if authorEmail := os.Getenv("GIT_AUTHOR_EMAIL"); authorEmail != "" {
		return authorEmail
	}

	// Fall back to git config user.email - the natural default
	if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
		if gitEmail := strings.TrimSpace(string(out)); gitEmail != "" {
			return gitEmail
		}
	}

	// Return empty if no email found (owner is optional)
	return ""
}

func init() {
	// Initialize viper configuration
	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
	}

	// Add command groups for organized help output
	rootCmd.AddGroup(
		&cobra.Group{ID: GroupMaintenance, Title: "Maintenance:"},
		&cobra.Group{ID: GroupIntegrations, Title: "Integrations & Advanced:"},
	)

	// Register persistent flags
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "Database path (default: auto-discover .beads/*.db)")
	rootCmd.PersistentFlags().StringVar(&actor, "actor", "", "Actor name for audit trail (default: $BD_ACTOR, git user.name, $USER)")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVar(&noDaemon, "no-daemon", false, "Force direct storage mode, bypass daemon if running")
	rootCmd.PersistentFlags().BoolVar(&noAutoFlush, "no-auto-flush", false, "Disable automatic JSONL sync after CRUD operations")
	rootCmd.PersistentFlags().BoolVar(&noAutoImport, "no-auto-import", false, "Disable automatic JSONL import when newer than DB")
	rootCmd.PersistentFlags().BoolVar(&sandboxMode, "sandbox", false, "Sandbox mode: disables daemon and auto-sync")
	rootCmd.PersistentFlags().BoolVar(&allowStale, "allow-stale", false, "Allow operations on potentially stale data (skip staleness check)")
	rootCmd.PersistentFlags().BoolVar(&noDb, "no-db", false, "Use no-db mode: load from JSONL, no SQLite")
	rootCmd.PersistentFlags().BoolVar(&readonlyMode, "readonly", false, "Read-only mode: block write operations (for worker sandboxes)")
	rootCmd.PersistentFlags().StringVar(&doltAutoCommit, "dolt-auto-commit", "", "Dolt backend: auto-commit after write commands (off|on). Default from config key dolt.auto-commit")
	rootCmd.PersistentFlags().DurationVar(&lockTimeout, "lock-timeout", 30*time.Second, "SQLite busy timeout (0 = fail immediately if locked)")
	rootCmd.PersistentFlags().BoolVar(&profileEnabled, "profile", false, "Generate CPU profile for performance analysis")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Enable verbose/debug output")
	rootCmd.PersistentFlags().BoolVarP(&quietFlag, "quiet", "q", false, "Suppress non-essential output (errors only)")

	// Add --version flag to root command (same behavior as version subcommand)
	rootCmd.Flags().BoolP("version", "V", false, "Print version information")

	// Command groups for organized help output (Tufte-inspired)
	rootCmd.AddGroup(&cobra.Group{ID: "issues", Title: "Working With Issues:"})
	rootCmd.AddGroup(&cobra.Group{ID: "views", Title: "Views & Reports:"})
	rootCmd.AddGroup(&cobra.Group{ID: "deps", Title: "Dependencies & Structure:"})
	rootCmd.AddGroup(&cobra.Group{ID: "sync", Title: "Sync & Data:"})
	rootCmd.AddGroup(&cobra.Group{ID: "setup", Title: "Setup & Configuration:"})
	// NOTE: Many maintenance commands (clean, cleanup, compact, validate, repair-deps)
	// should eventually be consolidated into 'bd doctor' and 'bd doctor --fix' to simplify
	// the user experience. The doctor command can detect issues and offer fixes interactively.
	rootCmd.AddGroup(&cobra.Group{ID: "maint", Title: "Maintenance:"})
	rootCmd.AddGroup(&cobra.Group{ID: "advanced", Title: "Integrations & Advanced:"})

	// Custom help function with semantic coloring (Tufte-inspired)
	// Note: Usage output (shown on errors) is not styled to avoid recursion issues
	rootCmd.SetHelpFunc(colorizedHelpFunc)
}

var rootCmd = &cobra.Command{
	Use:   "bd",
	Short: "bd - Dependency-aware issue tracker",
	Long:  `Issues chained together like beads. A lightweight issue tracker with first-class dependency support.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Handle --version flag on root command
		if v, _ := cmd.Flags().GetBool("version"); v {
			fmt.Printf("bd version %s (%s)\n", Version, Build)
			return
		}
		// No subcommand - show help
		_ = cmd.Help()
	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Initialize CommandContext to hold runtime state (replaces scattered globals)
		initCommandContext()

		// Reset per-command write tracking (used by Dolt auto-commit).
		commandDidWrite.Store(false)
		commandDidExplicitDoltCommit = false
		commandDidWriteTipMetadata = false
		commandTipIDsShown = make(map[string]struct{})

		// Set up signal-aware context for graceful cancellation
		rootCtx, rootCancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

		// Apply verbosity flags early (before any output)
		debug.SetVerbose(verboseFlag)
		debug.SetQuiet(quietFlag)

		// Block dangerous env var overrides that could cause data fragmentation (bd-hevyw).
		if err := checkBlockedEnvVars(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Apply viper configuration if flags weren't explicitly set
		// Priority: flags > viper (config file + env vars) > defaults
		// Do this BEFORE early-return so init/version/help respect config

		// Track flag overrides for notification (only in verbose mode)
		flagOverrides := make(map[string]struct {
			Value  interface{}
			WasSet bool
		})

		// If flag wasn't explicitly set, use viper value
		if !cmd.Flags().Changed("json") {
			jsonOutput = config.GetBool("json")
		} else {
			flagOverrides["json"] = struct {
				Value  interface{}
				WasSet bool
			}{jsonOutput, true}
		}
		if !cmd.Flags().Changed("no-daemon") {
			noDaemon = config.GetBool("no-daemon")
		} else {
			flagOverrides["no-daemon"] = struct {
				Value  interface{}
				WasSet bool
			}{noDaemon, true}
		}
		if !cmd.Flags().Changed("no-auto-flush") {
			noAutoFlush = config.GetBool("no-auto-flush")
		} else {
			flagOverrides["no-auto-flush"] = struct {
				Value  interface{}
				WasSet bool
			}{noAutoFlush, true}
		}
		if !cmd.Flags().Changed("no-auto-import") {
			noAutoImport = config.GetBool("no-auto-import")
		} else {
			flagOverrides["no-auto-import"] = struct {
				Value  interface{}
				WasSet bool
			}{noAutoImport, true}
		}
		if !cmd.Flags().Changed("no-db") {
			noDb = config.GetBool("no-db")
		} else {
			flagOverrides["no-db"] = struct {
				Value  interface{}
				WasSet bool
			}{noDb, true}
		}
		if !cmd.Flags().Changed("readonly") {
			readonlyMode = config.GetBool("readonly")
		} else {
			flagOverrides["readonly"] = struct {
				Value  interface{}
				WasSet bool
			}{readonlyMode, true}
		}
		if !cmd.Flags().Changed("lock-timeout") {
			lockTimeout = config.GetDuration("lock-timeout")
		} else {
			flagOverrides["lock-timeout"] = struct {
				Value  interface{}
				WasSet bool
			}{lockTimeout, true}
		}
		if !cmd.Flags().Changed("db") && dbPath == "" {
			dbPath = config.GetString("db")
		} else if cmd.Flags().Changed("db") {
			flagOverrides["db"] = struct {
				Value  interface{}
				WasSet bool
			}{dbPath, true}
		}
		if !cmd.Flags().Changed("actor") && actor == "" {
			actor = config.GetString("actor")
		} else if cmd.Flags().Changed("actor") {
			flagOverrides["actor"] = struct {
				Value  interface{}
				WasSet bool
			}{actor, true}
		}
		if !cmd.Flags().Changed("dolt-auto-commit") && strings.TrimSpace(doltAutoCommit) == "" {
			doltAutoCommit = config.GetString("dolt.auto-commit")
		} else if cmd.Flags().Changed("dolt-auto-commit") {
			flagOverrides["dolt-auto-commit"] = struct {
				Value  interface{}
				WasSet bool
			}{doltAutoCommit, true}
		}

		// Check for and log configuration overrides (only in verbose mode)
		if verboseFlag {
			overrides := config.CheckOverrides(flagOverrides)
			for _, override := range overrides {
				config.LogOverride(override)
			}
		}

		// Validate Dolt auto-commit mode early so all commands fail fast on invalid config.
		if _, err := getDoltAutoCommitMode(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// GH#1093: Check noDbCommands BEFORE expensive operations (ensureForkProtection,
		// signalOrchestratorActivity) to avoid spawning git subprocesses for simple commands
		// like "bd version" that don't need database access.
		noDbCommands := []string{
			cmdDaemon,
			"__complete",       // Cobra's internal completion command (shell completions work without db)
			"__completeNoDesc", // Cobra's completion without descriptions (used by fish)
			"bash",
			"completion",
			"doctor",
			"fish",
			"help",
			"hooks",
			"human",
			"init",
			"merge",
			"onboard",
			"powershell",
			"prime",
			"quickstart",
			"repair",
			"resolve-conflicts",
			"setup",
			"version",
			"zsh",
		}
		// Check both the command name and parent command name for subcommands
		cmdName := cmd.Name()
		if cmd.Parent() != nil {
			parentName := cmd.Parent().Name()
			if slices.Contains(noDbCommands, parentName) {
				return
			}
		}
		if slices.Contains(noDbCommands, cmdName) {
			return
		}

		// Skip for root command with no subcommand (just shows help)
		if cmd.Parent() == nil && cmdName == cmd.Use {
			return
		}

		// Also skip for --version flag on root command (cmdName would be "bd")
		if v, _ := cmd.Flags().GetBool("version"); v {
			return
		}

		// Signal orchestrator daemon about bd activity (best-effort, for exponential backoff)
		// GH#1093: Moved after noDbCommands check to avoid git subprocesses for simple commands
		defer signalOrchestratorActivity()

		// Protect forks from accidentally committing upstream issue database
		ensureForkProtection()

		// Show migration hint if SQLite + prefer-dolt configured (rate-limited, non-blocking)
		if hintDir := beads.FindBeadsDir(); hintDir != "" {
			maybeShowMigrationHint(hintDir)
		}

		// Performance profiling setup
		// When --profile is enabled, force direct mode to capture actual database operations
		// rather than just RPC serialization/network overhead. This gives accurate profiles
		// of the storage layer, query performance, and business logic.
		if profileEnabled {
			noDaemon = true
			timestamp := time.Now().Format("20060102-150405")
			if f, _ := os.Create(fmt.Sprintf("bd-profile-%s-%s.prof", cmd.Name(), timestamp)); f != nil {
				profileFile = f
				_ = pprof.StartCPUProfile(f)
			}
			if f, _ := os.Create(fmt.Sprintf("bd-trace-%s-%s.out", cmd.Name(), timestamp)); f != nil {
				traceFile = f
				_ = trace.Start(f)
			}
		}

		// Auto-detect sandboxed environment (Phase 2 for GH #353)
		// Only auto-enable if user hasn't explicitly set --sandbox or --no-daemon
		if !cmd.Flags().Changed("sandbox") && !cmd.Flags().Changed("no-daemon") {
			if isSandboxed() {
				sandboxMode = true
				fmt.Fprintf(os.Stderr, "ℹ️  Sandbox detected, using direct mode\n")
			}
		}

		// If sandbox mode is set, enable all sandbox flags
		if sandboxMode {
			noDaemon = true
			noAutoFlush = true
			noAutoImport = true
			// Use shorter lock timeout in sandbox mode unless explicitly set
			if !cmd.Flags().Changed("lock-timeout") {
				lockTimeout = 100 * time.Millisecond
			}
		}

		// Force direct mode for human-only interactive commands
		// edit: can take minutes in $EDITOR, daemon connection times out (GH #227)
		if cmd.Name() == "edit" {
			noDaemon = true
		}

		// Set auto-flush based on flag (invert no-auto-flush)
		autoFlushEnabled = !noAutoFlush

		// Set auto-import based on flag (invert no-auto-import)
		autoImportEnabled = !noAutoImport

		// Handle --no-db mode: load from JSONL, use in-memory storage
		if noDb {
			if err := initializeNoDbMode(); err != nil {
				fmt.Fprintf(os.Stderr, "Error initializing --no-db mode: %v\n", err)
				os.Exit(1)
			}

			// Set actor for audit trail
			actor = getActorWithGit()

			// Skip daemon and SQLite initialization - we're in memory mode
			return
		}

		// Initialize database path
		if dbPath == "" {
			// Use public API to find database (same logic as extensions)
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				dbPath = foundDB
			} else {
				// No database found - check if this is JSONL-only mode
				beadsDir := beads.FindBeadsDir()
				if beadsDir != "" {
					jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

					// Check if JSONL exists and config.yaml has no-db: true
					jsonlExists := false
					if _, err := os.Stat(jsonlPath); err == nil {
						jsonlExists = true
					}

					// Use proper YAML parsing to detect no-db mode
					isNoDbMode := isNoDbModeConfigured(beadsDir)

					// If JSONL-only mode is configured, auto-enable it
					if jsonlExists && isNoDbMode {
						noDb = true
						if err := initializeNoDbMode(); err != nil {
							fmt.Fprintf(os.Stderr, "Error initializing JSONL-only mode: %v\n", err)
							os.Exit(1)
						}
						// Set actor for audit trail
						actor = getActorWithGit()
						return
					}
				}

				// Allow some commands to run without a database
				// - import: auto-initializes database if missing
				// - setup: creates editor integration files (no DB needed)
				// - config set/get for yaml-only keys: writes to config.yaml, not SQLite (GH#536)
				isYamlOnlyConfigOp := false
				if (cmd.Name() == "set" || cmd.Name() == "get") && cmd.Parent() != nil && cmd.Parent().Name() == "config" {
					if len(args) > 0 && config.IsYamlOnlyKey(args[0]) {
						isYamlOnlyConfigOp = true
					}
				}

				// Allow read-only commands to auto-bootstrap from JSONL (GH#b09)
				// This enables `bd --no-daemon show` after cold-start when DB is missing
				canAutoBootstrap := false
				if isReadOnlyCommand(cmd.Name()) && beadsDir != "" {
					jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
					if _, err := os.Stat(jsonlPath); err == nil {
						canAutoBootstrap = true
						debug.Logf("cold-start bootstrap: JSONL exists, allowing auto-create for %s", cmd.Name())
					}
				}

				if cmd.Name() != "import" && cmd.Name() != "setup" && !isYamlOnlyConfigOp && !canAutoBootstrap {
					// No database found - provide context-aware error message
					fmt.Fprintf(os.Stderr, "Error: no beads database found\n")

					// Check if JSONL exists without no-db mode configured
					if beadsDir != "" {
						jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
						if _, err := os.Stat(jsonlPath); err == nil {
							// JSONL exists but no-db mode not configured
							fmt.Fprintf(os.Stderr, "\nFound JSONL file: %s\n", jsonlPath)
							fmt.Fprintf(os.Stderr, "This looks like a fresh clone or JSONL-only project.\n\n")
							fmt.Fprintf(os.Stderr, "Options:\n")
							fmt.Fprintf(os.Stderr, "  • Run 'bd init' to create database and import issues\n")
							fmt.Fprintf(os.Stderr, "  • Use 'bd --no-db %s' for JSONL-only mode\n", cmd.Name())
							fmt.Fprintf(os.Stderr, "  • Add 'no-db: true' to .beads/config.yaml for permanent JSONL-only mode\n")
							os.Exit(1)
						}
					}

					// Generic error - no beads directory or JSONL found
					fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to create a database in the current directory\n")
					fmt.Fprintf(os.Stderr, "      or use 'bd --no-db' to work with JSONL only (no SQLite)\n")
					fmt.Fprintf(os.Stderr, "      or set BEADS_DIR to point to your .beads directory\n")
					os.Exit(1)
				}
				// For import/setup commands, set default database path
				// Invariant: dbPath must always be absolute for filepath.Rel() compatibility
				// in daemon sync-branch code path. Use CanonicalizePath for OS-agnostic
				// handling (symlinks, case normalization on macOS).
				//
				// IMPORTANT: Use FindBeadsDir() to get the correct .beads directory,
				// which follows redirect files. Without this, a redirected .beads
				// would create a local database instead of using the redirect target.
				// (GH#bd-0qel)
				targetBeadsDir := beads.FindBeadsDir()
				if targetBeadsDir == "" {
					targetBeadsDir = ".beads"
				}
				dbPath = utils.CanonicalizePath(filepath.Join(targetBeadsDir, beads.CanonicalDatabaseName))
			}
		}

		// Set actor for audit trail
		actor = getActorWithGit()

		// Track bd version changes
		// Best-effort tracking - failures are silent
		trackBdVersion()

		// Initialize daemon status
		socketPath := getSocketPath()
		daemonStatus = DaemonStatus{
			Mode:             "direct",
			Connected:        false,
			Degraded:         true,
			SocketPath:       socketPath,
			AutoStartEnabled: shouldAutoStartDaemon(),
			FallbackReason:   FallbackNone,
		}

		// Doctor should always run in direct mode. It's specifically used to diagnose and
		// repair daemon/DB issues, so attempting to connect to (or auto-start) a daemon
		// can add noise and timeouts.
		if cmd.Name() == "doctor" {
			noDaemon = true
		}

		// Restore should always run in direct mode. It performs git checkouts to read
		// historical issue data, which could conflict with daemon operations.
		if cmd.Name() == "restore" {
			noDaemon = true
		}

		// Wisp operations auto-bypass daemon
		// Wisps are ephemeral (Ephemeral=true) and never exported to JSONL,
		// so daemon can't help anyway. This reduces friction in wisp workflows.
		if isWispOperation(cmd, args) {
			noDaemon = true
			daemonStatus.FallbackReason = FallbackWispOperation
			debug.Logf("wisp operation detected, using direct mode")
		}

		// Embedded Dolt is single-process-only; never use daemon/RPC.
		// (Dolt server mode supports multi-process and won't trigger this.)
		// This must be checked after dbPath is resolved.
		if !noDaemon && singleProcessOnlyBackend() {
			noDaemon = true
			daemonStatus.AutoStartEnabled = false
			daemonStatus.FallbackReason = FallbackSingleProcessOnly
			daemonStatus.Detail = "backend is single-process-only (embedded dolt): daemon mode disabled; using direct mode"
			debug.Logf("single-process backend detected, using direct mode")
		}

		// Try to connect to daemon first (unless --no-daemon flag is set or worktree safety check fails)
		if noDaemon {
			// Only set FallbackFlagNoDaemon if not already set by auto-bypass logic
			if daemonStatus.FallbackReason == FallbackNone {
				daemonStatus.FallbackReason = FallbackFlagNoDaemon
				debug.Logf("--no-daemon flag set, using direct mode")
			}
		} else if shouldDisableDaemonForWorktree() {
			// In a git worktree without sync-branch configured - daemon is unsafe
			// because all worktrees share the same .beads directory and the daemon
			// would commit to whatever branch its working directory has checked out.
			daemonStatus.FallbackReason = FallbackWorktreeSafety
			debug.Logf("git worktree detected without sync-branch, using direct mode for safety")
		} else {
			// Attempt daemon connection
			client, err := rpc.TryConnect(socketPath)
			if err == nil && client != nil {
				// Set expected database path for validation
				if dbPath != "" {
					absDBPath, _ := filepath.Abs(dbPath)
					client.SetDatabasePath(absDBPath)
				}

				// Perform health check
				health, healthErr := client.Health()
				if healthErr == nil && health.Status == statusHealthy {
					// Check version compatibility
					if !health.Compatible {
						debug.Logf("daemon version mismatch (daemon: %s, client: %s), restarting daemon",
							health.Version, Version)
						_ = client.Close()

						// Kill old daemon and restart with new version
						if restartDaemonForVersionMismatch() {
							// Retry connection after restart
							client, err = rpc.TryConnect(socketPath)
							if err == nil && client != nil {
								if dbPath != "" {
									absDBPath, _ := filepath.Abs(dbPath)
									client.SetDatabasePath(absDBPath)
								}
								health, healthErr = client.Health()
								if healthErr == nil && health.Status == statusHealthy {
									client.SetActor(actor)
									daemonClient = client
									daemonStatus.Mode = cmdDaemon
									daemonStatus.Connected = true
									daemonStatus.Degraded = false
									daemonStatus.Health = health.Status
									debug.Logf("connected to restarted daemon (version: %s)", health.Version)
									warnWorktreeDaemon(dbPath)
									return
								}
							}
						}
						// If restart failed, fall through to direct mode
						daemonStatus.FallbackReason = FallbackHealthFailed
						daemonStatus.Detail = fmt.Sprintf("version mismatch (daemon: %s, client: %s) and restart failed",
							health.Version, Version)
					} else {
						// Daemon is healthy and compatible - use it
						client.SetActor(actor)
						daemonClient = client
						daemonStatus.Mode = cmdDaemon
						daemonStatus.Connected = true
						daemonStatus.Degraded = false
						daemonStatus.Health = health.Status
						debug.Logf("connected to daemon at %s (health: %s)", socketPath, health.Status)
						// Warn if using daemon with git worktrees
						warnWorktreeDaemon(dbPath)
						return // Skip direct storage initialization
					}
				} else {
					// Health check failed or daemon unhealthy
					_ = client.Close()
					daemonStatus.FallbackReason = FallbackHealthFailed
					if healthErr != nil {
						daemonStatus.Detail = healthErr.Error()
						debug.Logf("daemon health check failed: %v", healthErr)
					} else {
						daemonStatus.Health = health.Status
						daemonStatus.Detail = health.Error
						debug.Logf("daemon unhealthy (status=%s): %s", health.Status, health.Error)
					}
				}
			} else {
				// Connection failed
				daemonStatus.FallbackReason = FallbackConnectFailed
				if err != nil {
					daemonStatus.Detail = err.Error()
					debug.Logf("daemon connect failed at %s: %v", socketPath, err)
				}
			}

			// Daemon not running or unhealthy - try auto-start if enabled
			if daemonStatus.AutoStartEnabled {
				daemonStatus.AutoStartAttempted = true
				debug.Logf("attempting to auto-start daemon")
				startTime := time.Now()
				if tryAutoStartDaemon(socketPath) {
					// Retry connection after auto-start
					client, err := rpc.TryConnect(socketPath)
					if err == nil && client != nil {
						// Set expected database path for validation
						if dbPath != "" {
							absDBPath, _ := filepath.Abs(dbPath)
							client.SetDatabasePath(absDBPath)
						}

						// Check health of auto-started daemon
						health, healthErr := client.Health()
						if healthErr == nil && health.Status == statusHealthy {
							client.SetActor(actor)
							daemonClient = client
							daemonStatus.Mode = cmdDaemon
							daemonStatus.Connected = true
							daemonStatus.Degraded = false
							daemonStatus.AutoStartSucceeded = true
							daemonStatus.Health = health.Status
							daemonStatus.FallbackReason = FallbackNone
							elapsed := time.Since(startTime).Milliseconds()
							debug.Logf("auto-start succeeded; connected at %s in %dms", socketPath, elapsed)
							// Warn if using daemon with git worktrees
							warnWorktreeDaemon(dbPath)
							return // Skip direct storage initialization
						} else {
							// Auto-started daemon is unhealthy
							_ = client.Close()
							daemonStatus.FallbackReason = FallbackHealthFailed
							if healthErr != nil {
								daemonStatus.Detail = healthErr.Error()
							} else {
								daemonStatus.Health = health.Status
								daemonStatus.Detail = health.Error
							}
							debug.Logf("auto-started daemon is unhealthy; falling back to direct mode")
						}
					} else {
						// Auto-start completed but connection still failed
						daemonStatus.FallbackReason = FallbackAutoStartFailed
						if err != nil {
							daemonStatus.Detail = err.Error()
						}
						// Check for daemon-error file to provide better error message
						if beadsDir := filepath.Dir(socketPath); beadsDir != "" {
							errFile := filepath.Join(beadsDir, "daemon-error")
							// nolint:gosec // G304: errFile is derived from secure beads directory
							if errMsg, readErr := os.ReadFile(errFile); readErr == nil && len(errMsg) > 0 {
								fmt.Fprintf(os.Stderr, "\n%s\n", string(errMsg))
								daemonStatus.Detail = string(errMsg)
							}
						}
						debug.Logf("auto-start did not yield a running daemon; falling back to direct mode")
					}
				} else {
					// Auto-start itself failed
					daemonStatus.FallbackReason = FallbackAutoStartFailed
					debug.Logf("auto-start failed; falling back to direct mode")
				}
			} else {
				// Auto-start disabled - preserve the actual failure reason
				// Don't override connect_failed or health_failed with auto_start_disabled
				// This preserves important diagnostic info (daemon crashed vs not running)
				debug.Logf("auto-start disabled by BEADS_AUTO_START_DAEMON")
			}

			// Emit BD_VERBOSE warning if falling back to direct mode
			if os.Getenv("BD_VERBOSE") != "" {
				emitVerboseWarning()
			}

			debug.Logf("using direct mode (reason: %s)", daemonStatus.FallbackReason)
		}

		// Check if this is a read-only command (GH#804)
		// Read-only commands open SQLite in read-only mode to avoid modifying
		// the database file (which breaks file watchers).
		useReadOnly := isReadOnlyCommand(cmd.Name())

		// Auto-migrate database on version bump
		// Skip for read-only commands - they can't write anyway
		// Do this AFTER daemon check but BEFORE opening database for main operation
		// This ensures: 1) no daemon has DB open, 2) we don't open DB twice
		if !useReadOnly {
			autoMigrateOnVersionBump(dbPath)
		}

		// Fall back to direct storage access
		var err error
		var needsBootstrap bool // Track if DB needs initial import (GH#b09)
		beadsDir := filepath.Dir(dbPath)

		// Detect backend from metadata.json
		backend := factory.GetBackendFromConfig(beadsDir)

		// Create storage with appropriate options
		opts := factory.Options{
			ReadOnly:    useReadOnly,
			LockTimeout: lockTimeout,
		}

		if backend == configfile.BackendDolt {
			// Set advisory lock timeout for dolt embedded mode.
			// Reads get a shorter timeout (shared lock, less contention expected).
			// Writes get a longer timeout (exclusive lock, may need to wait for readers).
			if useReadOnly {
				opts.OpenTimeout = 5 * time.Second
			} else {
				opts.OpenTimeout = 15 * time.Second
			}

			// For Dolt, use the dolt subdirectory
			doltPath := filepath.Join(beadsDir, "dolt")

			// Check if server mode is configured in metadata.json
			cfg, cfgErr := configfile.Load(beadsDir)
			if cfgErr == nil && cfg != nil && cfg.IsDoltServerMode() {
				opts.ServerMode = true
				opts.ServerHost = cfg.GetDoltServerHost()
				opts.ServerPort = cfg.GetDoltServerPort()
				if cfg.Database != "" {
					opts.Database = cfg.GetDoltDatabase()
				}
			}

			store, err = factory.NewWithOptions(rootCtx, backend, doltPath, opts)
		} else {
			// SQLite backend
			store, err = factory.NewWithOptions(rootCtx, backend, dbPath, opts)
			if err != nil && useReadOnly {
				// If read-only fails (e.g., DB doesn't exist), fall back to read-write
				// This handles the case where user runs "bd list" before "bd init"
				debug.Logf("read-only open failed, falling back to read-write: %v", err)
				opts.ReadOnly = false
				store, err = factory.NewWithOptions(rootCtx, backend, dbPath, opts)
				needsBootstrap = true // New DB needs auto-import (GH#b09)
			}
		}

		// Track final read-only state for staleness checks (GH#1089)
		// opts.ReadOnly may have changed if read-only open failed and fell back
		storeIsReadOnly = opts.ReadOnly

		if err != nil {
			// Check for fresh clone scenario
			if handleFreshCloneError(err, beadsDir) {
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
			os.Exit(1)
		}

		// Mark store as active for flush goroutine safety
		storeMutex.Lock()
		storeActive = true
		storeMutex.Unlock()

		// Initialize flush manager (fixes race condition in auto-flush)
		// Skip FlushManager creation in sandbox mode - no background goroutines needed
		// (improves Windows exit behavior and container scenarios)
		// Skip for read-only commands - they don't write anything (GH#804)
		// For in-process test scenarios where commands run multiple times,
		// we create a new manager each time. Shutdown() is idempotent so
		// PostRun can safely shutdown whichever manager is active.
		if !sandboxMode && !useReadOnly {
			flushManager = NewFlushManager(autoFlushEnabled, getDebounceDuration())
		}

		// Initialize hook runner
		// dbPath is .beads/something.db, so workspace root is parent of .beads
		if dbPath != "" {
			beadsDir := filepath.Dir(dbPath)
			hookRunner = hooks.NewRunner(filepath.Join(beadsDir, "hooks"))
		}

		// Warn if multiple databases detected in directory hierarchy
		warnMultipleDatabases(dbPath)

		// Auto-import if JSONL is newer than DB (e.g., after git pull)
		// Skip for import command itself to avoid recursion
		// Skip for delete command to prevent resurrection of deleted issues
		// Skip if sync --dry-run to avoid modifying DB in dry-run mode
		// Skip for read-only commands - they can't write anyway (GH#804)
		// Exception: allow auto-import for read-only commands that fell back to
		// read-write mode due to missing DB (needsBootstrap) - fixes GH#b09
		if cmd.Name() != "import" && cmd.Name() != "delete" && autoImportEnabled && (!useReadOnly || needsBootstrap) {
			// Check if this is sync command with --dry-run flag
			if cmd.Name() == "sync" {
				if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
					// Skip auto-import in dry-run mode
					debug.Logf("auto-import skipped for sync --dry-run")
				} else {
					autoImportIfNewer()
				}
			} else {
				autoImportIfNewer()
			}
		}

		// Load molecule templates from hierarchical catalog locations
		// Templates are loaded after auto-import to ensure the database is up-to-date.
		// Skip for import command to avoid conflicts during import operations.
		if cmd.Name() != "import" && store != nil {
			beadsDir := filepath.Dir(dbPath)
			loader := molecules.NewLoader(store)
			if result, err := loader.LoadAll(rootCtx, beadsDir); err != nil {
				debug.Logf("warning: failed to load molecules: %v", err)
			} else if result.Loaded > 0 {
				debug.Logf("loaded %d molecules from %v", result.Loaded, result.Sources)
			}
		}

		// Tips (including sync conflict proactive checks) are shown via maybeShowTip()
		// after successful command execution, not in PreRun

		// Sync all state to CommandContext for unified access
		syncCommandContext()
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Handle --no-db mode: write memory storage back to JSONL
		if noDb {
			if store != nil {
				// Determine beads directory (respect BEADS_DIR)
				var beadsDir string
				if envDir := os.Getenv("BEADS_DIR"); envDir != "" {
					// Canonicalize the path
					beadsDir = utils.CanonicalizePath(envDir)
				} else {
					// Fall back to current directory
					cwd, err := os.Getwd()
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error: failed to get current directory: %v\n", err)
						os.Exit(1)
					}
					beadsDir = filepath.Join(cwd, ".beads")
				}

				if memStore, ok := store.(*memory.MemoryStorage); ok {
					if err := writeIssuesToJSONL(memStore, beadsDir); err != nil {
						fmt.Fprintf(os.Stderr, "Error: failed to write JSONL: %v\n", err)
						os.Exit(1)
					}
				}
			}
			return
		}

		// Close daemon client if we're using it
		if daemonClient != nil {
			_ = daemonClient.Close()
			return
		}

		// Otherwise, handle direct mode cleanup
		// Shutdown flush manager (performs final flush if needed)
		// Skip if sync command already handled export and restore (sync.branch mode)
		if flushManager != nil && !skipFinalFlush {
			if err := flushManager.Shutdown(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: flush manager shutdown error: %v\n", err)
			}
		}

		// Dolt auto-commit: after a successful write command (and after final flush),
		// create a Dolt commit so changes don't remain only in the working set.
		if commandDidWrite.Load() && !commandDidExplicitDoltCommit {
			if err := maybeAutoCommit(rootCtx, doltAutoCommitParams{Command: cmd.Name()}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: dolt auto-commit failed: %v\n", err)
				os.Exit(1)
			}
		}

		// Tip metadata auto-commit: if a tip was shown, create a separate Dolt commit for the
		// tip_*_last_shown metadata updates. This may happen even for otherwise read-only commands.
		if commandDidWriteTipMetadata && len(commandTipIDsShown) > 0 {
			// Only applies when dolt auto-commit is enabled and backend is versioned (Dolt).
			if mode, err := getDoltAutoCommitMode(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: dolt tip auto-commit failed: %v\n", err)
				os.Exit(1)
			} else if mode == doltAutoCommitOn {
				// Apply tip metadata writes now (deferred in recordTipShown for Dolt).
				for tipID := range commandTipIDsShown {
					key := fmt.Sprintf("tip_%s_last_shown", tipID)
					value := time.Now().Format(time.RFC3339)
					if err := store.SetMetadata(rootCtx, key, value); err != nil {
						fmt.Fprintf(os.Stderr, "Error: dolt tip auto-commit failed: %v\n", err)
						os.Exit(1)
					}
				}

				ids := make([]string, 0, len(commandTipIDsShown))
				for tipID := range commandTipIDsShown {
					ids = append(ids, tipID)
				}
				msg := formatDoltAutoCommitMessage("tip", getActor(), ids)
				if err := maybeAutoCommit(rootCtx, doltAutoCommitParams{Command: "tip", MessageOverride: msg}); err != nil {
					fmt.Fprintf(os.Stderr, "Error: dolt tip auto-commit failed: %v\n", err)
					os.Exit(1)
				}
			}
		}

		// Signal that store is closing (prevents background flush from accessing closed store)
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()

		if store != nil {
			_ = store.Close()
		}

		if profileFile != nil {
			pprof.StopCPUProfile()
			_ = profileFile.Close()
		}
		if traceFile != nil {
			trace.Stop()
			_ = traceFile.Close()
		}

		// Cancel the signal context to clean up resources
		if rootCancel != nil {
			rootCancel()
		}
	},
}

// blockedEnvVars lists environment variables that must not be set because they
// could silently override the storage backend via viper's AutomaticEnv, causing
// data fragmentation between sqlite and dolt (bd-hevyw).
var blockedEnvVars = []string{"BD_BACKEND", "BD_DATABASE_BACKEND"}

// checkBlockedEnvVars returns an error if any blocked env vars are set.
func checkBlockedEnvVars() error {
	for _, name := range blockedEnvVars {
		if os.Getenv(name) != "" {
			return fmt.Errorf("%s env var is not supported and has been removed to prevent data fragmentation.\n"+
				"The storage backend is set in .beads/metadata.json. To change it, use: bd migrate dolt (or bd migrate sqlite)", name)
		}
	}
	return nil
}

func main() {
	// BD_NAME overrides the binary name in help text (e.g. BD_NAME=ops makes
	// "ops --help" show "ops" instead of "bd"). Useful for multi-instance
	// setups where wrapper scripts set BEADS_DIR for routing.
	if name := os.Getenv("BD_NAME"); name != "" {
		rootCmd.Use = name
	}

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
