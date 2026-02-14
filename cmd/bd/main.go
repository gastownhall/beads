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
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/utils"
)

var (
	dbPath     string
	actor      string
	store      storage.Storage
	jsonOutput bool

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
	rootCmd.PersistentFlags().BoolVar(&noAutoFlush, "no-auto-flush", false, "Disable automatic JSONL sync after CRUD operations")
	rootCmd.PersistentFlags().BoolVar(&noAutoImport, "no-auto-import", false, "Disable automatic JSONL import when newer than DB")
	rootCmd.PersistentFlags().BoolVar(&sandboxMode, "sandbox", false, "Sandbox mode: disables auto-sync")
	rootCmd.PersistentFlags().BoolVar(&allowStale, "allow-stale", false, "Allow operations on potentially stale data (skip staleness check)")
	rootCmd.PersistentFlags().BoolVar(&noDb, "no-db", false, "Use no-db mode: load from JSONL, no SQLite")
	rootCmd.PersistentFlags().BoolVar(&readonlyMode, "readonly", false, "Read-only mode: block write operations (for worker sandboxes)")
	rootCmd.PersistentFlags().StringVar(&doltAutoCommit, "dolt-auto-commit", "", "Dolt backend: auto-commit after write commands (off|on). Default: on for embedded, off for server mode. Override via config key dolt.auto-commit")
	rootCmd.PersistentFlags().DurationVar(&lockTimeout, "lock-timeout", 30*time.Second, "SQLite busy timeout (0 = fail immediately if locked)")
	rootCmd.PersistentFlags().BoolVar(&profileEnabled, "profile", false, "Generate CPU profile for performance analysis")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Enable verbose/debug output")
	rootCmd.PersistentFlags().BoolVarP(&quietFlag, "quiet", "q", false, "Suppress non-essential output (errors only)")

	// Note: --no-daemon is registered in daemon_compat.go as a deprecated no-op flag.

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
			"__complete",       // Cobra's internal completion command (shell completions work without db)
			"__completeNoDesc", // Cobra's completion without descriptions (used by fish)
			"bash",
			"completion",
			"doctor",
			"fish",
			"help",
			"hook",  // manages its own store lifecycle; double-open deadlocks embedded Dolt (#1719)
			"hooks",
			"human",
			"init",
			"merge",
			"migrate", // manages its own store lifecycle; double-open deadlocks embedded Dolt (#1668)
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
		if profileEnabled {
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
		if !cmd.Flags().Changed("sandbox") {
			if isSandboxed() {
				sandboxMode = true
				fmt.Fprintf(os.Stderr, "ℹ️  Sandbox detected, using direct mode\n")
			}
		}

		// If sandbox mode is set, enable all sandbox flags
		if sandboxMode {
			noAutoFlush = true
			noAutoImport = true
			// Use shorter lock timeout in sandbox mode unless explicitly set
			if !cmd.Flags().Changed("lock-timeout") {
				lockTimeout = 100 * time.Millisecond
			}
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

			// Skip SQLite initialization - we're in memory mode
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
				// This enables `bd show` after cold-start when DB is missing.
				// IMPORTANT: Only auto-bootstrap for SQLite backend. If metadata.json says
				// the backend is Dolt, we must NOT silently create a SQLite database —
				// that causes Classic contamination. Error out instead so the user can
				// fix the Dolt connection. (gt-r1nex)
				canAutoBootstrap := false
				if isReadOnlyCommand(cmd.Name()) && beadsDir != "" {
					jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
					if _, err := os.Stat(jsonlPath); err == nil {
						configuredBackend := factory.GetBackendFromConfig(beadsDir)
						if configuredBackend == configfile.BackendDolt {
							// Dolt backend configured but database not found — don't create SQLite
							fmt.Fprintf(os.Stderr, "Error: Dolt backend configured but database not found\n")
							fmt.Fprintf(os.Stderr, "The .beads/metadata.json specifies backend: dolt\n")
							fmt.Fprintf(os.Stderr, "but no Dolt database was found. Check that the Dolt server is running.\n")
							fmt.Fprintf(os.Stderr, "\nHint: run 'bd doctor --fix' to diagnose and repair\n")
							os.Exit(1)
						}
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
				// Invariant: dbPath must always be absolute. Use CanonicalizePath for OS-agnostic
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

		// Check if this is a read-only command (GH#804)
		// Read-only commands open SQLite in read-only mode to avoid modifying
		// the database file (which breaks file watchers).
		useReadOnly := isReadOnlyCommand(cmd.Name())

		// Auto-migrate database on version bump
		// Skip for read-only commands - they can't write anyway
		if !useReadOnly {
			autoMigrateOnVersionBump(filepath.Dir(dbPath))
		}

		// Initialize direct storage access
		var err error
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

			// Load config to get database name and server mode settings
			cfg, cfgErr := configfile.Load(beadsDir)
			if cfgErr == nil && cfg != nil {
				// Always set database name (needed for bootstrap to find
				// prefix-based databases like "beads_hq"; see #1669)
				opts.Database = cfg.GetDoltDatabase()

				if cfg.IsDoltServerMode() {
					opts.ServerMode = true
					opts.ServerHost = cfg.GetDoltServerHost()
					opts.ServerPort = cfg.GetDoltServerPort()
				}
			}

			// Apply mode-aware default for dolt-auto-commit if neither flag nor
			// config explicitly set it. Server mode defaults to OFF because the
			// server handles commits via its own transaction lifecycle; firing
			// DOLT_COMMIT after every write under concurrent load causes
			// 'database is read only' errors. Embedded mode defaults to ON so
			// each write is durably committed.
			if strings.TrimSpace(doltAutoCommit) == "" {
				if opts.ServerMode {
					doltAutoCommit = string(doltAutoCommitOff)
				} else {
					doltAutoCommit = string(doltAutoCommitOn)
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

		// Initialize hook runner
		// dbPath is .beads/something.db, so workspace root is parent of .beads
		if dbPath != "" {
			beadsDir := filepath.Dir(dbPath)
			hookRunner = hooks.NewRunner(filepath.Join(beadsDir, "hooks"))
		}

		// Warn if multiple databases detected in directory hierarchy
		warnMultipleDatabases(dbPath)

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

	// Register --all flag on Cobra's auto-generated help command.
	// Must be called after init() so all subcommands are registered and
	// Cobra has created its default help command.
	rootCmd.InitDefaultHelpCmd()
	registerHelpAllFlag()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
