// Package main provides the CommandContext struct that consolidates runtime state.
// This addresses the code smell of 20+ global variables in main.go by grouping
// related state into a single struct for better testability and clearer ownership.
package main

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
)

// CommandContext holds all runtime state for command execution.
// This consolidates the previously scattered global variables for:
// - Better testability (can inject mock contexts)
// - Clearer state ownership (all state in one place)
// - Reduced global count (20+ globals → 1 context)
// - Thread safety (mutexes grouped with the data they protect)
type CommandContext struct {
	// Configuration (derived from flags and config)
	DBPath       string
	Actor        string
	JSONOutput   bool
	NoDaemon     bool
	SandboxMode  bool
	AllowStale   bool
	NoDb         bool
	ReadonlyMode bool
	LockTimeout  time.Duration
	Verbose      bool
	Quiet        bool

	// Runtime state
	Store        storage.Storage
	DaemonClient *rpc.Client
	DaemonStatus DaemonStatus
	RootCtx      context.Context
	RootCancel   context.CancelFunc
	HookRunner   *hooks.Runner

	// Auto-flush state (grouped with protecting mutex)
	FlushManager      *FlushManager
	AutoFlushEnabled  bool
	FlushMutex        sync.Mutex
	StoreMutex        sync.Mutex // Protects Store access from background goroutine
	StoreActive       bool       // Tracks if Store is available
	FlushFailureCount int        // Consecutive flush failures
	LastFlushError    error      // Last flush error for debugging
	SkipFinalFlush    bool       // Set by sync command to prevent re-export

	// Auto-import state
	AutoImportEnabled bool

	// Version tracking
	VersionUpgradeDetected bool
	PreviousVersion        string
	UpgradeAcknowledged    bool

	// Profiling
	ProfileFile *os.File
	TraceFile   *os.File
}

// cmdCtx is the global CommandContext instance.
// Commands access state through this single point instead of scattered globals.
var cmdCtx *CommandContext

// testModeUseGlobals when true forces accessor functions to use legacy globals.
// This ensures backward compatibility with tests that manipulate globals directly.
var testModeUseGlobals bool

// initCommandContext creates and initializes a new CommandContext.
// Called from PersistentPreRun to set up runtime state.
func initCommandContext() {
	cmdCtx = &CommandContext{
		AutoFlushEnabled:  true,
		AutoImportEnabled: true,
	}
}

// GetCommandContext returns the current CommandContext.
// Returns nil if called before initialization (e.g., during init() or help).
func GetCommandContext() *CommandContext {
	return cmdCtx
}

// resetCommandContext clears the CommandContext for testing.
// This ensures tests that manipulate globals directly work correctly.
// Only call this in tests, never in production code.
func resetCommandContext() {
	cmdCtx = nil
}

// enableTestModeGlobals forces accessor functions to use legacy globals.
// This ensures backward compatibility with tests that manipulate globals directly.
func enableTestModeGlobals() {
	testModeUseGlobals = true
	cmdCtx = nil
}

// shouldUseGlobals returns true if accessor functions should use globals.
func shouldUseGlobals() bool {
	return testModeUseGlobals || cmdCtx == nil
}

// The following accessor functions provide backward-compatible access
// to the CommandContext fields. Commands can use these during the
// migration period, and they can be gradually replaced with direct
// cmdCtx access as files are updated.

// getStore returns the current storage backend (daemon client or direct SQLite).
// This is the primary way commands should access storage.
func getStore() storage.Storage {
	if shouldUseGlobals() {
		return store // fallback to legacy global during transition
	}
	return cmdCtx.Store
}

// setStore updates the storage backend in the CommandContext.
func setStore(s storage.Storage) {
	if cmdCtx != nil {
		cmdCtx.Store = s
	}
	store = s // keep legacy global in sync during transition
}

// getActor returns the current actor name for audit trail.
func getActor() string {
	if shouldUseGlobals() {
		return actor
	}
	return cmdCtx.Actor
}

// setActor updates the actor name in the CommandContext.
func setActor(a string) {
	if cmdCtx != nil {
		cmdCtx.Actor = a
	}
	actor = a
}

// getDaemonClient returns the RPC client for daemon mode, or nil in direct mode.
func getDaemonClient() *rpc.Client {
	if shouldUseGlobals() {
		return daemonClient
	}
	return cmdCtx.DaemonClient
}

// setDaemonClient updates the daemon client in the CommandContext.
func setDaemonClient(c *rpc.Client) {
	if cmdCtx != nil {
		cmdCtx.DaemonClient = c
	}
	daemonClient = c
}

// isJSONOutput returns true if JSON output mode is enabled.
func isJSONOutput() bool {
	if shouldUseGlobals() {
		return jsonOutput
	}
	return cmdCtx.JSONOutput
}

// setJSONOutput updates the JSON output flag.
func setJSONOutput(j bool) {
	if cmdCtx != nil {
		cmdCtx.JSONOutput = j
	}
	jsonOutput = j
}

// getDBPath returns the database path.
func getDBPath() string {
	if shouldUseGlobals() {
		return dbPath
	}
	return cmdCtx.DBPath
}

// setDBPath updates the database path.
func setDBPath(p string) {
	if cmdCtx != nil {
		cmdCtx.DBPath = p
	}
	dbPath = p
}

// getRootContext returns the signal-aware root context.
// Returns context.Background() if the root context is nil (e.g., before CLI initialization).
func getRootContext() context.Context {
	var ctx context.Context
	if shouldUseGlobals() {
		ctx = rootCtx
	} else {
		ctx = cmdCtx.RootCtx
	}
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// setRootContext updates the root context and cancel function.
func setRootContext(ctx context.Context, cancel context.CancelFunc) {
	if cmdCtx != nil {
		cmdCtx.RootCtx = ctx
		cmdCtx.RootCancel = cancel
	}
	rootCtx = ctx
	rootCancel = cancel
}

// getHookRunner returns the hook runner instance.
func getHookRunner() *hooks.Runner {
	if shouldUseGlobals() {
		return hookRunner
	}
	return cmdCtx.HookRunner
}

// setHookRunner updates the hook runner.
func setHookRunner(h *hooks.Runner) {
	if cmdCtx != nil {
		cmdCtx.HookRunner = h
	}
	hookRunner = h
}

// isAutoFlushEnabled returns true if auto-flush is enabled.
func isAutoFlushEnabled() bool {
	if shouldUseGlobals() {
		return autoFlushEnabled
	}
	return cmdCtx.AutoFlushEnabled
}

// setAutoFlushEnabled updates the auto-flush flag.
func setAutoFlushEnabled(enabled bool) {
	if cmdCtx != nil {
		cmdCtx.AutoFlushEnabled = enabled
	}
	autoFlushEnabled = enabled
}

// isAutoImportEnabled returns true if auto-import is enabled.
func isAutoImportEnabled() bool {
	if shouldUseGlobals() {
		return autoImportEnabled
	}
	return cmdCtx.AutoImportEnabled
}

// setAutoImportEnabled updates the auto-import flag.
func setAutoImportEnabled(enabled bool) {
	if cmdCtx != nil {
		cmdCtx.AutoImportEnabled = enabled
	}
	autoImportEnabled = enabled
}

// getFlushManager returns the flush manager instance.
func getFlushManager() *FlushManager {
	if shouldUseGlobals() {
		return flushManager
	}
	return cmdCtx.FlushManager
}

// setFlushManager updates the flush manager.
func setFlushManager(fm *FlushManager) {
	if cmdCtx != nil {
		cmdCtx.FlushManager = fm
	}
	flushManager = fm
}

// getDaemonStatus returns the current daemon status.
func getDaemonStatus() DaemonStatus {
	if shouldUseGlobals() {
		return daemonStatus
	}
	return cmdCtx.DaemonStatus
}

// setDaemonStatus updates the daemon status.
func setDaemonStatus(ds DaemonStatus) {
	if cmdCtx != nil {
		cmdCtx.DaemonStatus = ds
	}
	daemonStatus = ds
}

// isNoDaemon returns true if daemon mode is disabled.
func isNoDaemon() bool {
	if shouldUseGlobals() {
		return noDaemon
	}
	return cmdCtx.NoDaemon
}

// setNoDaemon updates the no-daemon flag.
func setNoDaemon(nd bool) {
	if cmdCtx != nil {
		cmdCtx.NoDaemon = nd
	}
	noDaemon = nd
}

// isReadonlyMode returns true if read-only mode is enabled.
func isReadonlyMode() bool {
	if shouldUseGlobals() {
		return readonlyMode
	}
	return cmdCtx.ReadonlyMode
}

// getLockTimeout returns the SQLite lock timeout.
func getLockTimeout() time.Duration {
	if shouldUseGlobals() {
		return lockTimeout
	}
	return cmdCtx.LockTimeout
}

// isSkipFinalFlush returns true if final flush should be skipped.
func isSkipFinalFlush() bool {
	if shouldUseGlobals() {
		return skipFinalFlush
	}
	return cmdCtx.SkipFinalFlush
}

// setSkipFinalFlush updates the skip final flush flag.
func setSkipFinalFlush(skip bool) {
	if cmdCtx != nil {
		cmdCtx.SkipFinalFlush = skip
	}
	skipFinalFlush = skip
}

// lockStore acquires the store mutex for thread-safe access.
func lockStore() {
	if cmdCtx != nil {
		cmdCtx.StoreMutex.Lock()
	} else {
		storeMutex.Lock()
	}
}

// unlockStore releases the store mutex.
func unlockStore() {
	if cmdCtx != nil {
		cmdCtx.StoreMutex.Unlock()
	} else {
		storeMutex.Unlock()
	}
}

// isStoreActive returns true if the store is currently available.
func isStoreActive() bool {
	if cmdCtx != nil {
		return cmdCtx.StoreActive
	}
	return storeActive
}

// setStoreActive updates the store active flag.
func setStoreActive(active bool) {
	if cmdCtx != nil {
		cmdCtx.StoreActive = active
	}
	storeActive = active
}

// lockFlush acquires the flush mutex for thread-safe flush operations.
func lockFlush() {
	if cmdCtx != nil {
		cmdCtx.FlushMutex.Lock()
	} else {
		flushMutex.Lock()
	}
}

// unlockFlush releases the flush mutex.
func unlockFlush() {
	if cmdCtx != nil {
		cmdCtx.FlushMutex.Unlock()
	} else {
		flushMutex.Unlock()
	}
}

// isVerbose returns true if verbose mode is enabled.
func isVerbose() bool {
	if shouldUseGlobals() {
		return verboseFlag
	}
	return cmdCtx.Verbose
}

// isQuiet returns true if quiet mode is enabled.
func isQuiet() bool {
	if shouldUseGlobals() {
		return quietFlag
	}
	return cmdCtx.Quiet
}

// isNoDb returns true if no-db mode is enabled.
func isNoDb() bool {
	if shouldUseGlobals() {
		return noDb
	}
	return cmdCtx.NoDb
}

// setNoDb updates the no-db flag.
func setNoDb(nd bool) {
	if cmdCtx != nil {
		cmdCtx.NoDb = nd
	}
	noDb = nd
}

// isSandboxMode returns true if sandbox mode is enabled.
func isSandboxMode() bool {
	if shouldUseGlobals() {
		return sandboxMode
	}
	return cmdCtx.SandboxMode
}

// setSandboxMode updates the sandbox mode flag.
func setSandboxMode(sm bool) {
	if cmdCtx != nil {
		cmdCtx.SandboxMode = sm
	}
	sandboxMode = sm
}

// isVersionUpgradeDetected returns true if a version upgrade was detected.
func isVersionUpgradeDetected() bool {
	if shouldUseGlobals() {
		return versionUpgradeDetected
	}
	return cmdCtx.VersionUpgradeDetected
}

// setVersionUpgradeDetected updates the version upgrade detected flag.
func setVersionUpgradeDetected(detected bool) {
	if cmdCtx != nil {
		cmdCtx.VersionUpgradeDetected = detected
	}
	versionUpgradeDetected = detected
}

// getPreviousVersion returns the previous bd version.
func getPreviousVersion() string {
	if shouldUseGlobals() {
		return previousVersion
	}
	return cmdCtx.PreviousVersion
}

// setPreviousVersion updates the previous version.
func setPreviousVersion(v string) {
	if cmdCtx != nil {
		cmdCtx.PreviousVersion = v
	}
	previousVersion = v
}

// isUpgradeAcknowledged returns true if the upgrade notification was shown.
func isUpgradeAcknowledged() bool {
	if shouldUseGlobals() {
		return upgradeAcknowledged
	}
	return cmdCtx.UpgradeAcknowledged
}

// setUpgradeAcknowledged updates the upgrade acknowledged flag.
func setUpgradeAcknowledged(ack bool) {
	if cmdCtx != nil {
		cmdCtx.UpgradeAcknowledged = ack
	}
	upgradeAcknowledged = ack
}

// getProfileFile returns the CPU profile file handle.
func getProfileFile() *os.File {
	if shouldUseGlobals() {
		return profileFile
	}
	return cmdCtx.ProfileFile
}

// setProfileFile updates the CPU profile file handle.
func setProfileFile(f *os.File) {
	if cmdCtx != nil {
		cmdCtx.ProfileFile = f
	}
	profileFile = f
}

// getTraceFile returns the trace file handle.
func getTraceFile() *os.File {
	if shouldUseGlobals() {
		return traceFile
	}
	return cmdCtx.TraceFile
}

// setTraceFile updates the trace file handle.
func setTraceFile(f *os.File) {
	if cmdCtx != nil {
		cmdCtx.TraceFile = f
	}
	traceFile = f
}

// isAllowStale returns true if staleness checks should be skipped.
func isAllowStale() bool {
	if shouldUseGlobals() {
		return allowStale
	}
	return cmdCtx.AllowStale
}

// syncCommandContext copies all legacy global values to the CommandContext.
// This is called after initialization is complete to ensure cmdCtx has all values.
// During the transition period, this keeps cmdCtx in sync with globals.
func syncCommandContext() {
	if shouldUseGlobals() {
		return
	}

	// Configuration
	cmdCtx.DBPath = dbPath
	cmdCtx.Actor = actor
	cmdCtx.JSONOutput = jsonOutput
	cmdCtx.NoDaemon = noDaemon
	cmdCtx.SandboxMode = sandboxMode
	cmdCtx.AllowStale = allowStale
	cmdCtx.NoDb = noDb
	cmdCtx.ReadonlyMode = readonlyMode
	cmdCtx.LockTimeout = lockTimeout
	cmdCtx.Verbose = verboseFlag
	cmdCtx.Quiet = quietFlag

	// Runtime state
	cmdCtx.Store = store
	cmdCtx.DaemonClient = daemonClient
	cmdCtx.DaemonStatus = daemonStatus
	cmdCtx.RootCtx = rootCtx
	cmdCtx.RootCancel = rootCancel
	cmdCtx.HookRunner = hookRunner

	// Auto-flush state
	cmdCtx.FlushManager = flushManager
	cmdCtx.AutoFlushEnabled = autoFlushEnabled
	cmdCtx.StoreActive = storeActive
	cmdCtx.FlushFailureCount = flushFailureCount
	cmdCtx.LastFlushError = lastFlushError
	cmdCtx.SkipFinalFlush = skipFinalFlush

	// Auto-import state
	cmdCtx.AutoImportEnabled = autoImportEnabled

	// Version tracking
	cmdCtx.VersionUpgradeDetected = versionUpgradeDetected
	cmdCtx.PreviousVersion = previousVersion
	cmdCtx.UpgradeAcknowledged = upgradeAcknowledged

	// Profiling
	cmdCtx.ProfileFile = profileFile
	cmdCtx.TraceFile = traceFile
}
