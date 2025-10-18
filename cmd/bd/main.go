package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"golang.org/x/mod/semver"
)

var (
	dbPath     string
	actor      string
	store      storage.Storage
	jsonOutput bool
	
	// Daemon mode
	daemonClient *rpc.Client // RPC client when daemon is running
	noDaemon     bool         // Force direct mode (no daemon)

	// Auto-flush state
	autoFlushEnabled  = true  // Can be disabled with --no-auto-flush
	isDirty           = false // Tracks if DB has changes needing export
	needsFullExport   = false // Set to true when IDs change (renumber, rename-prefix)
	flushMutex        sync.Mutex
	flushTimer        *time.Timer
	flushDebounce     = 5 * time.Second
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
		// Skip database initialization for commands that don't need a database
		if cmd.Name() == "init" || cmd.Name() == "daemon" || cmd.Name() == "help" || cmd.Name() == "version" || cmd.Name() == "quickstart" {
			return
		}

		// Set auto-flush based on flag (invert no-auto-flush)
		autoFlushEnabled = !noAutoFlush

		// Set auto-import based on flag (invert no-auto-import)
		autoImportEnabled = !noAutoImport

		// Initialize database path
		if dbPath == "" {
			// Use public API to find database (same logic as extensions)
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				dbPath = foundDB
			} else {
				// No database found - error out instead of falling back to ~/.beads
				fmt.Fprintf(os.Stderr, "Error: no beads database found\n")
				fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to create a database in the current directory\n")
				fmt.Fprintf(os.Stderr, "      or set BEADS_DB environment variable to specify a database\n")
				os.Exit(1)
			}
		}

		// Set actor from flag, env, or default
		// Priority: --actor flag > BD_ACTOR env > USER env > "unknown"
		if actor == "" {
			if bdActor := os.Getenv("BD_ACTOR"); bdActor != "" {
				actor = bdActor
			} else if user := os.Getenv("USER"); user != "" {
				actor = user
			} else {
				actor = "unknown"
			}
		}

		// Try to connect to daemon first (unless --no-daemon flag is set)
		if !noDaemon {
			socketPath := getSocketPath()
			client, err := rpc.TryConnect(socketPath)
			if err == nil && client != nil {
				daemonClient = client
				if os.Getenv("BD_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "Debug: connected to daemon at %s\n", socketPath)
				}
				return // Skip direct storage initialization
			}
			
			// Daemon not running - try auto-start if enabled
			if shouldAutoStartDaemon() {
				if os.Getenv("BD_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "Debug: attempting to auto-start daemon\n")
				}
				if tryAutoStartDaemon(socketPath) {
					// Retry connection after auto-start
					client, err := rpc.TryConnect(socketPath)
					if err == nil && client != nil {
						daemonClient = client
						if os.Getenv("BD_DEBUG") != "" {
							fmt.Fprintf(os.Stderr, "Debug: connected to auto-started daemon at %s\n", socketPath)
						}
						return // Skip direct storage initialization
					}
				}
			}
			
			if os.Getenv("BD_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "Debug: daemon not available, using direct mode\n")
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

// shouldAutoStartDaemon checks if daemon auto-start is enabled
func shouldAutoStartDaemon() bool {
	// Check environment variable (default: true)
	autoStart := strings.ToLower(strings.TrimSpace(os.Getenv("BEADS_AUTO_START_DAEMON")))
	if autoStart != "" {
		// Accept common falsy values
		return autoStart != "false" && autoStart != "0" && autoStart != "no" && autoStart != "off"
	}
	return true // Default to enabled
}

// tryAutoStartDaemon attempts to start the daemon in the background
// Returns true if daemon was started successfully and socket is ready
func tryAutoStartDaemon(socketPath string) bool {
	// Check if we've failed recently (exponential backoff)
	if !canRetryDaemonStart() {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: skipping auto-start due to recent failures\n")
		}
		return false
	}
	
	// Use lockfile to prevent multiple processes from starting daemon simultaneously
	lockPath := socketPath + ".startlock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		// Someone else is already starting daemon, wait for socket readiness
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: another process is starting daemon, waiting for readiness\n")
		}
		return waitForSocketReadiness(socketPath, 5*time.Second)
	}
	
	// Write our PID to lockfile
	fmt.Fprintf(lockFile, "%d\n", os.Getpid())
	lockFile.Close()
	defer os.Remove(lockPath)
	
	// Determine if we should start global or local daemon
	isGlobal := false
	if home, err := os.UserHomeDir(); err == nil {
		globalSocket := filepath.Join(home, ".beads", "bd.sock")
		if socketPath == globalSocket {
			isGlobal = true
		}
	}
	
	// Build daemon command using absolute path for security
	binPath, err := os.Executable()
	if err != nil {
		binPath = os.Args[0] // Fallback
	}
	
	args := []string{"daemon"}
	if isGlobal {
		args = append(args, "--global")
	}
	
	// Start daemon in background with proper I/O redirection
	cmd := exec.Command(binPath, args...)
	
	// Redirect stdio to /dev/null to prevent daemon output in foreground
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.Stdin = devNull
		defer devNull.Close()
	}
	
	// Set working directory to database directory for local daemon
	if !isGlobal && dbPath != "" {
		cmd.Dir = filepath.Dir(dbPath)
	}
	
	// Detach from parent process
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	
	if err := cmd.Start(); err != nil {
		recordDaemonStartFailure()
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: failed to start daemon: %v\n", err)
		}
		return false
	}
	
	// Reap the process to avoid zombies
	go cmd.Wait()
	
	// Wait for socket to be ready with actual connection test
	if waitForSocketReadiness(socketPath, 5*time.Second) {
		recordDaemonStartSuccess()
		return true
	}
	
	recordDaemonStartFailure()
	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: daemon socket not ready after 5 seconds\n")
	}
	return false
}

// waitForSocketReadiness waits for daemon socket to be ready by testing actual connections
func waitForSocketReadiness(socketPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Try actual connection, not just file existence
		client, err := rpc.TryConnect(socketPath)
		if err == nil && client != nil {
			client.Close()
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
	backoff := time.Duration(5*(1<<uint(daemonStartFailures-1))) * time.Second
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
// If no local socket exists, check for global socket at ~/.beads/bd.sock
func getSocketPath() string {
	// First check local socket (same directory as database: .beads/bd.sock)
	localSocket := filepath.Join(filepath.Dir(dbPath), "bd.sock")
	if _, err := os.Stat(localSocket); err == nil {
		return localSocket
	}
	
	// Fall back to global socket at ~/.beads/bd.sock
	if home, err := os.UserHomeDir(); err == nil {
		globalSocket := filepath.Join(home, ".beads", "bd.sock")
		if _, err := os.Stat(globalSocket); err == nil {
			return globalSocket
		}
	}
	
	// Default to local socket even if it doesn't exist
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
func findJSONLPath() string {
	// Use public API for path discovery
	jsonlPath := beads.FindJSONLPath(dbPath)

	// Ensure the directory exists (important for new databases)
	// This is the only difference from the public API - we create the directory
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
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
	// Find JSONL path
	jsonlPath := findJSONLPath()

	// Read JSONL file
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		// JSONL doesn't exist or can't be accessed, skip import
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: auto-import skipped, JSONL not found: %v\n", err)
		}
		return
	}

	// Compute current JSONL hash
	hasher := sha256.New()
	hasher.Write(jsonlData)
	currentHash := hex.EncodeToString(hasher.Sum(nil))

	// Get last import hash from DB metadata
	ctx := context.Background()
	lastHash, err := store.GetMetadata(ctx, "last_import_hash")
	if err != nil {
		// Metadata error - treat as first import rather than skipping (bd-663)
		// This allows auto-import to recover from corrupt/missing metadata
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: metadata read failed (%v), treating as first import\n", err)
		}
		lastHash = ""
	}

	// Compare hashes
	if currentHash == lastHash {
		// Content unchanged, skip import
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: auto-import skipped, JSONL unchanged (hash match)\n")
		}
		return
	}

	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: auto-import triggered (hash changed)\n")
	}

	// Check for Git merge conflict markers (bd-270)
	// Only match if they appear as standalone lines (not embedded in JSON strings)
	lines := bytes.Split(jsonlData, []byte("\n"))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("<<<<<<< ")) ||
			bytes.Equal(trimmed, []byte("=======")) ||
			bytes.HasPrefix(trimmed, []byte(">>>>>>> ")) {
			fmt.Fprintf(os.Stderr, "\n❌ Git merge conflict detected in %s\n\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "The JSONL file contains unresolved merge conflict markers.\n")
			fmt.Fprintf(os.Stderr, "This prevents auto-import from loading your issues.\n\n")
			fmt.Fprintf(os.Stderr, "To resolve:\n")
			fmt.Fprintf(os.Stderr, "  1. Resolve the merge conflict in your Git client, OR\n")
			fmt.Fprintf(os.Stderr, "  2. Export from database to regenerate clean JSONL:\n")
			fmt.Fprintf(os.Stderr, "     bd export -o %s\n\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "After resolving, commit the fixed JSONL file.\n")
			return
		}
	}

	// Content changed - parse all issues
	scanner := bufio.NewScanner(strings.NewReader(string(jsonlData)))
	scanner.Buffer(make([]byte, 0, 1024), 2*1024*1024) // 2MB buffer for large JSON lines
	var allIssues []*types.Issue
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			// Parse error, skip this import
			snippet := line
			if len(snippet) > 80 {
				snippet = snippet[:80] + "..."
			}
			fmt.Fprintf(os.Stderr, "Auto-import skipped: parse error at line %d: %v\nSnippet: %s\n", lineNo, err, snippet)
			return
		}

		allIssues = append(allIssues, &issue)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import skipped: scanner error: %v\n", err)
		return
	}

	// Detect collisions before importing (bd-228 fix)
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		// Not SQLite - skip auto-import to avoid silent data loss without collision detection
		fmt.Fprintf(os.Stderr, "Auto-import disabled for non-SQLite backend (no collision detection).\n")
		fmt.Fprintf(os.Stderr, "To import manually, run: bd import -i %s\n", jsonlPath)
		return
	}

	collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, allIssues)
	if err != nil {
		// Collision detection failed, skip import to be safe
		fmt.Fprintf(os.Stderr, "Auto-import skipped: collision detection error: %v\n", err)
		return
	}

	// If collisions detected, auto-resolve them by remapping to new IDs
	if len(collisionResult.Collisions) > 0 {
		// Get all existing issues for scoring
		allExistingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Auto-import failed: error getting existing issues: %v\n", err)
			return
		}

		// Score collisions
		if err := sqlite.ScoreCollisions(ctx, sqliteStore, collisionResult.Collisions, allExistingIssues); err != nil {
			fmt.Fprintf(os.Stderr, "Auto-import failed: error scoring collisions: %v\n", err)
			return
		}

		// Remap collisions
		idMapping, err := sqlite.RemapCollisions(ctx, sqliteStore, collisionResult.Collisions, allExistingIssues)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Auto-import failed: error remapping collisions: %v\n", err)
			return
		}
		
		// Show concise notification
		maxShow := 10
		numRemapped := len(idMapping)
		if numRemapped < maxShow {
			maxShow = numRemapped
		}
		
		fmt.Fprintf(os.Stderr, "\nAuto-import: remapped %d colliding issue(s) to new IDs:\n", numRemapped)
		i := 0
		for oldID, newID := range idMapping {
			if i >= maxShow {
				break
			}
			// Find the collision detail to get title
			var title string
			for _, collision := range collisionResult.Collisions {
				if collision.ID == oldID {
					title = collision.IncomingIssue.Title
					break
				}
			}
			fmt.Fprintf(os.Stderr, "  %s → %s (%s)\n", oldID, newID, title)
			i++
		}
		if numRemapped > maxShow {
			fmt.Fprintf(os.Stderr, "  ... and %d more\n", numRemapped-maxShow)
		}
		fmt.Fprintf(os.Stderr, "\n")
		
		// Remove colliding issues from allIssues (they were already created with new IDs by RemapCollisions)
		collidingIDs := make(map[string]bool)
		for _, collision := range collisionResult.Collisions {
			collidingIDs[collision.ID] = true
		}
		filteredIssues := make([]*types.Issue, 0)
		for _, issue := range allIssues {
			if !collidingIDs[issue.ID] {
				filteredIssues = append(filteredIssues, issue)
			}
		}
		allIssues = filteredIssues
	}

	// Batch fetch all existing issues to avoid N+1 query pattern (bd-666)
	allExistingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import failed: error fetching existing issues: %v\n", err)
		return
	}
	
	// Build map for O(1) lookup
	existingByID := make(map[string]*types.Issue)
	for _, issue := range allExistingIssues {
		existingByID[issue.ID] = issue
	}

	// Import non-colliding issues (exact matches + new issues)
	for _, issue := range allIssues {
		existing := existingByID[issue.ID]

		if existing != nil {
			// Update existing issue
			updates := make(map[string]interface{})
			updates["title"] = issue.Title
			updates["description"] = issue.Description
			updates["design"] = issue.Design
			updates["acceptance_criteria"] = issue.AcceptanceCriteria
			updates["notes"] = issue.Notes
			updates["status"] = issue.Status
			updates["priority"] = issue.Priority
			updates["issue_type"] = issue.IssueType
			updates["assignee"] = issue.Assignee
			if issue.EstimatedMinutes != nil {
				updates["estimated_minutes"] = *issue.EstimatedMinutes
			}
			if issue.ExternalRef != nil {
				updates["external_ref"] = *issue.ExternalRef
			}

			// Enforce status/closed_at invariant (bd-226)
			if issue.Status == "closed" {
				// Issue is closed - ensure closed_at is set
				if issue.ClosedAt != nil {
					updates["closed_at"] = *issue.ClosedAt
				} else if !issue.UpdatedAt.IsZero() {
					updates["closed_at"] = issue.UpdatedAt
				} else {
					updates["closed_at"] = time.Now().UTC()
				}
			} else {
				// Issue is not closed - ensure closed_at is null
				updates["closed_at"] = nil
			}

			_ = store.UpdateIssue(ctx, issue.ID, updates, "auto-import")
		} else {
			// Create new issue - enforce invariant before creation
			if issue.Status == "closed" {
				if issue.ClosedAt == nil {
					now := time.Now().UTC()
					issue.ClosedAt = &now
				}
			} else {
				issue.ClosedAt = nil
			}
			_ = store.CreateIssue(ctx, issue, "auto-import")
		}
	}

	// Import dependencies
	for _, issue := range allIssues {
		if len(issue.Dependencies) == 0 {
			continue
		}

		// Get existing dependencies
		existingDeps, err := store.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			continue
		}

		// Add missing dependencies
		for _, dep := range issue.Dependencies {
			exists := false
			for _, existing := range existingDeps {
				if existing.DependsOnID == dep.DependsOnID && existing.Type == dep.Type {
					exists = true
					break
				}
			}

			if !exists {
				_ = store.AddDependency(ctx, dep, "auto-import")
			}
		}
	}

	// Store new hash after successful import
	if err := store.SetMetadata(ctx, "last_import_hash", currentHash); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_hash after import: %v\n", err)
		fmt.Fprintf(os.Stderr, "This may cause auto-import to retry the same import on next operation.\n")
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
			fmt.Fprintf(os.Stderr, "%s\n\n", yellow("⚠️  The database will be upgraded automatically."))
			// Update stored version to current
			_ = store.SetMetadata(ctx, "bd_version", Version)
		}
	}

	// Always update the version metadata to track last-used version
	// This is safe even if versions match (idempotent operation)
	_ = store.SetMetadata(ctx, "bd_version", Version)
}

// markDirtyAndScheduleFlush marks the database as dirty and schedules a flush
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
	flushTimer = time.AfterFunc(flushDebounce, func() {
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
	flushTimer = time.AfterFunc(flushDebounce, func() {
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
			existingFile.Close()
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
	f, err := os.Create(tempPath)
	if err != nil {
		recordFailure(fmt.Errorf("failed to create temp file: %w", err))
		return
	}

	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			f.Close()
			os.Remove(tempPath)
			recordFailure(fmt.Errorf("failed to encode issue %s: %w", issue.ID, err))
			return
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tempPath)
		recordFailure(fmt.Errorf("failed to close temp file: %w", err))
		return
	}

	// Atomic rename
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		os.Remove(tempPath)
		recordFailure(fmt.Errorf("failed to rename file: %w", err))
		return
	}

	// Clear only the dirty issues that were actually exported (fixes bd-52 race condition)
	if err := store.ClearDirtyIssuesByID(ctx, dirtyIDs); err != nil {
		// Don't fail the whole flush for this, but warn
		fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty issues: %v\n", err)
	}

	// Store hash of exported JSONL (fixes bd-84: enables hash-based auto-import)
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
)

func init() {
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "Database path (default: auto-discover .beads/*.db or ~/.beads/default.db)")
	rootCmd.PersistentFlags().StringVar(&actor, "actor", "", "Actor name for audit trail (default: $BD_ACTOR or $USER)")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVar(&noDaemon, "no-daemon", false, "Force direct storage mode, bypass daemon if running")
	rootCmd.PersistentFlags().BoolVar(&noAutoFlush, "no-auto-flush", false, "Disable automatic JSONL sync after CRUD operations")
	rootCmd.PersistentFlags().BoolVar(&noAutoImport, "no-auto-import", false, "Disable automatic JSONL import when newer than DB")
}

// createIssuesFromMarkdown parses a markdown file and creates multiple issues
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
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "Error: title required (or use --file to create from markdown)\n")
			os.Exit(1)
		}

		title := args[0]
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
	Use:   "show [id]",
	Short: "Show issue details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// If daemon is running, use RPC
		if daemonClient != nil {
			showArgs := &rpc.ShowArgs{ID: args[0]}
			resp, err := daemonClient.Show(showArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
			} else {
				// Check if issue exists (daemon returns null for non-existent issues)
				if string(resp.Data) == "null" || len(resp.Data) == 0 {
					fmt.Fprintf(os.Stderr, "Issue %s not found\n", args[0])
					os.Exit(1)
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
				if issue.CompactionLevel == 1 {
					tierEmoji = " 🗜️"
				} else if issue.CompactionLevel == 2 {
					tierEmoji = " 📦"
				}
				if issue.CompactionLevel > 0 {
					statusSuffix = fmt.Sprintf(" (compacted L%d)", issue.CompactionLevel)
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
			return
		}

		// Direct mode
		ctx := context.Background()
		issue, err := store.GetIssue(ctx, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if issue == nil {
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", args[0])
			os.Exit(1)
		}

		if jsonOutput {
			// Include labels and dependencies in JSON output
			type IssueDetails struct {
				*types.Issue
				Labels      []string       `json:"labels,omitempty"`
				Dependencies []*types.Issue `json:"dependencies,omitempty"`
				Dependents   []*types.Issue `json:"dependents,omitempty"`
			}
			details := &IssueDetails{Issue: issue}
			details.Labels, _ = store.GetLabels(ctx, issue.ID)
			details.Dependencies, _ = store.GetDependencies(ctx, issue.ID)
			details.Dependents, _ = store.GetDependents(ctx, issue.ID)
			outputJSON(details)
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		
		// Add compaction emoji to title line
		tierEmoji := ""
		statusSuffix := ""
		if issue.CompactionLevel == 1 {
			tierEmoji = " 🗜️"
		} else if issue.CompactionLevel == 2 {
			tierEmoji = " 📦"
		}
		if issue.CompactionLevel > 0 {
			statusSuffix = fmt.Sprintf(" (compacted L%d)", issue.CompactionLevel)
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

		fmt.Println()
	},
}

func init() {
	rootCmd.AddCommand(showCmd)
}

var updateCmd = &cobra.Command{
	Use:   "update [id]",
	Short: "Update an issue",
	Args:  cobra.ExactArgs(1),
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
		if cmd.Flags().Changed("design") {
			design, _ := cmd.Flags().GetString("design")
			updates["design"] = design
		}
		if cmd.Flags().Changed("notes") {
			notes, _ := cmd.Flags().GetString("notes")
			updates["notes"] = notes
		}
		if cmd.Flags().Changed("acceptance-criteria") {
			acceptanceCriteria, _ := cmd.Flags().GetString("acceptance-criteria")
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
			updateArgs := &rpc.UpdateArgs{ID: args[0]}
			
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
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Updated issue: %s\n", green("✓"), args[0])
			}
			return
		}

		// Direct mode
		ctx := context.Background()
		if err := store.UpdateIssue(ctx, args[0], updates, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			// Fetch updated issue and output
			issue, _ := store.GetIssue(ctx, args[0])
			outputJSON(issue)
		} else {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Updated issue: %s\n", green("✓"), args[0])
		}
	},
}

func init() {
	updateCmd.Flags().StringP("status", "s", "", "New status")
	updateCmd.Flags().IntP("priority", "p", 0, "New priority")
	updateCmd.Flags().String("title", "", "New title")
	updateCmd.Flags().StringP("assignee", "a", "", "New assignee")
	updateCmd.Flags().String("design", "", "Design notes")
	updateCmd.Flags().String("notes", "", "Additional notes")
	updateCmd.Flags().String("acceptance-criteria", "", "Acceptance criteria")
	updateCmd.Flags().String("external-ref", "", "External reference (e.g., 'gh-9', 'jira-ABC')")
	rootCmd.AddCommand(updateCmd)
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
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
