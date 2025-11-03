package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/storage/sqlite"
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

	// Auto-flush state
	autoFlushEnabled  = true  // Can be disabled with --no-auto-flush
	isDirty           = false // Tracks if DB has changes needing export
	needsFullExport   = false // Set to true when IDs change (e.g., rename-prefix)
	flushMutex        sync.Mutex
	flushTimer        *time.Timer
	storeMutex        sync.Mutex // Protects store access from background goroutine
	storeActive       = false    // Tracks if store is available
	flushFailureCount = 0        // Consecutive flush failures
	lastFlushError    error      // Last flush error for debugging

	// Auto-import state
	autoImportEnabled = true // Can be disabled with --no-auto-import
)

var (
	noAutoFlush  bool
	noAutoImport bool
	sandboxMode  bool
	noDb         bool // Use --no-db mode: load from JSONL, write back after each command
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
	rootCmd.PersistentFlags().BoolVar(&noDb, "no-db", false, "Use no-db mode: load from JSONL, no SQLite")
}

var rootCmd = &cobra.Command{
	Use:   "bd",
	Short: "bd - Dependency-aware issue tracker",
	Long:  `Issues chained together like beads. A lightweight issue tracker with first-class dependency support.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
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
		if !cmd.Flags().Changed("db") && dbPath == "" {
			dbPath = config.GetString("db")
		}
		if !cmd.Flags().Changed("actor") && actor == "" {
			actor = config.GetString("actor")
		}

		// Skip database initialization for commands that don't need a database
		noDbCommands := []string{"init", cmdDaemon, "help", "version", "quickstart", "doctor"}
		if slices.Contains(noDbCommands, cmd.Name()) {
			return
		}

		// If sandbox mode is set, enable all sandbox flags
		if sandboxMode {
			noDaemon = true
			noAutoFlush = true
			noAutoImport = true
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
				// No database found - error out instead of falling back to ~/.beads
				fmt.Fprintf(os.Stderr, "Error: no beads database found\n")
				fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to create a database in the current directory\n")
				fmt.Fprintf(os.Stderr, "      or set BEADS_DIR to point to your .beads directory\n")
				fmt.Fprintf(os.Stderr, "      or set BEADS_DB to point to your database file (deprecated)\n")
				os.Exit(1)
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

		// Try to connect to daemon first (unless --no-daemon flag is set)
		if noDaemon {
			daemonStatus.FallbackReason = FallbackFlagNoDaemon
			if os.Getenv("BD_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "Debug: --no-daemon flag set, using direct mode\n")
			}
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
						if os.Getenv("BD_DEBUG") != "" {
							fmt.Fprintf(os.Stderr, "Debug: daemon version mismatch (daemon: %s, client: %s), restarting daemon\n",
								health.Version, Version)
						}
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
									if os.Getenv("BD_DEBUG") != "" {
										fmt.Fprintf(os.Stderr, "Debug: connected to restarted daemon (version: %s)\n", health.Version)
									}
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
						if os.Getenv("BD_DEBUG") != "" {
							fmt.Fprintf(os.Stderr, "Debug: connected to daemon at %s (health: %s)\n", socketPath, health.Status)
						}
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
						if os.Getenv("BD_DEBUG") != "" {
							fmt.Fprintf(os.Stderr, "Debug: daemon health check failed: %v\n", healthErr)
						}
					} else {
						daemonStatus.Health = health.Status
						daemonStatus.Detail = health.Error
						if os.Getenv("BD_DEBUG") != "" {
							fmt.Fprintf(os.Stderr, "Debug: daemon unhealthy (status=%s): %s\n", health.Status, health.Error)
						}
					}
				}
			} else {
				// Connection failed
				daemonStatus.FallbackReason = FallbackConnectFailed
				if err != nil {
					daemonStatus.Detail = err.Error()
					if os.Getenv("BD_DEBUG") != "" {
						fmt.Fprintf(os.Stderr, "Debug: daemon connect failed at %s: %v\n", socketPath, err)
					}
				}
			}

			// Daemon not running or unhealthy - try auto-start if enabled
			if daemonStatus.AutoStartEnabled {
				daemonStatus.AutoStartAttempted = true
				if os.Getenv("BD_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "Debug: attempting to auto-start daemon\n")
				}
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
							if os.Getenv("BD_DEBUG") != "" {
								elapsed := time.Since(startTime).Milliseconds()
								fmt.Fprintf(os.Stderr, "Debug: auto-start succeeded; connected at %s in %dms\n", socketPath, elapsed)
							}
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
							if os.Getenv("BD_DEBUG") != "" {
								fmt.Fprintf(os.Stderr, "Debug: auto-started daemon is unhealthy; falling back to direct mode\n")
							}
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
						if os.Getenv("BD_DEBUG") != "" {
							fmt.Fprintf(os.Stderr, "Debug: auto-start did not yield a running daemon; falling back to direct mode\n")
						}
					}
				} else {
					// Auto-start itself failed
					daemonStatus.FallbackReason = FallbackAutoStartFailed
					if os.Getenv("BD_DEBUG") != "" {
						fmt.Fprintf(os.Stderr, "Debug: auto-start failed; falling back to direct mode\n")
					}
				}
			} else {
				// Auto-start disabled - preserve the actual failure reason
				// Don't override connect_failed or health_failed with auto_start_disabled
				// This preserves important diagnostic info (daemon crashed vs not running)
				if os.Getenv("BD_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "Debug: auto-start disabled by BEADS_AUTO_START_DAEMON\n")
				}
			}

			// Emit BD_VERBOSE warning if falling back to direct mode
			if os.Getenv("BD_VERBOSE") != "" {
				emitVerboseWarning()
			}

			if os.Getenv("BD_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "Debug: using direct mode (reason: %s)\n", daemonStatus.FallbackReason)
			}
		}

		// Fall back to direct storage access
		var err error
		store, err = sqlite.New(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
			os.Exit(1)
		}

		// Mark store as active for flush goroutine safety
		storeMutex.Lock()
		storeActive = true
		storeMutex.Unlock()

		// Warn if multiple databases detected in directory hierarchy
		warnMultipleDatabases(dbPath)

		// Check for version mismatch (warn if binary is older than DB)
		checkVersionMismatch()

		// Auto-import if JSONL is newer than DB (e.g., after git pull)
		// Skip for import command itself to avoid recursion
		// Skip if sync --dry-run to avoid modifying DB in dry-run mode (bd-191)
		if cmd.Name() != "import" && autoImportEnabled {
			// Check if this is sync command with --dry-run flag
			if cmd.Name() == "sync" {
				if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
					// Skip auto-import in dry-run mode
					if os.Getenv("BD_DEBUG") != "" {
						fmt.Fprintf(os.Stderr, "Debug: auto-import skipped for sync --dry-run\n")
					}
				} else {
					autoImportIfNewer()
				}
			} else {
				autoImportIfNewer()
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
		// Flush any pending changes before closing
		flushMutex.Lock()
		needsFlush := isDirty && autoFlushEnabled
		if needsFlush {
			// Cancel timer and flush immediately
			if flushTimer != nil {
				flushTimer.Stop()
				flushTimer = nil
			}
			// Don't clear isDirty or needsFullExport here - let flushToJSONL do it
		}
		flushMutex.Unlock()

		if needsFlush {
			// Call the shared flush function (handles both incremental and full export)
			flushToJSONL()
		}

		// Signal that store is closing (prevents background flush from accessing closed store)
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()

		if store != nil {
			_ = store.Close()
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
