package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/autoimport"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"golang.org/x/mod/semver"
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
	needsFullExport   = false // Set to true when IDs change (renumber, rename-prefix)
	flushMutex        sync.Mutex
	flushTimer        *time.Timer
	storeMutex        sync.Mutex // Protects store access from background goroutine
	storeActive       = false    // Tracks if store is available
	flushFailureCount = 0        // Consecutive flush failures
	lastFlushError    error      // Last flush error for debugging

	// Auto-import state
	autoImportEnabled = true // Can be disabled with --no-auto-import
)

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
		if !cmd.Flags().Changed("db") && dbPath == "" {
			dbPath = config.GetString("db")
		}
		if !cmd.Flags().Changed("actor") && actor == "" {
			actor = config.GetString("actor")
		}

		// Skip database initialization for commands that don't need a database
		if cmd.Name() == "init" || cmd.Name() == cmdDaemon || cmd.Name() == "help" || cmd.Name() == "version" || cmd.Name() == "quickstart" {
			return
		}

		// If sandbox mode is set, enable all sandbox flags
		if sandboxMode {
			noDaemon = true
			noAutoFlush = true
			noAutoImport = true
		}

		// Sync RPC client version with CLI version
		rpc.ClientVersion = Version

		// Set auto-flush based on flag (invert no-auto-flush)
		autoFlushEnabled = !noAutoFlush

		// Set auto-import based on flag (invert no-auto-import)
		autoImportEnabled = !noAutoImport

		// Initialize database path
		if dbPath == "" {
			cwd, err := os.Getwd()
			localBeadsDir := ""
			if err == nil {
				localBeadsDir = filepath.Join(cwd, ".beads")
			}

			// Use public API to find database (same logic as extensions)
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				dbPath = foundDB

				// Special case for import: if we found a database but there's a local .beads/
				// directory without a database, prefer creating a local database
				if cmd.Name() == cmdImport && localBeadsDir != "" {
				if _, err := os.Stat(localBeadsDir); err == nil {
				// Check if found database is NOT in the local .beads/ directory
				if !strings.HasPrefix(dbPath, localBeadsDir+string(filepath.Separator)) {
				// Look for existing .db file in local .beads/ directory
				matches, _ := filepath.Glob(filepath.Join(localBeadsDir, "*.db"))
				 if len(matches) > 0 {
				   dbPath = matches[0]
				   } else {
							// No database exists yet - will be created by import
							// Use generic name that will be renamed after prefix detection
							dbPath = filepath.Join(localBeadsDir, "bd.db")
						}
					}
				}
			}
			} else {
			// For import command, allow creating database if .beads/ directory exists
			if cmd.Name() == cmdImport && localBeadsDir != "" {
			if _, err := os.Stat(localBeadsDir); err == nil {
			// Look for existing .db file in local .beads/ directory
			matches, _ := filepath.Glob(filepath.Join(localBeadsDir, "*.db"))
			 if len(matches) > 0 {
			   dbPath = matches[0]
					} else {
						// .beads/ directory exists - set dbPath for import to create
						// Use generic name that will be renamed after prefix detection
						dbPath = filepath.Join(localBeadsDir, "bd.db")
					}
				}
			}

				// If dbPath still not set, error out
				if dbPath == "" {
					// No database found - error out instead of falling back to ~/.beads
					fmt.Fprintf(os.Stderr, "Error: no beads database found\n")
					fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to create a database in the current directory\n")
					fmt.Fprintf(os.Stderr, "      or set BEADS_DB environment variable to specify a database\n")
					os.Exit(1)
				}
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
						// Daemon is healthy and compatible - validate database path
						beadsDir := filepath.Dir(dbPath)
						if err := validateDaemonLock(beadsDir, dbPath); err != nil {
							_ = client.Close()
							daemonStatus.FallbackReason = FallbackHealthFailed
							daemonStatus.Detail = fmt.Sprintf("daemon lock validation failed: %v", err)
							if os.Getenv("BD_DEBUG") != "" {
								fmt.Fprintf(os.Stderr, "Debug: daemon lock validation failed: %v\n", err)
							}
							// Fall through to direct mode
						} else {
							// Daemon is healthy, compatible, and validated - use it
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
				// Auto-start disabled - only override if we don't already have a health failure
				if daemonStatus.FallbackReason != FallbackHealthFailed {
					// For connect failures, mention that auto-start was disabled
					if daemonStatus.FallbackReason == FallbackConnectFailed {
						daemonStatus.FallbackReason = FallbackAutoStartDisabled
					}
				}
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
		if cmd.Name() != "import" && autoImportEnabled {
			autoImportIfNewer()
		}
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
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
// Defaults to 30 seconds if not set or invalid (provides batching window)
func getDebounceDuration() time.Duration {
	duration := config.GetDuration("flush-debounce")
	if duration == 0 {
		// If parsing failed, use default
		return 30 * time.Second
	}
	return duration
}

// shouldAutoStartDaemon checks if daemon auto-start is enabled
// emitVerboseWarning prints a one-line warning when falling back to direct mode
func emitVerboseWarning() {
	switch daemonStatus.FallbackReason {
	case FallbackConnectFailed:
		fmt.Fprintf(os.Stderr, "Warning: Daemon unreachable at %s. Running in direct mode. Hint: bd daemon --status\n", daemonStatus.SocketPath)
	case FallbackHealthFailed:
		fmt.Fprintf(os.Stderr, "Warning: Daemon unhealthy. Falling back to direct mode. Hint: bd daemon --health\n")
	case FallbackAutoStartDisabled:
		fmt.Fprintf(os.Stderr, "Warning: Auto-start disabled (BEADS_AUTO_START_DAEMON=false). Running in direct mode. Hint: bd daemon\n")
	case FallbackAutoStartFailed:
		fmt.Fprintf(os.Stderr, "Warning: Failed to auto-start daemon. Running in direct mode. Hint: bd daemon --status\n")
	case FallbackDaemonUnsupported:
		fmt.Fprintf(os.Stderr, "Warning: Daemon does not support this command yet. Running in direct mode. Hint: update daemon or use local mode.\n")
	case FallbackFlagNoDaemon:
		// Don't warn when user explicitly requested --no-daemon
		return
	}
}

func shouldAutoStartDaemon() bool {
	// Check BEADS_NO_DAEMON first (escape hatch for single-user workflows)
	noDaemon := strings.ToLower(strings.TrimSpace(os.Getenv("BEADS_NO_DAEMON")))
	if noDaemon == "1" || noDaemon == "true" || noDaemon == "yes" || noDaemon == "on" {
		return false // Explicit opt-out
	}

	// Use viper to read from config file or BEADS_AUTO_START_DAEMON env var
	// Viper handles BEADS_AUTO_START_DAEMON automatically via BindEnv
	return config.GetBool("auto-start-daemon") // Defaults to true
}

// shouldUseGlobalDaemon determines if global daemon should be preferred
// based on heuristics (multi-repo detection)
// Note: Global daemon is deprecated; this always returns false for now
func shouldUseGlobalDaemon() bool {
	// Global daemon support is deprecated
	// Always use local daemon (per-project .beads/ socket)
	// Previously supported BEADS_PREFER_GLOBAL_DAEMON env var, but global
	// daemon has issues with multi-workspace git workflows
	return false
}

// restartDaemonForVersionMismatch stops the old daemon and starts a new one
// Returns true if restart was successful
func restartDaemonForVersionMismatch() bool {
	// Use local daemon (global is deprecated)
	pidFile, err := getPIDFilePath(false)
	if err != nil {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: failed to get PID file path: %v\n", err)
		}
		return false
	}

	socketPath := getSocketPath()

	// Check if daemon is running and stop it
	forcedKill := false
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: stopping old daemon (PID %d)\n", pid)
		}

		process, err := os.FindProcess(pid)
		if err != nil {
			if os.Getenv("BD_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "Debug: failed to find process: %v\n", err)
			}
			return false
		}

		// Send stop signal
		if err := sendStopSignal(process); err != nil {
			if os.Getenv("BD_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "Debug: failed to signal daemon: %v\n", err)
			}
			return false
		}

		// Wait for daemon to stop (up to 5 seconds)
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if isRunning, _ := isDaemonRunning(pidFile); !isRunning {
				if os.Getenv("BD_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "Debug: old daemon stopped successfully\n")
				}
				break
			}
		}

		// Force kill if still running
		if isRunning, _ := isDaemonRunning(pidFile); isRunning {
			if os.Getenv("BD_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "Debug: force killing old daemon\n")
			}
			_ = process.Kill()
			forcedKill = true
		}
	}

	// Clean up stale socket and PID file after force kill or if not running
	if forcedKill || !isDaemonRunningQuiet(pidFile) {
		_ = os.Remove(socketPath)
		_ = os.Remove(pidFile)
	}

	// Start new daemon with current binary version
	exe, err := os.Executable()
	if err != nil {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: failed to get executable path: %v\n", err)
		}
		return false
	}

	args := []string{"daemon"}
	cmd := exec.Command(exe, args...) // #nosec G204 - bd daemon command from trusted binary
	cmd.Env = append(os.Environ(), "BD_DAEMON_FOREGROUND=1")

	// Set working directory to database directory so daemon finds correct DB
	if dbPath != "" {
		cmd.Dir = filepath.Dir(dbPath)
	}

	configureDaemonProcess(cmd)

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		defer func() { _ = devNull.Close() }()
	}

	if err := cmd.Start(); err != nil {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: failed to start new daemon: %v\n", err)
		}
		return false
	}

	// Reap the process to avoid zombies
	go func() { _ = cmd.Wait() }()

	// Wait for daemon to be ready using shared helper
	if waitForSocketReadiness(socketPath, 5*time.Second) {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: new daemon started successfully\n")
		}
		return true
	}

	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: new daemon failed to become ready\n")
	}
	return false
}

// isDaemonRunningQuiet checks if daemon is running without output
func isDaemonRunningQuiet(pidFile string) bool {
	isRunning, _ := isDaemonRunning(pidFile)
	return isRunning
}

// tryAutoStartDaemon attempts to start the daemon in the background
// Returns true if daemon was started successfully and socket is ready
func tryAutoStartDaemon(socketPath string) bool {
	if !canRetryDaemonStart() {
		debugLog("skipping auto-start due to recent failures")
		return false
	}

	if isDaemonHealthy(socketPath) {
		debugLog("daemon already running and healthy")
		return true
	}

	lockPath := socketPath + ".startlock"
	if !acquireStartLock(lockPath, socketPath) {
		return false
	}
	defer func() {
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			debugLog("failed to remove lock file: %v", err)
		}
	}()

	if handleExistingSocket(socketPath) {
		return true
	}

	socketPath, isGlobal := determineSocketMode(socketPath)
	return startDaemonProcess(socketPath, isGlobal)
}

func debugLog(msg string, args ...interface{}) {
	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: "+msg+"\n", args...)
	}
}

func isDaemonHealthy(socketPath string) bool {
	client, err := rpc.TryConnect(socketPath)
	if err == nil && client != nil {
		_ = client.Close()
		return true
	}
	return false
}

func acquireStartLock(lockPath, socketPath string) bool {
	// #nosec G304 - controlled path from config
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		debugLog("another process is starting daemon, waiting for readiness")
		if waitForSocketReadiness(socketPath, 5*time.Second) {
			return true
		}
		return handleStaleLock(lockPath, socketPath)
	}

	_, _ = fmt.Fprintf(lockFile, "%d\n", os.Getpid())
	_ = lockFile.Close()
	return true
}

func handleStaleLock(lockPath, socketPath string) bool {
	lockPID, err := readPIDFromFile(lockPath)
	if err == nil && !isPIDAlive(lockPID) {
		debugLog("lock is stale (PID %d dead), removing and retrying", lockPID)
		_ = os.Remove(lockPath)
		return tryAutoStartDaemon(socketPath)
	}
	return false
}

func handleExistingSocket(socketPath string) bool {
	if _, err := os.Stat(socketPath); err != nil {
		return false
	}

	if canDialSocket(socketPath, 200*time.Millisecond) {
		debugLog("daemon started by another process")
		return true
	}

	pidFile := getPIDFileForSocket(socketPath)
	if pidFile != "" {
		if pid, err := readPIDFromFile(pidFile); err == nil && isPIDAlive(pid) {
			debugLog("daemon PID %d alive, waiting for socket", pid)
			return waitForSocketReadiness(socketPath, 5*time.Second)
		}
	}

	debugLog("socket is stale, cleaning up")
	_ = os.Remove(socketPath)
	if pidFile != "" {
		_ = os.Remove(pidFile)
	}
	return false
}

func determineSocketMode(socketPath string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return socketPath, false
	}

	globalSocket := filepath.Join(home, ".beads", "bd.sock")
	if socketPath == globalSocket {
		return socketPath, true
	}

	if shouldUseGlobalDaemon() {
		debugLog("detected multiple repos, auto-starting global daemon")
		return globalSocket, true
	}

	return socketPath, false
}

func startDaemonProcess(socketPath string, isGlobal bool) bool {
	binPath, err := os.Executable()
	if err != nil {
		binPath = os.Args[0]
	}

	args := []string{"daemon"}
	if isGlobal {
		args = append(args, "--global")
	}

	cmd := exec.Command(binPath, args...) // #nosec G204 - bd daemon command from trusted binary
	setupDaemonIO(cmd)

	if !isGlobal && dbPath != "" {
		cmd.Dir = filepath.Dir(dbPath)
	}

	configureDaemonProcess(cmd)
	if err := cmd.Start(); err != nil {
		recordDaemonStartFailure()
		debugLog("failed to start daemon: %v", err)
		return false
	}

	go func() { _ = cmd.Wait() }()

	if waitForSocketReadiness(socketPath, 5*time.Second) {
		recordDaemonStartSuccess()
		return true
	}

	recordDaemonStartFailure()
	debugLog("daemon socket not ready after 5 seconds")
	return false
}

func setupDaemonIO(cmd *exec.Cmd) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.Stdin = devNull
		go func() {
			time.Sleep(1 * time.Second)
			_ = devNull.Close()
		}()
	}
}

// getPIDFileForSocket returns the PID file path for a given socket path
func getPIDFileForSocket(socketPath string) string {
	// PID file is in same directory as socket, named daemon.pid
	dir := filepath.Dir(socketPath)
	return filepath.Join(dir, "daemon.pid")
}

// readPIDFromFile reads a PID from a file
func readPIDFromFile(path string) (int, error) {
	// #nosec G304 - controlled path from config
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// isPIDAlive checks if a process with the given PID is running
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return isProcessRunning(pid)
}

// canDialSocket attempts a quick dial to the socket with a timeout
func canDialSocket(socketPath string, timeout time.Duration) bool {
	client, err := rpc.TryConnectWithTimeout(socketPath, timeout)
	if err != nil || client == nil {
		return false
	}
	_ = client.Close()
	return true
}

// waitForSocketReadiness waits for daemon socket to be ready by testing actual connections
//
//nolint:unparam // timeout is configurable even though current callers use 5s
func waitForSocketReadiness(socketPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if canDialSocket(socketPath, 200*time.Millisecond) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// Daemon start failure tracking for exponential backoff
var (
	lastDaemonStartAttempt time.Time
	daemonStartFailures    int
)

func canRetryDaemonStart() bool {
	if daemonStartFailures == 0 {
		return true
	}

	// Exponential backoff: 5s, 10s, 20s, 40s, 80s, 120s (capped at 120s)
	backoff := time.Duration(5*(1<<uint(daemonStartFailures-1))) * time.Second // #nosec G115 - controlled value, no overflow risk
	if backoff > 120*time.Second {
		backoff = 120 * time.Second
	}

	return time.Since(lastDaemonStartAttempt) > backoff
}

func recordDaemonStartSuccess() {
	daemonStartFailures = 0
}

func recordDaemonStartFailure() {
	lastDaemonStartAttempt = time.Now()
	daemonStartFailures++
	// No cap needed - backoff is capped at 120s in canRetryDaemonStart
}

// getSocketPath returns the daemon socket path based on the database location
// Always returns local socket path (.beads/bd.sock relative to database)
func getSocketPath() string {
	// Always use local socket (same directory as database: .beads/bd.sock)
	localSocket := filepath.Join(filepath.Dir(dbPath), "bd.sock")

	// Warn if old global socket exists
	if home, err := os.UserHomeDir(); err == nil {
		globalSocket := filepath.Join(home, ".beads", "bd.sock")
		if _, err := os.Stat(globalSocket); err == nil {
			fmt.Fprintf(os.Stderr, "Warning: Found old global daemon socket at %s\n", globalSocket)
			fmt.Fprintf(os.Stderr, "Global sockets are deprecated. Each project now uses its own local daemon.\n")
			fmt.Fprintf(os.Stderr, "To migrate: Stop the global daemon and restart with 'bd daemon' in each project.\n")
		}
	}

	return localSocket
}

// outputJSON outputs data as pretty-printed JSON
func outputJSON(v interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// findJSONLPath finds the JSONL file path for the current database
// findJSONLPath discovers the JSONL file path for the current database and ensures
// the parent directory exists. Uses beads.FindJSONLPath() for discovery (checking
// BEADS_JSONL env var first, then using .beads/issues.jsonl next to the database).
//
// Creates the .beads directory if it doesn't exist (important for new databases).
// If directory creation fails, returns the path anyway - the subsequent write will
// fail with a clearer error message.
//
// Thread-safe: No shared state access.
func findJSONLPath() string {
	// Use public API for path discovery
	jsonlPath := beads.FindJSONLPath(dbPath)

	// Ensure the directory exists (important for new databases)
	// This is the only difference from the public API - we create the directory
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		// If we can't create the directory, return discovered path anyway
		// (the subsequent write will fail with a clearer error)
		return jsonlPath
	}

	return jsonlPath
}

// autoImportIfNewer checks if JSONL content changed (via hash) and imports if so
// Fixes bd-84: Hash-based comparison is git-proof (mtime comparison fails after git pull)
// Fixes bd-228: Now uses collision detection to prevent silently overwriting local changes
func autoImportIfNewer() {
	ctx := context.Background()
	
	notify := autoimport.NewStderrNotifier(os.Getenv("BD_DEBUG") != "")
	
	importFunc := func(ctx context.Context, issues []*types.Issue) (created, updated int, idMapping map[string]string, err error) {
		opts := ImportOptions{
			ResolveCollisions:    true,
			DryRun:               false,
			SkipUpdate:           false,
			Strict:               false,
			SkipPrefixValidation: true,
		}
		
		result, err := importIssuesCore(ctx, dbPath, store, issues, opts)
		if err != nil {
			return 0, 0, nil, err
		}
		
		return result.Created, result.Updated, result.IDMapping, nil
	}
	
	onChanged := func(needsFullExport bool) {
		if needsFullExport {
			markDirtyAndScheduleFullExport()
		} else {
			markDirtyAndScheduleFlush()
		}
	}
	
	if err := autoimport.AutoImportIfNewer(ctx, store, dbPath, notify, importFunc, onChanged); err != nil {
		// Error already logged by notifier
		return
	}
}

// checkVersionMismatch checks if the binary version matches the database version
// and warns the user if they're running an outdated binary
func checkVersionMismatch() {
	ctx := context.Background()

	// Get the database version (version that last wrote to this DB)
	dbVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		// Metadata error - skip check (shouldn't happen, but be defensive)
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: version check skipped, metadata error: %v\n", err)
		}
		return
	}

	// If no version stored, this is an old database - store current version and continue
	if dbVersion == "" {
		_ = store.SetMetadata(ctx, "bd_version", Version)
		return
	}

	// Compare versions: warn if binary is older than database
	if dbVersion != Version {
		yellow := color.New(color.FgYellow, color.Bold).SprintFunc()
		fmt.Fprintf(os.Stderr, "\n%s\n", yellow("⚠️  WARNING: Version mismatch detected!"))
		fmt.Fprintf(os.Stderr, "%s\n", yellow(fmt.Sprintf("⚠️  Your bd binary (v%s) differs from the database version (v%s)", Version, dbVersion)))

		// Use semantic version comparison (requires v prefix)
		binaryVer := "v" + Version
		dbVer := "v" + dbVersion

		// semver.Compare returns -1 if binaryVer < dbVer, 0 if equal, 1 if binaryVer > dbVer
		cmp := semver.Compare(binaryVer, dbVer)

		if cmp < 0 {
			// Binary is older than database
			fmt.Fprintf(os.Stderr, "%s\n", yellow("⚠️  Your binary appears to be OUTDATED."))
			fmt.Fprintf(os.Stderr, "%s\n\n", yellow("⚠️  Some features may not work correctly. Rebuild: go build -o bd ./cmd/bd"))
		} else if cmp > 0 {
			// Binary is newer than database
			fmt.Fprintf(os.Stderr, "%s\n", yellow("⚠️  Your binary appears NEWER than the database."))
			fmt.Fprintf(os.Stderr, "%s\n", yellow("⚠️  Run 'bd migrate' to check for and migrate old database files."))
			fmt.Fprintf(os.Stderr, "%s\n\n", yellow("⚠️  The current database version will be updated automatically."))
			// Update stored version to current
			_ = store.SetMetadata(ctx, "bd_version", Version)
		}
	}

	// Always update the version metadata to track last-used version
	// This is safe even if versions match (idempotent operation)
	_ = store.SetMetadata(ctx, "bd_version", Version)
}

// markDirtyAndScheduleFlush marks the database as dirty and schedules a flush
// markDirtyAndScheduleFlush marks the database as dirty and schedules a debounced
// export to JSONL. Uses a timer that resets on each call - flush occurs 5 seconds
// after the LAST database modification (not the first).
//
// Debouncing behavior: If multiple operations happen within 5 seconds, the timer
// resets each time, and only one flush occurs after the burst of activity completes.
// This prevents excessive writes during rapid issue creation/updates.
//
// Flush-on-exit guarantee: PersistentPostRun cancels the timer and flushes immediately
// before the command exits, ensuring no data is lost even if the timer hasn't fired.
//
// Thread-safe: Protected by flushMutex. Safe to call from multiple goroutines.
// No-op if auto-flush is disabled via --no-auto-flush flag.
func markDirtyAndScheduleFlush() {
	if !autoFlushEnabled {
		return
	}

	flushMutex.Lock()
	defer flushMutex.Unlock()

	isDirty = true

	// Cancel existing timer if any
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Schedule new flush
	flushTimer = time.AfterFunc(getDebounceDuration(), func() {
		flushToJSONL()
	})
}

// markDirtyAndScheduleFullExport marks DB as needing a full export (for ID-changing operations)
func markDirtyAndScheduleFullExport() {
	if !autoFlushEnabled {
		return
	}

	flushMutex.Lock()
	defer flushMutex.Unlock()

	isDirty = true
	needsFullExport = true // Force full export, not incremental

	// Cancel existing timer if any
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Schedule new flush
	flushTimer = time.AfterFunc(getDebounceDuration(), func() {
		flushToJSONL()
	})
}

// clearAutoFlushState cancels pending flush and marks DB as clean (after manual export)
func clearAutoFlushState() {
	flushMutex.Lock()
	defer flushMutex.Unlock()

	// Cancel pending timer
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Clear dirty flag
	isDirty = false

	// Reset failure counter (manual export succeeded)
	flushFailureCount = 0
	lastFlushError = nil
}

// flushToJSONL exports dirty issues to JSONL using incremental updates
// flushToJSONL exports dirty database changes to the JSONL file. Uses incremental
// export by default (only exports modified issues), or full export for ID-changing
// operations (renumber, resolve-collisions). Invoked by the debounce timer or
// immediately on command exit.
//
// Export modes:
//   - Incremental (default): Exports only GetDirtyIssues(), merges with existing JSONL
//   - Full (after renumber): Exports all issues, rebuilds JSONL from scratch
//
// Error handling: Tracks consecutive failures. After 3+ failures, displays prominent
// warning suggesting manual "bd export" to recover. Failure counter resets on success.
//
// Thread-safety:
//   - Protected by flushMutex for isDirty/needsFullExport access
//   - Checks storeActive flag (via storeMutex) to prevent use-after-close
//   - Safe to call from timer goroutine or main thread
//
// No-op conditions:
//   - Store already closed (storeActive=false)
//   - Database not dirty (isDirty=false)
//   - No dirty issues found (incremental mode only)
func flushToJSONL() {
	// Check if store is still active (not closed)
	storeMutex.Lock()
	if !storeActive {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	flushMutex.Lock()
	if !isDirty {
		flushMutex.Unlock()
		return
	}
	isDirty = false
	fullExport := needsFullExport
	needsFullExport = false // Reset flag
	flushMutex.Unlock()

	jsonlPath := findJSONLPath()

	// Double-check store is still active before accessing
	storeMutex.Lock()
	if !storeActive {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	// Helper to record failure
	recordFailure := func(err error) {
		flushMutex.Lock()
		flushFailureCount++
		lastFlushError = err
		failCount := flushFailureCount
		flushMutex.Unlock()

		// Always show the immediate warning
		fmt.Fprintf(os.Stderr, "Warning: auto-flush failed: %v\n", err)

		// Show prominent warning after 3+ consecutive failures
		if failCount >= 3 {
			red := color.New(color.FgRed, color.Bold).SprintFunc()
			fmt.Fprintf(os.Stderr, "\n%s\n", red("⚠️  CRITICAL: Auto-flush has failed "+fmt.Sprint(failCount)+" times consecutively!"))
			fmt.Fprintf(os.Stderr, "%s\n", red("⚠️  Your JSONL file may be out of sync with the database."))
			fmt.Fprintf(os.Stderr, "%s\n\n", red("⚠️  Run 'bd export -o .beads/issues.jsonl' manually to fix."))
		}
	}

	// Helper to record success
	recordSuccess := func() {
		flushMutex.Lock()
		flushFailureCount = 0
		lastFlushError = nil
		flushMutex.Unlock()
	}

	ctx := context.Background()

	// Determine which issues to export
	var dirtyIDs []string
	var err error

	if fullExport {
		// Full export: get ALL issues (needed after ID-changing operations like renumber)
		allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			recordFailure(fmt.Errorf("failed to get all issues: %w", err))
			return
		}
		dirtyIDs = make([]string, len(allIssues))
		for i, issue := range allIssues {
			dirtyIDs[i] = issue.ID
		}
	} else {
		// Incremental export: get only dirty issue IDs (bd-39 optimization)
		dirtyIDs, err = store.GetDirtyIssues(ctx)
		if err != nil {
			recordFailure(fmt.Errorf("failed to get dirty issues: %w", err))
			return
		}

		// No dirty issues? Nothing to do!
		if len(dirtyIDs) == 0 {
			recordSuccess()
			return
		}
	}

	// Read existing JSONL into a map (skip for full export - we'll rebuild from scratch)
	issueMap := make(map[string]*types.Issue)
	if !fullExport {
		// #nosec G304 - controlled path from config
		if existingFile, err := os.Open(jsonlPath); err == nil {
			scanner := bufio.NewScanner(existingFile)
			lineNum := 0
			for scanner.Scan() {
				lineNum++
				line := scanner.Text()
				if line == "" {
					continue
				}
				var issue types.Issue
				if err := json.Unmarshal([]byte(line), &issue); err == nil {
					issueMap[issue.ID] = &issue
				} else {
					// Warn about malformed JSONL lines
					fmt.Fprintf(os.Stderr, "Warning: skipping malformed JSONL line %d: %v\n", lineNum, err)
				}
			}
			_ = existingFile.Close()
		}
	}

	// Fetch only dirty issues from DB
	for _, issueID := range dirtyIDs {
		issue, err := store.GetIssue(ctx, issueID)
		if err != nil {
			recordFailure(fmt.Errorf("failed to get issue %s: %w", issueID, err))
			return
		}
		if issue == nil {
			// Issue was deleted, remove from map
			delete(issueMap, issueID)
			continue
		}

		// Get dependencies for this issue
		deps, err := store.GetDependencyRecords(ctx, issueID)
		if err != nil {
			recordFailure(fmt.Errorf("failed to get dependencies for %s: %w", issueID, err))
			return
		}
		issue.Dependencies = deps

		// Update map
		issueMap[issueID] = issue
	}

	// Convert map to sorted slice
	issues := make([]*types.Issue, 0, len(issueMap))
	for _, issue := range issueMap {
		issues = append(issues, issue)
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Write to temp file first, then rename (atomic)
	// Use PID in filename to avoid collisions between concurrent bd commands (bd-306)
	tempPath := fmt.Sprintf("%s.tmp.%d", jsonlPath, os.Getpid())
	// #nosec G304 - controlled path from config
	f, err := os.Create(tempPath)
	if err != nil {
		recordFailure(fmt.Errorf("failed to create temp file: %w", err))
		return
	}

	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			_ = f.Close()
			_ = os.Remove(tempPath)
			recordFailure(fmt.Errorf("failed to encode issue %s: %w", issue.ID, err))
			return
		}
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tempPath)
		recordFailure(fmt.Errorf("failed to close temp file: %w", err))
		return
	}

	// Atomic rename
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		_ = os.Remove(tempPath)
		recordFailure(fmt.Errorf("failed to rename file: %w", err))
		return
	}

	// Clear only the dirty issues that were actually exported (fixes bd-52 race condition)
	if err := store.ClearDirtyIssuesByID(ctx, dirtyIDs); err != nil {
		// Don't fail the whole flush for this, but warn
		fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty issues: %v\n", err)
	}

	// Store hash of exported JSONL (fixes bd-84: enables hash-based auto-import)
	// #nosec G304 - controlled path from config
	jsonlData, err := os.ReadFile(jsonlPath)
	if err == nil {
		hasher := sha256.New()
		hasher.Write(jsonlData)
		exportedHash := hex.EncodeToString(hasher.Sum(nil))
		if err := store.SetMetadata(ctx, "last_import_hash", exportedHash); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_hash after export: %v\n", err)
		}
	}

	// Success!
	recordSuccess()
}

var (
	noAutoFlush  bool
	noAutoImport bool
	sandboxMode  bool
)

func init() {
	// Initialize viper configuration
	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
	}

	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "Database path (default: auto-discover .beads/*.db or ~/.beads/default.db)")
	rootCmd.PersistentFlags().StringVar(&actor, "actor", "", "Actor name for audit trail (default: $BD_ACTOR or $USER)")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVar(&noDaemon, "no-daemon", false, "Force direct storage mode, bypass daemon if running")
	rootCmd.PersistentFlags().BoolVar(&noAutoFlush, "no-auto-flush", false, "Disable automatic JSONL sync after CRUD operations")
	rootCmd.PersistentFlags().BoolVar(&noAutoImport, "no-auto-import", false, "Disable automatic JSONL import when newer than DB")
	rootCmd.PersistentFlags().BoolVar(&sandboxMode, "sandbox", false, "Sandbox mode: disables daemon and auto-sync (equivalent to --no-daemon --no-auto-flush --no-auto-import)")
}

// createIssuesFromMarkdown parses a markdown file and creates multiple issues
//nolint:unparam // cmd parameter required for potential future use
func createIssuesFromMarkdown(cmd *cobra.Command, filepath string) {
	// Parse markdown file
	templates, err := parseMarkdownFile(filepath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing markdown file: %v\n", err)
		os.Exit(1)
	}

	if len(templates) == 0 {
		fmt.Fprintf(os.Stderr, "No issues found in markdown file\n")
		os.Exit(1)
	}

	ctx := context.Background()
	createdIssues := []*types.Issue{}
	failedIssues := []string{}

	// Create each issue
	for _, template := range templates {
		issue := &types.Issue{
			Title:              template.Title,
			Description:        template.Description,
			Design:             template.Design,
			AcceptanceCriteria: template.AcceptanceCriteria,
			Status:             types.StatusOpen,
			Priority:           template.Priority,
			IssueType:          template.IssueType,
			Assignee:           template.Assignee,
		}

		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating issue '%s': %v\n", template.Title, err)
			failedIssues = append(failedIssues, template.Title)
			continue
		}

		// Add labels
		for _, label := range template.Labels {
			if err := store.AddLabel(ctx, issue.ID, label, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add label %s to %s: %v\n", label, issue.ID, err)
			}
		}

		// Add dependencies
		for _, depSpec := range template.Dependencies {
			depSpec = strings.TrimSpace(depSpec)
			if depSpec == "" {
				continue
			}

			var depType types.DependencyType
			var dependsOnID string

			// Parse format: "type:id" or just "id" (defaults to "blocks")
			if strings.Contains(depSpec, ":") {
				parts := strings.SplitN(depSpec, ":", 2)
				if len(parts) != 2 {
					fmt.Fprintf(os.Stderr, "Warning: invalid dependency format '%s' for %s\n", depSpec, issue.ID)
					continue
				}
				depType = types.DependencyType(strings.TrimSpace(parts[0]))
				dependsOnID = strings.TrimSpace(parts[1])
			} else {
				depType = types.DepBlocks
				dependsOnID = depSpec
			}

			if !depType.IsValid() {
				fmt.Fprintf(os.Stderr, "Warning: invalid dependency type '%s' for %s\n", depType, issue.ID)
				continue
			}

			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: dependsOnID,
				Type:        depType,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add dependency %s -> %s: %v\n", issue.ID, dependsOnID, err)
			}
		}

		createdIssues = append(createdIssues, issue)
	}

	// Schedule auto-flush
	if len(createdIssues) > 0 {
		markDirtyAndScheduleFlush()
	}

	// Report failures if any
	if len(failedIssues) > 0 {
		red := color.New(color.FgRed).SprintFunc()
		fmt.Fprintf(os.Stderr, "\n%s Failed to create %d issues:\n", red("✗"), len(failedIssues))
		for _, title := range failedIssues {
			fmt.Fprintf(os.Stderr, "  - %s\n", title)
		}
	}

	if jsonOutput {
		outputJSON(createdIssues)
	} else {
		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Created %d issues from %s:\n", green("✓"), len(createdIssues), filepath)
		for _, issue := range createdIssues {
			fmt.Printf("  %s: %s [P%d, %s]\n", issue.ID, issue.Title, issue.Priority, issue.IssueType)
		}
	}
}

var createCmd = &cobra.Command{
	Use:   "create [title]",
	Short: "Create a new issue (or multiple issues from markdown file)",
	Args:  cobra.MinimumNArgs(0), // Changed to allow no args when using -f
	Run: func(cmd *cobra.Command, args []string) {
		file, _ := cmd.Flags().GetString("file")

		// If file flag is provided, parse markdown and create multiple issues
		if file != "" {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "Error: cannot specify both title and --file flag\n")
				os.Exit(1)
			}
			createIssuesFromMarkdown(cmd, file)
			return
		}

		// Original single-issue creation logic
		// Get title from flag or positional argument
		titleFlag, _ := cmd.Flags().GetString("title")
		var title string

		if len(args) > 0 && titleFlag != "" {
			// Both provided - check if they match
			if args[0] != titleFlag {
				fmt.Fprintf(os.Stderr, "Error: cannot specify different titles as both positional argument and --title flag\n")
				fmt.Fprintf(os.Stderr, "  Positional: %q\n", args[0])
				fmt.Fprintf(os.Stderr, "  --title:    %q\n", titleFlag)
				os.Exit(1)
			}
			title = args[0] // They're the same, use either
		} else if len(args) > 0 {
			title = args[0]
		} else if titleFlag != "" {
			title = titleFlag
		} else {
			fmt.Fprintf(os.Stderr, "Error: title required (or use --file to create from markdown)\n")
			os.Exit(1)
		}
		description, _ := cmd.Flags().GetString("description")
		design, _ := cmd.Flags().GetString("design")
		acceptance, _ := cmd.Flags().GetString("acceptance")
		priority, _ := cmd.Flags().GetInt("priority")
		issueType, _ := cmd.Flags().GetString("type")
		assignee, _ := cmd.Flags().GetString("assignee")
		labels, _ := cmd.Flags().GetStringSlice("labels")
		explicitID, _ := cmd.Flags().GetString("id")
		externalRef, _ := cmd.Flags().GetString("external-ref")
		deps, _ := cmd.Flags().GetStringSlice("deps")
		forceCreate, _ := cmd.Flags().GetBool("force")

		// Validate explicit ID format if provided (prefix-number)
		if explicitID != "" {
			// Check format: must contain hyphen and have numeric suffix
			parts := strings.Split(explicitID, "-")
			if len(parts) != 2 {
				fmt.Fprintf(os.Stderr, "Error: invalid ID format '%s' (expected format: prefix-number, e.g., 'bd-42')\n", explicitID)
				os.Exit(1)
			}
			// Validate numeric suffix
			if _, err := fmt.Sscanf(parts[1], "%d", new(int)); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid ID format '%s' (numeric suffix required, e.g., 'bd-42')\n", explicitID)
				os.Exit(1)
			}

			// Validate prefix matches database prefix (unless --force is used)
			if !forceCreate {
				requestedPrefix := parts[0]
				ctx := context.Background()

				// Get database prefix from config
				var dbPrefix string
				if daemonClient != nil {
					// Using daemon - need to get config via RPC
					// For now, skip validation in daemon mode (needs RPC enhancement)
				} else {
					// Direct mode - check config
					dbPrefix, _ = store.GetConfig(ctx, "issue_prefix")
				}

				if dbPrefix != "" && dbPrefix != requestedPrefix {
					fmt.Fprintf(os.Stderr, "Error: prefix mismatch detected\n")
					fmt.Fprintf(os.Stderr, "  This database uses prefix '%s-', but you specified '%s-'\n", dbPrefix, requestedPrefix)
					fmt.Fprintf(os.Stderr, "  Did you mean to create '%s-%s'?\n", dbPrefix, parts[1])
					fmt.Fprintf(os.Stderr, "  Use --force to create with mismatched prefix anyway\n")
					os.Exit(1)
				}
			}
		}

		var externalRefPtr *string
		if externalRef != "" {
			externalRefPtr = &externalRef
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			createArgs := &rpc.CreateArgs{
				ID:                 explicitID,
				Title:              title,
				Description:        description,
				IssueType:          issueType,
				Priority:           priority,
				Design:             design,
				AcceptanceCriteria: acceptance,
				Assignee:           assignee,
				Labels:             labels,
				Dependencies:       deps,
			}

			resp, err := daemonClient.Create(createArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
			} else {
				var issue types.Issue
				if err := json.Unmarshal(resp.Data, &issue); err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
					os.Exit(1)
				}
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Created issue: %s\n", green("✓"), issue.ID)
				fmt.Printf("  Title: %s\n", issue.Title)
				fmt.Printf("  Priority: P%d\n", issue.Priority)
				fmt.Printf("  Status: %s\n", issue.Status)
			}
			return
		}

		// Direct mode
		issue := &types.Issue{
			ID:                 explicitID, // Set explicit ID if provided (empty string if not)
			Title:              title,
			Description:        description,
			Design:             design,
			AcceptanceCriteria: acceptance,
			Status:             types.StatusOpen,
			Priority:           priority,
			IssueType:          types.IssueType(issueType),
			Assignee:           assignee,
			ExternalRef:        externalRefPtr,
		}

		ctx := context.Background()
		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Add labels if specified
		for _, label := range labels {
			if err := store.AddLabel(ctx, issue.ID, label, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add label %s: %v\n", label, err)
			}
		}

		// Add dependencies if specified (format: type:id or just id for default "blocks" type)
		for _, depSpec := range deps {
			// Skip empty specs (e.g., from trailing commas)
			depSpec = strings.TrimSpace(depSpec)
			if depSpec == "" {
				continue
			}

			var depType types.DependencyType
			var dependsOnID string

			// Parse format: "type:id" or just "id" (defaults to "blocks")
			if strings.Contains(depSpec, ":") {
				parts := strings.SplitN(depSpec, ":", 2)
				if len(parts) != 2 {
					fmt.Fprintf(os.Stderr, "Warning: invalid dependency format '%s', expected 'type:id' or 'id'\n", depSpec)
					continue
				}
				depType = types.DependencyType(strings.TrimSpace(parts[0]))
				dependsOnID = strings.TrimSpace(parts[1])
			} else {
				// Default to "blocks" if no type specified
				depType = types.DepBlocks
				dependsOnID = depSpec
			}

			// Validate dependency type
			if !depType.IsValid() {
				fmt.Fprintf(os.Stderr, "Warning: invalid dependency type '%s' (valid: blocks, related, parent-child, discovered-from)\n", depType)
				continue
			}

			// Add the dependency
			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: dependsOnID,
				Type:        depType,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add dependency %s -> %s: %v\n", issue.ID, dependsOnID, err)
			}
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(issue)
		} else {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Created issue: %s\n", green("✓"), issue.ID)
			fmt.Printf("  Title: %s\n", issue.Title)
			fmt.Printf("  Priority: P%d\n", issue.Priority)
			fmt.Printf("  Status: %s\n", issue.Status)
		}
	},
}

func init() {
	createCmd.Flags().StringP("file", "f", "", "Create multiple issues from markdown file")
	createCmd.Flags().String("title", "", "Issue title (alternative to positional argument)")
	createCmd.Flags().StringP("description", "d", "", "Issue description")
	createCmd.Flags().String("design", "", "Design notes")
	createCmd.Flags().String("acceptance", "", "Acceptance criteria")
	createCmd.Flags().IntP("priority", "p", 2, "Priority (0-4, 0=highest)")
	createCmd.Flags().StringP("type", "t", "task", "Issue type (bug|feature|task|epic|chore)")
	createCmd.Flags().StringP("assignee", "a", "", "Assignee")
	createCmd.Flags().StringSliceP("labels", "l", []string{}, "Labels (comma-separated)")
	createCmd.Flags().String("id", "", "Explicit issue ID (e.g., 'bd-42' for partitioning)")
	createCmd.Flags().String("external-ref", "", "External reference (e.g., 'gh-9', 'jira-ABC')")
	createCmd.Flags().StringSlice("deps", []string{}, "Dependencies in format 'type:id' or 'id' (e.g., 'discovered-from:bd-20,blocks:bd-15' or 'bd-20')")
	createCmd.Flags().Bool("force", false, "Force creation even if prefix doesn't match database prefix")
	rootCmd.AddCommand(createCmd)
}

var showCmd = &cobra.Command{
	Use:   "show [id...]",
	Short: "Show issue details",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// If daemon is running, use RPC
		if daemonClient != nil {
			allDetails := []interface{}{}
			for idx, id := range args {
				showArgs := &rpc.ShowArgs{ID: id}
				resp, err := daemonClient.Show(showArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					type IssueDetails struct {
						types.Issue
						Labels       []string       `json:"labels,omitempty"`
						Dependencies []*types.Issue `json:"dependencies,omitempty"`
						Dependents   []*types.Issue `json:"dependents,omitempty"`
					}
					var details IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err == nil {
						allDetails = append(allDetails, details)
					}
				} else {
					// Check if issue exists (daemon returns null for non-existent issues)
					if string(resp.Data) == "null" || len(resp.Data) == 0 {
						fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
						continue
					}
					if idx > 0 {
						fmt.Println("\n" + strings.Repeat("─", 60))
					}

					// Parse response and use existing formatting code
					type IssueDetails struct {
						types.Issue
						Labels       []string       `json:"labels,omitempty"`
						Dependencies []*types.Issue `json:"dependencies,omitempty"`
						Dependents   []*types.Issue `json:"dependents,omitempty"`
					}
					var details IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err != nil {
						fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
						os.Exit(1)
					}
					issue := &details.Issue

					cyan := color.New(color.FgCyan).SprintFunc()

					// Format output (same as direct mode below)
					tierEmoji := ""
					statusSuffix := ""
					switch issue.CompactionLevel {
					case 1:
						tierEmoji = " 🗜️"
						statusSuffix = " (compacted L1)"
					case 2:
						tierEmoji = " 📦"
						statusSuffix = " (compacted L2)"
					}

					fmt.Printf("\n%s: %s%s\n", cyan(issue.ID), issue.Title, tierEmoji)
					fmt.Printf("Status: %s%s\n", issue.Status, statusSuffix)
					fmt.Printf("Priority: P%d\n", issue.Priority)
					fmt.Printf("Type: %s\n", issue.IssueType)
					if issue.Assignee != "" {
						fmt.Printf("Assignee: %s\n", issue.Assignee)
					}
					if issue.EstimatedMinutes != nil {
						fmt.Printf("Estimated: %d minutes\n", *issue.EstimatedMinutes)
					}
					fmt.Printf("Created: %s\n", issue.CreatedAt.Format("2006-01-02 15:04"))
					fmt.Printf("Updated: %s\n", issue.UpdatedAt.Format("2006-01-02 15:04"))

					// Show compaction status
					if issue.CompactionLevel > 0 {
						fmt.Println()
						if issue.OriginalSize > 0 {
							currentSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
							saved := issue.OriginalSize - currentSize
							if saved > 0 {
								reduction := float64(saved) / float64(issue.OriginalSize) * 100
								fmt.Printf("📊 Original: %d bytes | Compressed: %d bytes (%.0f%% reduction)\n",
									issue.OriginalSize, currentSize, reduction)
							}
						}
						tierEmoji2 := "🗜️"
						if issue.CompactionLevel == 2 {
							tierEmoji2 = "📦"
						}
						compactedDate := ""
						if issue.CompactedAt != nil {
							compactedDate = issue.CompactedAt.Format("2006-01-02")
						}
						fmt.Printf("%s Compacted: %s (Tier %d)\n", tierEmoji2, compactedDate, issue.CompactionLevel)
					}

					if issue.Description != "" {
						fmt.Printf("\nDescription:\n%s\n", issue.Description)
					}
					if issue.Design != "" {
						fmt.Printf("\nDesign:\n%s\n", issue.Design)
					}
					if issue.Notes != "" {
						fmt.Printf("\nNotes:\n%s\n", issue.Notes)
					}
					if issue.AcceptanceCriteria != "" {
						fmt.Printf("\nAcceptance Criteria:\n%s\n", issue.AcceptanceCriteria)
					}

					if len(details.Labels) > 0 {
						fmt.Printf("\nLabels: %v\n", details.Labels)
					}

					if len(details.Dependencies) > 0 {
						fmt.Printf("\nDepends on (%d):\n", len(details.Dependencies))
						for _, dep := range details.Dependencies {
							fmt.Printf("  → %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
						}
					}

					if len(details.Dependents) > 0 {
						fmt.Printf("\nBlocks (%d):\n", len(details.Dependents))
						for _, dep := range details.Dependents {
							fmt.Printf("  ← %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
						}
					}

					fmt.Println()
				}
			}

			if jsonOutput && len(allDetails) > 0 {
				outputJSON(allDetails)
			}
			return
		}

		// Direct mode
		ctx := context.Background()
		allDetails := []interface{}{}
		for idx, id := range args {
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
				continue
			}
			if issue == nil {
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				continue
			}

			if jsonOutput {
				// Include labels, dependencies, and comments in JSON output
				type IssueDetails struct {
					*types.Issue
					Labels       []string         `json:"labels,omitempty"`
					Dependencies []*types.Issue   `json:"dependencies,omitempty"`
					Dependents   []*types.Issue   `json:"dependents,omitempty"`
					Comments     []*types.Comment `json:"comments,omitempty"`
				}
				details := &IssueDetails{Issue: issue}
				details.Labels, _ = store.GetLabels(ctx, issue.ID)
				details.Dependencies, _ = store.GetDependencies(ctx, issue.ID)
				details.Dependents, _ = store.GetDependents(ctx, issue.ID)
				details.Comments, _ = store.GetIssueComments(ctx, issue.ID)
				allDetails = append(allDetails, details)
				continue
			}

			if idx > 0 {
				fmt.Println("\n" + strings.Repeat("─", 60))
			}

			cyan := color.New(color.FgCyan).SprintFunc()

			// Add compaction emoji to title line
			tierEmoji := ""
			statusSuffix := ""
			switch issue.CompactionLevel {
			case 1:
				tierEmoji = " 🗜️"
				statusSuffix = " (compacted L1)"
			case 2:
				tierEmoji = " 📦"
				statusSuffix = " (compacted L2)"
			}

			fmt.Printf("\n%s: %s%s\n", cyan(issue.ID), issue.Title, tierEmoji)
			fmt.Printf("Status: %s%s\n", issue.Status, statusSuffix)
			fmt.Printf("Priority: P%d\n", issue.Priority)
			fmt.Printf("Type: %s\n", issue.IssueType)
			if issue.Assignee != "" {
				fmt.Printf("Assignee: %s\n", issue.Assignee)
			}
			if issue.EstimatedMinutes != nil {
				fmt.Printf("Estimated: %d minutes\n", *issue.EstimatedMinutes)
			}
			fmt.Printf("Created: %s\n", issue.CreatedAt.Format("2006-01-02 15:04"))
			fmt.Printf("Updated: %s\n", issue.UpdatedAt.Format("2006-01-02 15:04"))

			// Show compaction status footer
			if issue.CompactionLevel > 0 {
				tierEmoji := "🗜️"
				if issue.CompactionLevel == 2 {
					tierEmoji = "📦"
				}
				tierName := fmt.Sprintf("Tier %d", issue.CompactionLevel)

				fmt.Println()
				if issue.OriginalSize > 0 {
					currentSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
					saved := issue.OriginalSize - currentSize
					if saved > 0 {
						reduction := float64(saved) / float64(issue.OriginalSize) * 100
						fmt.Printf("📊 Original: %d bytes | Compressed: %d bytes (%.0f%% reduction)\n",
							issue.OriginalSize, currentSize, reduction)
					}
				}
				compactedDate := ""
				if issue.CompactedAt != nil {
					compactedDate = issue.CompactedAt.Format("2006-01-02")
				}
				fmt.Printf("%s Compacted: %s (%s)\n", tierEmoji, compactedDate, tierName)
			}

			if issue.Description != "" {
				fmt.Printf("\nDescription:\n%s\n", issue.Description)
			}
			if issue.Design != "" {
				fmt.Printf("\nDesign:\n%s\n", issue.Design)
			}
			if issue.Notes != "" {
				fmt.Printf("\nNotes:\n%s\n", issue.Notes)
			}
			if issue.AcceptanceCriteria != "" {
				fmt.Printf("\nAcceptance Criteria:\n%s\n", issue.AcceptanceCriteria)
			}

			// Show labels
			labels, _ := store.GetLabels(ctx, issue.ID)
			if len(labels) > 0 {
				fmt.Printf("\nLabels: %v\n", labels)
			}

			// Show dependencies
			deps, _ := store.GetDependencies(ctx, issue.ID)
			if len(deps) > 0 {
				fmt.Printf("\nDepends on (%d):\n", len(deps))
				for _, dep := range deps {
					fmt.Printf("  → %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
				}
			}

			// Show dependents
			dependents, _ := store.GetDependents(ctx, issue.ID)
			if len(dependents) > 0 {
				fmt.Printf("\nBlocks (%d):\n", len(dependents))
				for _, dep := range dependents {
					fmt.Printf("  ← %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
				}
			}

			// Show comments
			comments, _ := store.GetIssueComments(ctx, issue.ID)
			if len(comments) > 0 {
				fmt.Printf("\nComments (%d):\n", len(comments))
				for _, comment := range comments {
					fmt.Printf("  [%s at %s]\n  %s\n\n", comment.Author, comment.CreatedAt.Format("2006-01-02 15:04"), comment.Text)
				}
			}

			fmt.Println()
		}

		if jsonOutput && len(allDetails) > 0 {
			outputJSON(allDetails)
		}
	},
}

func init() {
	rootCmd.AddCommand(showCmd)
}

var updateCmd = &cobra.Command{
	Use:   "update [id...]",
	Short: "Update one or more issues",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		updates := make(map[string]interface{})

		if cmd.Flags().Changed("status") {
			status, _ := cmd.Flags().GetString("status")
			updates["status"] = status
		}
		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			updates["priority"] = priority
		}
		if cmd.Flags().Changed("title") {
			title, _ := cmd.Flags().GetString("title")
			updates["title"] = title
		}
		if cmd.Flags().Changed("assignee") {
			assignee, _ := cmd.Flags().GetString("assignee")
			updates["assignee"] = assignee
		}
		if cmd.Flags().Changed("description") {
			description, _ := cmd.Flags().GetString("description")
			updates["description"] = description
		}
		if cmd.Flags().Changed("design") {
			design, _ := cmd.Flags().GetString("design")
			updates["design"] = design
		}
		if cmd.Flags().Changed("notes") {
			notes, _ := cmd.Flags().GetString("notes")
			updates["notes"] = notes
		}
		if cmd.Flags().Changed("acceptance") || cmd.Flags().Changed("acceptance-criteria") {
			var acceptanceCriteria string
			if cmd.Flags().Changed("acceptance") {
				acceptanceCriteria, _ = cmd.Flags().GetString("acceptance")
			} else {
				acceptanceCriteria, _ = cmd.Flags().GetString("acceptance-criteria")
			}
			updates["acceptance_criteria"] = acceptanceCriteria
		}
		if cmd.Flags().Changed("external-ref") {
			externalRef, _ := cmd.Flags().GetString("external-ref")
			updates["external_ref"] = externalRef
		}

		if len(updates) == 0 {
			fmt.Println("No updates specified")
			return
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			updatedIssues := []*types.Issue{}
			for _, id := range args {
				updateArgs := &rpc.UpdateArgs{ID: id}

				// Map updates to RPC args
				if status, ok := updates["status"].(string); ok {
					updateArgs.Status = &status
				}
				if priority, ok := updates["priority"].(int); ok {
					updateArgs.Priority = &priority
				}
				if title, ok := updates["title"].(string); ok {
					updateArgs.Title = &title
				}
				if assignee, ok := updates["assignee"].(string); ok {
					updateArgs.Assignee = &assignee
				}
				if description, ok := updates["description"].(string); ok {
					updateArgs.Description = &description
				}
				if design, ok := updates["design"].(string); ok {
					updateArgs.Design = &design
				}
				if notes, ok := updates["notes"].(string); ok {
					updateArgs.Notes = &notes
				}
				if acceptanceCriteria, ok := updates["acceptance_criteria"].(string); ok {
					updateArgs.AcceptanceCriteria = &acceptanceCriteria
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						updatedIssues = append(updatedIssues, &issue)
					}
				} else {
					green := color.New(color.FgGreen).SprintFunc()
					fmt.Printf("%s Updated issue: %s\n", green("✓"), id)
				}
			}

			if jsonOutput && len(updatedIssues) > 0 {
				outputJSON(updatedIssues)
			}
			return
		}

		// Direct mode
		ctx := context.Background()
		updatedIssues := []*types.Issue{}
		for _, id := range args {
			if err := store.UpdateIssue(ctx, id, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
				continue
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, id)
				if issue != nil {
					updatedIssues = append(updatedIssues, issue)
				}
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Updated issue: %s\n", green("✓"), id)
			}
		}

		// Schedule auto-flush if any issues were updated
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(updatedIssues) > 0 {
			outputJSON(updatedIssues)
		}
	},
}

func init() {
	updateCmd.Flags().StringP("status", "s", "", "New status")
	updateCmd.Flags().IntP("priority", "p", 0, "New priority")
	updateCmd.Flags().String("title", "", "New title")
	updateCmd.Flags().StringP("assignee", "a", "", "New assignee")
	updateCmd.Flags().StringP("description", "d", "", "Issue description")
	updateCmd.Flags().String("design", "", "Design notes")
	updateCmd.Flags().String("notes", "", "Additional notes")
	updateCmd.Flags().String("acceptance", "", "Acceptance criteria")
	updateCmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	_ = updateCmd.Flags().MarkHidden("acceptance-criteria")
	updateCmd.Flags().String("external-ref", "", "External reference (e.g., 'gh-9', 'jira-ABC')")
	rootCmd.AddCommand(updateCmd)
}

var editCmd = &cobra.Command{
	Use:   "edit [id]",
	Short: "Edit an issue field in $EDITOR",
	Long: `Edit an issue field using your configured $EDITOR.

By default, edits the description. Use flags to edit other fields.

Examples:
  bd edit bd-42                    # Edit description
  bd edit bd-42 --title            # Edit title
  bd edit bd-42 --design           # Edit design notes
  bd edit bd-42 --notes            # Edit notes
  bd edit bd-42 --acceptance       # Edit acceptance criteria`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]
		ctx := context.Background()

		// Determine which field to edit
		fieldToEdit := "description"
		if cmd.Flags().Changed("title") {
			fieldToEdit = "title"
		} else if cmd.Flags().Changed("design") {
			fieldToEdit = "design"
		} else if cmd.Flags().Changed("notes") {
			fieldToEdit = "notes"
		} else if cmd.Flags().Changed("acceptance") {
			fieldToEdit = "acceptance_criteria"
		}

		// Get the editor from environment
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = os.Getenv("VISUAL")
		}
		if editor == "" {
			// Try common defaults
			for _, defaultEditor := range []string{"vim", "vi", "nano", "emacs"} {
				if _, err := exec.LookPath(defaultEditor); err == nil {
					editor = defaultEditor
					break
				}
			}
		}
		if editor == "" {
			fmt.Fprintf(os.Stderr, "Error: No editor found. Set $EDITOR or $VISUAL environment variable.\n")
			os.Exit(1)
		}

		// Get the current issue
		var issue *types.Issue
		var err error

		if daemonClient != nil {
			// Daemon mode
			showArgs := &rpc.ShowArgs{ID: id}
			resp, err := daemonClient.Show(showArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue %s: %v\n", id, err)
				os.Exit(1)
			}

			issue = &types.Issue{}
			if err := json.Unmarshal(resp.Data, issue); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing issue data: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Direct mode
			issue, err = store.GetIssue(ctx, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue %s: %v\n", id, err)
				os.Exit(1)
			}
			if issue == nil {
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				os.Exit(1)
			}
		}

		// Get the current field value
		var currentValue string
		switch fieldToEdit {
		case "title":
			currentValue = issue.Title
		case "description":
			currentValue = issue.Description
		case "design":
			currentValue = issue.Design
		case "notes":
			currentValue = issue.Notes
		case "acceptance_criteria":
			currentValue = issue.AcceptanceCriteria
		}

		// Create a temporary file with the current value
		tmpFile, err := os.CreateTemp("", fmt.Sprintf("bd-edit-%s-*.txt", fieldToEdit))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating temp file: %v\n", err)
			os.Exit(1)
		}
		tmpPath := tmpFile.Name()
		defer os.Remove(tmpPath)

		// Write current value to temp file
		if _, err := tmpFile.WriteString(currentValue); err != nil {
			tmpFile.Close()
			fmt.Fprintf(os.Stderr, "Error writing to temp file: %v\n", err)
			os.Exit(1)
		}
		tmpFile.Close()

		// Open the editor
		editorCmd := exec.Command(editor, tmpPath) // #nosec G204 - user-provided editor command is intentional
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		if err := editorCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running editor: %v\n", err)
			os.Exit(1)
		}

		// Read the edited content
		// #nosec G304 - controlled temp file path
		editedContent, err := os.ReadFile(tmpPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading edited file: %v\n", err)
			os.Exit(1)
		}

		newValue := string(editedContent)

		// Check if the value changed
		if newValue == currentValue {
			fmt.Println("No changes made")
			return
		}

		// Validate title if editing title
		if fieldToEdit == "title" && strings.TrimSpace(newValue) == "" {
			fmt.Fprintf(os.Stderr, "Error: title cannot be empty\n")
			os.Exit(1)
		}

		// Update the issue
		updates := map[string]interface{}{
			fieldToEdit: newValue,
		}

		if daemonClient != nil {
			// Daemon mode
			updateArgs := &rpc.UpdateArgs{ID: id}

			switch fieldToEdit {
			case "title":
				updateArgs.Title = &newValue
			case "description":
				updateArgs.Description = &newValue
			case "design":
				updateArgs.Design = &newValue
			case "notes":
				updateArgs.Notes = &newValue
			case "acceptance_criteria":
				updateArgs.AcceptanceCriteria = &newValue
			}

			_, err := daemonClient.Update(updateArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error updating issue: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Direct mode
			if err := store.UpdateIssue(ctx, id, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating issue: %v\n", err)
				os.Exit(1)
			}
			markDirtyAndScheduleFlush()
		}

		green := color.New(color.FgGreen).SprintFunc()
		fieldName := strings.ReplaceAll(fieldToEdit, "_", " ")
		fmt.Printf("%s Updated %s for issue: %s\n", green("✓"), fieldName, id)
	},
}

func init() {
	editCmd.Flags().Bool("title", false, "Edit the title")
	editCmd.Flags().Bool("description", false, "Edit the description (default)")
	editCmd.Flags().Bool("design", false, "Edit the design notes")
	editCmd.Flags().Bool("notes", false, "Edit the notes")
	editCmd.Flags().Bool("acceptance", false, "Edit the acceptance criteria")
	rootCmd.AddCommand(editCmd)
}

var closeCmd = &cobra.Command{
	Use:   "close [id...]",
	Short: "Close one or more issues",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			reason = "Closed"
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			closedIssues := []*types.Issue{}
			for _, id := range args {
				closeArgs := &rpc.CloseArgs{
					ID:     id,
					Reason: reason,
				}
				resp, err := daemonClient.CloseIssue(closeArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						closedIssues = append(closedIssues, &issue)
					}
				} else {
					green := color.New(color.FgGreen).SprintFunc()
					fmt.Printf("%s Closed %s: %s\n", green("✓"), id, reason)
				}
			}

			if jsonOutput && len(closedIssues) > 0 {
				outputJSON(closedIssues)
			}
			return
		}

		// Direct mode
		ctx := context.Background()
		closedIssues := []*types.Issue{}
		for _, id := range args {
			if err := store.CloseIssue(ctx, id, reason, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}
			if jsonOutput {
				issue, _ := store.GetIssue(ctx, id)
				if issue != nil {
					closedIssues = append(closedIssues, issue)
				}
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Closed %s: %s\n", green("✓"), id, reason)
			}
		}

		// Schedule auto-flush if any issues were closed
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(closedIssues) > 0 {
			outputJSON(closedIssues)
		}
	},
}

func init() {
	closeCmd.Flags().StringP("reason", "r", "", "Reason for closing")
	rootCmd.AddCommand(closeCmd)
}

func main() {
	// Handle --version flag (in addition to 'version' subcommand)
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Printf("bd version %s (%s)\n", Version, Build)
			return
		}
	}

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
