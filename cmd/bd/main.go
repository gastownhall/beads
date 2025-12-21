package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/molecules"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// DaemonStatus captures daemon connection state for the current command
type DaemonStatus struct {
	Mode               string `json:"mode"` // "daemon" or "direct"
	Connected          bool   `json:"connected"`
	Degraded           bool   `json:"degraded"`
	SocketPath         string `json:"socket_path,omitempty"`
	AutoStartEnabled   bool   `json:"auto_start_enabled"`
	AutoStartAttempted bool   `json:"auto_start_attempted"`
	AutoStartSucceeded bool   `json:"auto_start_succeeded"`
	FallbackReason     string `json:"fallback_reason,omitempty"` // "none","flag_no_daemon","connect_failed","health_failed","auto_start_disabled","auto_start_failed"
	Detail             string `json:"detail,omitempty"`          // short diagnostic
	Health             string `json:"health,omitempty"`          // "healthy","degraded","unhealthy"
}

// Fallback reason constants
const (
	FallbackNone              = "none"
	FallbackFlagNoDaemon      = "flag_no_daemon"
	FallbackConnectFailed     = "connect_failed"
	FallbackHealthFailed      = "health_failed"
	FallbackWorktreeSafety    = "worktree_safety"
	cmdDaemon                 = "daemon"
	cmdImport                 = "import"
	statusHealthy             = "healthy"
	FallbackAutoStartDisabled = "auto_start_disabled"
	FallbackAutoStartFailed   = "auto_start_failed"
	FallbackDaemonUnsupported = "daemon_unsupported"
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
	autoFlushEnabled  = true  // Can be disabled with --no-auto-flush
	isDirty           = false // Tracks if DB has changes needing export (used by legacy code)
	needsFullExport   = false // Set to true when IDs change (used by legacy code)
	flushMutex        sync.Mutex
	flushTimer        *time.Timer // DEPRECATED: Use flushManager instead
	storeMutex        sync.Mutex  // Protects store access from background goroutine
	storeActive       = false     // Tracks if store is available
	flushFailureCount = 0         // Consecutive flush failures
	lastFlushError    error       // Last flush error for debugging

	// Auto-flush manager (replaces timer-based approach to fix bd-52)
	flushManager *FlushManager

	// Hook runner for extensibility (bd-kwro.8)
	hookRunner *hooks.Runner

	// skipFinalFlush is set by sync command when sync.branch mode completes successfully.
	// This prevents PersistentPostRun from re-exporting and dirtying the working directory.
	skipFinalFlush = false

	// Auto-import state
	autoImportEnabled = true // Can be disabled with --no-auto-import

	// Version upgrade tracking (bd-loka)
	versionUpgradeDetected = false // Set to true if bd version changed since last run
	previousVersion        = ""    // The last bd version user had (empty = first run or unknown)
	upgradeAcknowledged    = false // Set to true after showing upgrade notification once per session
)

var (
	noAutoFlush    bool
	noAutoImport   bool
	sandboxMode    bool
	allowStale     bool          // Use --allow-stale: skip staleness check (emergency escape hatch)
	noDb           bool          // Use --no-db mode: load from JSONL, write back after each command
	readonlyMode   bool          // Read-only mode: block write operations (for worker sandboxes)
	lockTimeout    time.Duration // SQLite busy_timeout (default 30s, 0 = fail immediately)
	profileEnabled bool
	profileFile    *os.File
	traceFile      *os.File
	verboseFlag    bool // Enable verbose/debug output
	quietFlag      bool // Suppress non-essential output
)

func init() {
	// Initialize viper configuration
	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
	}

	// Register persistent flags
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "Database path (default: auto-discover .beads/*.db)")
	rootCmd.PersistentFlags().StringVar(&actor, "actor", "", "Actor name for audit trail (default: $BD_ACTOR or $USER)")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVar(&noDaemon, "no-daemon", false, "Force direct storage mode, bypass daemon if running")
	rootCmd.PersistentFlags().BoolVar(&noAutoFlush, "no-auto-flush", false, "Disable automatic JSONL sync after CRUD operations")
	rootCmd.PersistentFlags().BoolVar(&noAutoImport, "no-auto-import", false, "Disable automatic JSONL import when newer than DB")
	rootCmd.PersistentFlags().BoolVar(&sandboxMode, "sandbox", false, "Sandbox mode: disables daemon and auto-sync")
	rootCmd.PersistentFlags().BoolVar(&allowStale, "allow-stale", false, "Allow operations on potentially stale data (skip staleness check)")
	rootCmd.PersistentFlags().BoolVar(&noDb, "no-db", false, "Use no-db mode: load from JSONL, no SQLite")
	rootCmd.PersistentFlags().BoolVar(&readonlyMode, "readonly", false, "Read-only mode: block write operations (for worker sandboxes)")
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

// colorizedHelpFunc wraps Cobra's default help with semantic coloring
// Applies subtle accent color to group headers for visual hierarchy
func colorizedHelpFunc(cmd *cobra.Command, args []string) {
	// Build full help output: Long description + Usage
	var output strings.Builder

	// Include Long description first (like Cobra's default help)
	if cmd.Long != "" {
		output.WriteString(cmd.Long)
		output.WriteString("\n\n")
	} else if cmd.Short != "" {
		output.WriteString(cmd.Short)
		output.WriteString("\n\n")
	}

	// Add the usage string which contains commands, flags, etc.
	output.WriteString(cmd.UsageString())

	// Apply semantic coloring
	result := colorizeHelpOutput(output.String())
	fmt.Print(result)
}

// colorizeHelpOutput applies semantic colors to help text
// - Group headers get accent color for visual hierarchy
// - Section headers (Examples:, Flags:) get accent color
// - Command names get subtle styling for scanability
// - Flag names get bold styling, types get muted
// - Default values get muted styling
func colorizeHelpOutput(help string) string {
	// Match group header lines (e.g., "Working With Issues:")
	// These are standalone lines ending with ":" and followed by commands
	groupHeaderRE := regexp.MustCompile(`(?m)^([A-Z][A-Za-z &]+:)\s*$`)

	result := groupHeaderRE.ReplaceAllStringFunc(help, func(match string) string {
		// Trim whitespace, colorize, then restore
		trimmed := strings.TrimSpace(match)
		return ui.RenderAccent(trimmed)
	})

	// Match section headers in subcommand help (Examples:, Flags:, etc.)
	sectionHeaderRE := regexp.MustCompile(`(?m)^(Examples|Flags|Usage|Global Flags|Aliases|Available Commands):`)
	result = sectionHeaderRE.ReplaceAllStringFunc(result, func(match string) string {
		return ui.RenderAccent(match)
	})

	// Match command lines: "  command   Description text"
	// Commands are indented with 2 spaces, followed by spaces, then description
	// Pattern matches: indent + command-name (with hyphens) + spacing + description
	cmdLineRE := regexp.MustCompile(`(?m)^(  )([a-z][a-z0-9]*(?:-[a-z0-9]+)*)(\s{2,})(.*)$`)

	result = cmdLineRE.ReplaceAllStringFunc(result, func(match string) string {
		parts := cmdLineRE.FindStringSubmatch(match)
		if len(parts) != 5 {
			return match
		}
		indent := parts[1]
		cmdName := parts[2]
		spacing := parts[3]
		description := parts[4]

		// Colorize command references in description (e.g., 'comments add')
		description = colorizeCommandRefs(description)

		// Highlight entry point hints (e.g., "(start here)")
		description = highlightEntryPoints(description)

		// Subtle styling on command name for scanability
		return indent + ui.RenderCommand(cmdName) + spacing + description
	})

	// Match flag lines: "  -f, --file string   Description"
	// Pattern: indent + flags + spacing + optional type + description
	flagLineRE := regexp.MustCompile(`(?m)^(\s+)(-\w,\s+--[\w-]+|--[\w-]+)(\s+)(string|int|duration|bool)?(\s*.*)$`)
	result = flagLineRE.ReplaceAllStringFunc(result, func(match string) string {
		parts := flagLineRE.FindStringSubmatch(match)
		if len(parts) < 6 {
			return match
		}
		indent := parts[1]
		flags := parts[2]
		spacing := parts[3]
		typeStr := parts[4]
		desc := parts[5]

		// Mute default values in description
		desc = muteDefaults(desc)

		if typeStr != "" {
			return indent + ui.RenderCommand(flags) + spacing + ui.RenderMuted(typeStr) + desc
		}
		return indent + ui.RenderCommand(flags) + spacing + desc
	})

	return result
}

// muteDefaults applies muted styling to default value annotations
func muteDefaults(text string) string {
	defaultRE := regexp.MustCompile(`(\(default[^)]*\))`)
	return defaultRE.ReplaceAllStringFunc(text, func(match string) string {
		return ui.RenderMuted(match)
	})
}

// highlightEntryPoints applies accent styling to entry point hints like "(start here)"
func highlightEntryPoints(text string) string {
	entryRE := regexp.MustCompile(`(\(start here\))`)
	return entryRE.ReplaceAllStringFunc(text, func(match string) string {
		return ui.RenderAccent(match)
	})
}

// colorizeCommandRefs applies command styling to references in text
// Matches patterns like 'command name' or 'bd command'
func colorizeCommandRefs(text string) string {
	// Match 'command words' in single quotes (e.g., 'comments add')
	cmdRefRE := regexp.MustCompile(`'([a-z][a-z0-9 -]+)'`)

	return cmdRefRE.ReplaceAllStringFunc(text, func(match string) string {
		// Extract the command name without quotes
		inner := match[1 : len(match)-1]
		return "'" + ui.RenderCommand(inner) + "'"
	})
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
		// Set up signal-aware context for graceful cancellation
		rootCtx, rootCancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

		// Apply verbosity flags early (before any output)
		debug.SetVerbose(verboseFlag)
		debug.SetQuiet(quietFlag)

		// Apply viper configuration if flags weren't explicitly set
		// Priority: flags > viper (config file + env vars) > defaults
		// Do this BEFORE early-return so init/version/help respect config

		// If flag wasn't explicitly set, use viper value
		if !cmd.Flags().Changed("json") {
			jsonOutput = config.GetBool("json")
		}
		if !cmd.Flags().Changed("no-daemon") {
			noDaemon = config.GetBool("no-daemon")
		}
		if !cmd.Flags().Changed("no-auto-flush") {
			noAutoFlush = config.GetBool("no-auto-flush")
		}
		if !cmd.Flags().Changed("no-auto-import") {
			noAutoImport = config.GetBool("no-auto-import")
		}
		if !cmd.Flags().Changed("no-db") {
			noDb = config.GetBool("no-db")
		}
		if !cmd.Flags().Changed("readonly") {
			readonlyMode = config.GetBool("readonly")
		}
		if !cmd.Flags().Changed("lock-timeout") {
			lockTimeout = config.GetDuration("lock-timeout")
		}
		if !cmd.Flags().Changed("db") && dbPath == "" {
			dbPath = config.GetString("db")
		}
		if !cmd.Flags().Changed("actor") && actor == "" {
			actor = config.GetString("actor")
		}

		// Protect forks from accidentally committing upstream issue database
		ensureForkProtection()

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

		// Skip database initialization for commands that don't need a database
		noDbCommands := []string{
			cmdDaemon,
			"bash",
			"completion",
			"doctor",
			"fish",
			"help",
			"hooks",
			"init",
			"merge",
			"onboard",
			"powershell",
			"prime",
			"quickstart",
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

		// Also skip for --version flag on root command (cmdName would be "bd")
		if v, _ := cmd.Flags().GetBool("version"); v {
			return
		}

		// Auto-detect sandboxed environment (bd-u3t: Phase 2 for GH #353)
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
			if actor == "" {
				if bdActor := os.Getenv("BD_ACTOR"); bdActor != "" {
					actor = bdActor
				} else if user := os.Getenv("USER"); user != "" {
					actor = user
				} else {
					actor = "unknown"
				}
			}

			// Skip daemon and SQLite initialization - we're in memory mode
			return
		}

		// Initialize database path
		if dbPath == "" {
			// Use public API to find database (same logic as extensions)
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				dbPath = foundDB
			} else {
				// No database found - check if this is JSONL-only mode (bd-5kj)
				beadsDir := beads.FindBeadsDir()
				if beadsDir != "" {
					jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

					// Check if JSONL exists and config.yaml has no-db: true
					jsonlExists := false
					if _, err := os.Stat(jsonlPath); err == nil {
						jsonlExists = true
					}

					// Use proper YAML parsing to detect no-db mode (bd-r6k2)
					isNoDbMode := isNoDbModeConfigured(beadsDir)

					// If JSONL-only mode is configured, auto-enable it
					if jsonlExists && isNoDbMode {
						noDb = true
						if err := initializeNoDbMode(); err != nil {
							fmt.Fprintf(os.Stderr, "Error initializing JSONL-only mode: %v\n", err)
							os.Exit(1)
						}
						// Set actor from flag, viper, or env
						if actor == "" {
							if user := os.Getenv("USER"); user != "" {
								actor = user
							} else {
								actor = "unknown"
							}
						}
						return
					}
				}

				// Allow some commands to run without a database
				// - import: auto-initializes database if missing
				// - setup: creates editor integration files (no DB needed)
				if cmd.Name() != "import" && cmd.Name() != "setup" {
					// No database found - provide context-aware error message (bd-534)
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
				dbPath = filepath.Join(".beads", beads.CanonicalDatabaseName)
			}
		}

		// Set actor from flag, viper (env), or default
		// Priority: --actor flag > viper (config + BD_ACTOR env) > USER env > "unknown"
		// Note: Viper handles BD_ACTOR automatically via AutomaticEnv()
		if actor == "" {
			// Viper already populated from config file or BD_ACTOR env
			// Fall back to USER env if still empty
			if user := os.Getenv("USER"); user != "" {
				actor = user
			} else {
				actor = "unknown"
			}
		}

		// Track bd version changes (bd-loka)
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

		// Try to connect to daemon first (unless --no-daemon flag is set or worktree safety check fails)
		if noDaemon {
			daemonStatus.FallbackReason = FallbackFlagNoDaemon
			debug.Logf("--no-daemon flag set, using direct mode")
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

		// Auto-migrate database on version bump (bd-jgxi)
		// Do this AFTER daemon check but BEFORE opening database for main operation
		// This ensures: 1) no daemon has DB open, 2) we don't open DB twice
		autoMigrateOnVersionBump(dbPath)

		// Fall back to direct storage access
		var err error
		store, err = sqlite.NewWithTimeout(rootCtx, dbPath, lockTimeout)
		if err != nil {
			// Check for fresh clone scenario (bd-dmb)
			beadsDir := filepath.Dir(dbPath)
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

		// Initialize flush manager (fixes bd-52: race condition in auto-flush)
		// Skip FlushManager creation in sandbox mode - no background goroutines needed
		// (bd-dh8a: improves Windows exit behavior and container scenarios)
		// For in-process test scenarios where commands run multiple times,
		// we create a new manager each time. Shutdown() is idempotent so
		// PostRun can safely shutdown whichever manager is active.
		if !sandboxMode {
			flushManager = NewFlushManager(autoFlushEnabled, getDebounceDuration())
		}

		// Initialize hook runner (bd-kwro.8)
		// dbPath is .beads/something.db, so workspace root is parent of .beads
		if dbPath != "" {
			beadsDir := filepath.Dir(dbPath)
			hookRunner = hooks.NewRunner(filepath.Join(beadsDir, "hooks"))
		}

		// Warn if multiple databases detected in directory hierarchy
		warnMultipleDatabases(dbPath)

		// Auto-import if JSONL is newer than DB (e.g., after git pull)
		// Skip for import command itself to avoid recursion
		// Skip for delete command to prevent resurrection of deleted issues (bd-8kde)
		// Skip if sync --dry-run to avoid modifying DB in dry-run mode (bd-191)
		if cmd.Name() != "import" && cmd.Name() != "delete" && autoImportEnabled {
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

		// Load molecule templates from hierarchical catalog locations (gt-0ei3)
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

		// Signal that store is closing (prevents background flush from accessing closed store)
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()

		if store != nil {
			_ = store.Close()
		}
		if profileFile != nil { pprof.StopCPUProfile(); _ = profileFile.Close() }
		if traceFile != nil { trace.Stop(); _ = traceFile.Close() }

		// Cancel the signal context to clean up resources
		if rootCancel != nil {
			rootCancel()
		}
	},
}

// getDebounceDuration returns the auto-flush debounce duration
// Configurable via config file or BEADS_FLUSH_DEBOUNCE env var (e.g., "500ms", "10s")
// Defaults to 5 seconds if not set or invalid

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// isFreshCloneError checks if the error is due to a fresh clone scenario
// where the database exists but is missing required config (like issue_prefix).
// This happens when someone clones a repo with beads but needs to initialize.
func isFreshCloneError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for the specific migration invariant error pattern
	return strings.Contains(errStr, "post-migration validation failed") &&
		strings.Contains(errStr, "required config key missing: issue_prefix")
}

// handleFreshCloneError displays a helpful message when a fresh clone is detected
// and returns true if the error was handled (so caller should exit).
// If not a fresh clone error, returns false and does nothing.
func handleFreshCloneError(err error, beadsDir string) bool {
	if !isFreshCloneError(err) {
		return false
	}

	// Look for JSONL file in the .beads directory
	jsonlPath := ""
	issueCount := 0

	if beadsDir != "" {
		// Check for issues.jsonl (canonical) first, then beads.jsonl (legacy)
		for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
			candidate := filepath.Join(beadsDir, name)
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				jsonlPath = candidate
				// Count lines (approximately = issue count)
				// #nosec G304 -- candidate is constructed from beadsDir which is .beads/
				if data, readErr := os.ReadFile(candidate); readErr == nil {
					for _, line := range strings.Split(string(data), "\n") {
						if strings.TrimSpace(line) != "" {
							issueCount++
						}
					}
				}
				break
			}
		}
	}

	fmt.Fprintf(os.Stderr, "Error: Database not initialized\n\n")
	fmt.Fprintf(os.Stderr, "This appears to be a fresh clone or the database needs initialization.\n")

	if jsonlPath != "" && issueCount > 0 {
		fmt.Fprintf(os.Stderr, "Found: %s (%d issues)\n\n", jsonlPath, issueCount)
		fmt.Fprintf(os.Stderr, "To initialize from the JSONL file, run:\n")
		fmt.Fprintf(os.Stderr, "  bd import -i %s\n\n", jsonlPath)
	} else {
		fmt.Fprintf(os.Stderr, "\nTo initialize a new database, run:\n")
		fmt.Fprintf(os.Stderr, "  bd init --prefix <your-prefix>\n\n")
	}

	fmt.Fprintf(os.Stderr, "For more information: bd init --help\n")
	return true
}
