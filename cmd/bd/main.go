package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var (
	dbPath     string
	actor      string
	store      storage.Storage
	jsonOutput bool

	// Auto-flush state
	autoFlushEnabled  = true // Can be disabled with --no-auto-flush
	isDirty           = false
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
		// Skip database initialization for init command
		if cmd.Name() == "init" {
			return
		}

		// Set auto-flush based on flag (invert no-auto-flush)
		autoFlushEnabled = !noAutoFlush

		// Set auto-import based on flag (invert no-auto-import)
		autoImportEnabled = !noAutoImport

		// Initialize storage
		if dbPath == "" {
			// Use public API to find database (same logic as extensions)
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				dbPath = foundDB
			} else {
				// Fallback to default location (will be created by init command)
				home, err := os.UserHomeDir()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not determine home directory: %v\n", err)
					fmt.Fprintf(os.Stderr, "Using current directory for default database\n")
					home = "."
				}
				dbPath = filepath.Join(home, ".beads", "default.db")
			}
		}

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

		// Check for version mismatch (warn if binary is older than DB)
		checkVersionMismatch()

		// Auto-import if JSONL is newer than DB (e.g., after git pull)
		// Skip for import command itself to avoid recursion
		if cmd.Name() != "import" && autoImportEnabled {
			autoImportIfNewer()
		}
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Flush any pending changes before closing
		flushMutex.Lock()
		needsFlush := isDirty && autoFlushEnabled
		if needsFlush {
			// Cancel timer and flush immediately
			if flushTimer != nil {
				flushTimer.Stop()
				flushTimer = nil
			}
			// Don't clear isDirty here - let flushToJSONL do it
		}
		flushMutex.Unlock()

		if needsFlush {
			// Call the shared flush function (no code duplication)
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

// parseJSONLIssues parses issues from JSONL data
func parseJSONLIssues(jsonlData []byte) ([]*types.Issue, error) {
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
			snippet := line
			if len(snippet) > 80 {
				snippet = snippet[:80] + "..."
			}
			return nil, fmt.Errorf("parse error at line %d: %w\nSnippet: %s", lineNo, err, snippet)
		}

		allIssues = append(allIssues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	return allIssues, nil
}

// handleCollisions detects and reports collisions, returning safe issues to import
func handleCollisions(ctx context.Context, sqliteStore *sqlite.SQLiteStorage, allIssues []*types.Issue, jsonlPath string) ([]*types.Issue, error) {
	collisionResult, err := sqlite.DetectCollisions(ctx, sqliteStore, allIssues)
	if err != nil {
		return nil, fmt.Errorf("collision detection error: %w", err)
	}

	if len(collisionResult.Collisions) == 0 {
		return allIssues, nil
	}

	// Track colliding IDs
	collidingIDs := make(map[string]bool)
	for _, collision := range collisionResult.Collisions {
		collidingIDs[collision.ID] = true
	}

	// Concise warning: show first 10 collisions
	maxShow := 10
	if len(collisionResult.Collisions) < maxShow {
		maxShow = len(collisionResult.Collisions)
	}

	fmt.Fprintf(os.Stderr, "\nAuto-import: skipped %d issue(s) due to local edits.\n", len(collisionResult.Collisions))
	fmt.Fprintf(os.Stderr, "Conflicting issues (showing first %d): ", maxShow)
	for i := 0; i < maxShow; i++ {
		if i > 0 {
			fmt.Fprintf(os.Stderr, ", ")
		}
		fmt.Fprintf(os.Stderr, "%s", collisionResult.Collisions[i].ID)
	}
	if len(collisionResult.Collisions) > maxShow {
		fmt.Fprintf(os.Stderr, " ... and %d more", len(collisionResult.Collisions)-maxShow)
	}
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "To merge these changes, run: bd import -i %s --resolve-collisions\n\n", jsonlPath)

	// Full details under BD_DEBUG
	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: Full collision details:\n")
		for _, collision := range collisionResult.Collisions {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", collision.ID, collision.IncomingIssue.Title)
			fmt.Fprintf(os.Stderr, "    Conflicting fields: %v\n", collision.ConflictingFields)
		}
	}

	// Remove colliding issues from the import list
	safeIssues := make([]*types.Issue, 0)
	for _, issue := range allIssues {
		if !collidingIDs[issue.ID] {
			safeIssues = append(safeIssues, issue)
		}
	}

	return safeIssues, nil
}

// importIssueData imports issues and their dependencies/labels
func importIssueData(ctx context.Context, issues []*types.Issue) {
	// Import issues
	for _, issue := range issues {
		existing, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			continue
		}

		if existing != nil {
			updateExistingIssue(ctx, issue)
		} else {
			createNewIssue(ctx, issue)
		}
	}

	// Import dependencies and labels
	for _, issue := range issues {
		importDependencies(ctx, issue)
		importLabels(ctx, issue)
	}
}

// updateExistingIssue updates an existing issue with imported data
func updateExistingIssue(ctx context.Context, issue *types.Issue) {
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
		if issue.ClosedAt != nil {
			updates["closed_at"] = *issue.ClosedAt
		} else if !issue.UpdatedAt.IsZero() {
			updates["closed_at"] = issue.UpdatedAt
		} else {
			updates["closed_at"] = time.Now().UTC()
		}
	} else {
		updates["closed_at"] = nil
	}

	if err := store.UpdateIssue(ctx, issue.ID, updates, "auto-import"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-import failed to update %s: %v\n", issue.ID, err)
	}
}

// createNewIssue creates a new issue from imported data
func createNewIssue(ctx context.Context, issue *types.Issue) {
	// Enforce invariant before creation
	if issue.Status == "closed" {
		if issue.ClosedAt == nil {
			now := time.Now().UTC()
			issue.ClosedAt = &now
		}
	} else {
		issue.ClosedAt = nil
	}
	if err := store.CreateIssue(ctx, issue, "auto-import"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-import failed to create %s: %v\n", issue.ID, err)
	}
}

// importDependencies imports dependencies for an issue
func importDependencies(ctx context.Context, issue *types.Issue) {
	if len(issue.Dependencies) == 0 {
		return
	}

	existingDeps, err := store.GetDependencyRecords(ctx, issue.ID)
	if err != nil {
		return
	}

	for _, dep := range issue.Dependencies {
		exists := false
		for _, existing := range existingDeps {
			if existing.DependsOnID == dep.DependsOnID && existing.Type == dep.Type {
				exists = true
				break
			}
		}

		if !exists {
			if err := store.AddDependency(ctx, dep, "auto-import"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: auto-import failed to add dependency %s -> %s: %v\n", issue.ID, dep.DependsOnID, err)
			}
		}
	}
}

// importLabels imports labels for an issue
func importLabels(ctx context.Context, issue *types.Issue) {
	if issue.Labels == nil {
		return
	}

	existingLabels, err := store.GetLabels(ctx, issue.ID)
	if err != nil {
		return
	}

	// Convert to maps for comparison
	existingLabelMap := make(map[string]bool)
	for _, label := range existingLabels {
		existingLabelMap[label] = true
	}
	importedLabelMap := make(map[string]bool)
	for _, label := range issue.Labels {
		importedLabelMap[label] = true
	}

	// Add missing labels
	for _, label := range issue.Labels {
		if !existingLabelMap[label] {
			if err := store.AddLabel(ctx, issue.ID, label, "auto-import"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: auto-import failed to add label %s to %s: %v\n", label, issue.ID, err)
			}
		}
	}

	// Remove labels not in imported data
	for _, label := range existingLabels {
		if !importedLabelMap[label] {
			if err := store.RemoveLabel(ctx, issue.ID, label, "auto-import"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: auto-import failed to remove label %s from %s: %v\n", label, issue.ID, err)
			}
		}
	}
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
		// Metadata not supported or error reading - this shouldn't happen
		// since we added metadata table, but be defensive
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: auto-import skipped, metadata error: %v\n", err)
		}
		return
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

	// Parse issues from JSONL
	allIssues, err := parseJSONLIssues(jsonlData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import skipped: %v\n", err)
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

	safeIssues, err := handleCollisions(ctx, sqliteStore, allIssues, jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import skipped: %v\n", err)
		return
	}

	// Import non-colliding issues (exact matches + new issues)
	importIssueData(ctx, safeIssues)

	// Store new hash after successful import
	if err := store.SetMetadata(ctx, "last_import_hash", currentHash); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to store import hash: %v\n", err)
		fmt.Fprintf(os.Stderr, "Auto-import may re-run unnecessarily. Run 'bd export' to fix.\n")
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
		// Simple string comparison is sufficient for detecting version mismatch
		// We're not trying to parse semantic versions, just detect "different"
		yellow := color.New(color.FgYellow, color.Bold).SprintFunc()
		fmt.Fprintf(os.Stderr, "\n%s\n", yellow("⚠️  WARNING: Version mismatch detected!"))
		fmt.Fprintf(os.Stderr, "%s\n", yellow(fmt.Sprintf("⚠️  Your bd binary (v%s) differs from the database version (v%s)", Version, dbVersion)))

		// Determine if binary is likely older (heuristic: lower version number)
		if Version < dbVersion {
			fmt.Fprintf(os.Stderr, "%s\n", yellow("⚠️  Your binary appears to be OUTDATED."))
			fmt.Fprintf(os.Stderr, "%s\n\n", yellow("⚠️  Some features may not work correctly. Rebuild: go build -o bd ./cmd/bd"))
		} else {
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

// recordFlushFailure records a flush failure and shows appropriate warnings
func recordFlushFailure(err error) {
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

// recordFlushSuccess records a successful flush
func recordFlushSuccess() {
	flushMutex.Lock()
	flushFailureCount = 0
	lastFlushError = nil
	flushMutex.Unlock()
}

// readExistingJSONL reads existing JSONL file into a map
func readExistingJSONL(jsonlPath string) map[string]*types.Issue {
	issueMap := make(map[string]*types.Issue)

	existingFile, err := os.Open(jsonlPath)
	if err != nil {
		return issueMap // Return empty map if file doesn't exist
	}
	defer existingFile.Close()

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

	return issueMap
}

// fetchDirtyIssuesFromDB fetches dirty issues from database and updates the issue map
func fetchDirtyIssuesFromDB(ctx context.Context, dirtyIDs []string, issueMap map[string]*types.Issue) error {
	for _, issueID := range dirtyIDs {
		issue, err := store.GetIssue(ctx, issueID)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", issueID, err)
		}

		if issue == nil {
			// Issue was deleted, remove from map
			delete(issueMap, issueID)
			continue
		}

		// Get dependencies for this issue
		deps, err := store.GetDependencyRecords(ctx, issueID)
		if err != nil {
			return fmt.Errorf("failed to get dependencies for %s: %w", issueID, err)
		}
		issue.Dependencies = deps

		// Get labels for this issue
		labels, err := store.GetLabels(ctx, issueID)
		if err != nil {
			return fmt.Errorf("failed to get labels for %s: %w", issueID, err)
		}
		issue.Labels = labels

		// Update map
		issueMap[issueID] = issue
	}

	return nil
}

// writeIssuesToJSONL writes issues to JSONL file atomically
func writeIssuesToJSONL(jsonlPath string, issueMap map[string]*types.Issue) error {
	// Convert map to sorted slice
	issues := make([]*types.Issue, 0, len(issueMap))
	for _, issue := range issueMap {
		issues = append(issues, issue)
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Write to temp file first, then rename (atomic)
	tempPath := jsonlPath + ".tmp"
	f, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			f.Close()
			os.Remove(tempPath)
			return fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename file: %w", err)
	}

	return nil
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
	flushMutex.Unlock()

	jsonlPath := findJSONLPath()

	// Double-check store is still active before accessing
	storeMutex.Lock()
	if !storeActive {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	ctx := context.Background()

	// Get dirty issue IDs (bd-39: incremental export optimization)
	dirtyIDs, err := store.GetDirtyIssues(ctx)
	if err != nil {
		recordFlushFailure(fmt.Errorf("failed to get dirty issues: %w", err))
		return
	}

	// No dirty issues? Nothing to do!
	if len(dirtyIDs) == 0 {
		recordFlushSuccess()
		return
	}

	// Read existing JSONL into a map
	issueMap := readExistingJSONL(jsonlPath)

	// Fetch only dirty issues from DB
	if err := fetchDirtyIssuesFromDB(ctx, dirtyIDs, issueMap); err != nil {
		recordFlushFailure(err)
		return
	}

	// Write to JSONL file atomically
	if err := writeIssuesToJSONL(jsonlPath, issueMap); err != nil {
		recordFlushFailure(err)
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
		_ = store.SetMetadata(ctx, "last_import_hash", exportedHash)
	}

	// Success!
	recordFlushSuccess()
}

var (
	noAutoFlush  bool
	noAutoImport bool
)

func init() {
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "Database path (default: auto-discover .beads/*.db or ~/.beads/default.db)")
	rootCmd.PersistentFlags().StringVar(&actor, "actor", "", "Actor name for audit trail (default: $BD_ACTOR or $USER)")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().BoolVar(&noAutoFlush, "no-auto-flush", false, "Disable automatic JSONL sync after CRUD operations")
	rootCmd.PersistentFlags().BoolVar(&noAutoImport, "no-auto-import", false, "Disable automatic JSONL import when newer than DB")
}

// addLabelsToIssue adds labels to an issue
func addLabelsToIssue(ctx context.Context, issue *types.Issue, labels []string) {
	for _, label := range labels {
		if err := store.AddLabel(ctx, issue.ID, label, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add label %s to %s: %v\n", label, issue.ID, err)
		}
	}
}

// parseDependencySpec parses a dependency specification string
func parseDependencySpec(depSpec string) (types.DependencyType, string, error) {
	depSpec = strings.TrimSpace(depSpec)
	if depSpec == "" {
		return "", "", fmt.Errorf("empty dependency spec")
	}

	var depType types.DependencyType
	var dependsOnID string

	// Parse format: "type:id" or just "id" (defaults to "blocks")
	if strings.Contains(depSpec, ":") {
		parts := strings.SplitN(depSpec, ":", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid dependency format '%s'", depSpec)
		}
		depType = types.DependencyType(strings.TrimSpace(parts[0]))
		dependsOnID = strings.TrimSpace(parts[1])
	} else {
		depType = types.DepBlocks
		dependsOnID = depSpec
	}

	if !depType.IsValid() {
		return "", "", fmt.Errorf("invalid dependency type '%s'", depType)
	}

	return depType, dependsOnID, nil
}

// addDependenciesToIssue adds dependencies to an issue
func addDependenciesToIssue(ctx context.Context, issue *types.Issue, depSpecs []string) {
	for _, depSpec := range depSpecs {
		depType, dependsOnID, err := parseDependencySpec(depSpec)
		if err != nil {
			if !strings.Contains(err.Error(), "empty dependency spec") {
				fmt.Fprintf(os.Stderr, "Warning: %v for %s\n", err, issue.ID)
			}
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
}

// createIssueFromTemplate creates a single issue from a template
func createIssueFromTemplate(ctx context.Context, template *IssueTemplate) (*types.Issue, error) {
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
		return nil, err
	}

	addLabelsToIssue(ctx, issue, template.Labels)
	addDependenciesToIssue(ctx, issue, template.Dependencies)

	return issue, nil
}

// reportMarkdownCreationResults reports the results of creating issues from markdown
func reportMarkdownCreationResults(filepath string, createdIssues []*types.Issue, failedIssues []string) {
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

// createIssuesFromMarkdown parses a markdown file and creates multiple issues
func createIssuesFromMarkdown(filepath string) {
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
		issue, err := createIssueFromTemplate(ctx, template)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating issue '%s': %v\n", template.Title, err)
			failedIssues = append(failedIssues, template.Title)
			continue
		}
		createdIssues = append(createdIssues, issue)
	}

	// Schedule auto-flush
	if len(createdIssues) > 0 {
		markDirtyAndScheduleFlush()
	}

	reportMarkdownCreationResults(filepath, createdIssues, failedIssues)
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
			createIssuesFromMarkdown(file)
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
		}

		var externalRefPtr *string
		if externalRef != "" {
			externalRefPtr = &externalRef
		}

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

		// Add labels and dependencies using shared helper functions
		addLabelsToIssue(ctx, issue, labels)
		addDependenciesToIssue(ctx, issue, deps)

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
	rootCmd.AddCommand(createCmd)
}

var showCmd = &cobra.Command{
	Use:   "show [id]",
	Short: "Show issue details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
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
			var err error
			details.Labels, err = store.GetLabels(ctx, issue.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to fetch labels: %v\n", err)
			}
			details.Dependencies, err = store.GetDependencies(ctx, issue.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to fetch dependencies: %v\n", err)
			}
			details.Dependents, err = store.GetDependents(ctx, issue.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to fetch dependents: %v\n", err)
			}
			outputJSON(details)
			return
		}

		cyan := color.New(color.FgCyan).SprintFunc()
		fmt.Printf("\n%s: %s\n", cyan(issue.ID), issue.Title)
		fmt.Printf("Status: %s\n", issue.Status)
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

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List issues",
	Run: func(cmd *cobra.Command, args []string) {
		status, _ := cmd.Flags().GetString("status")
		assignee, _ := cmd.Flags().GetString("assignee")
		issueType, _ := cmd.Flags().GetString("type")
		limit, _ := cmd.Flags().GetInt("limit")
		labels, _ := cmd.Flags().GetStringSlice("label")

		filter := types.IssueFilter{
			Limit: limit,
		}
		if status != "" {
			s := types.Status(status)
			filter.Status = &s
		}
		// Use Changed() to properly handle P0 (priority=0)
		if cmd.Flags().Changed("priority") {
			priority, _ := cmd.Flags().GetInt("priority")
			filter.Priority = &priority
		}
		if assignee != "" {
			filter.Assignee = &assignee
		}
		if issueType != "" {
			t := types.IssueType(issueType)
			filter.IssueType = &t
		}
		if len(labels) > 0 {
			filter.Labels = labels
		}

		ctx := context.Background()
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(issues)
			return
		}

		fmt.Printf("\nFound %d issues:\n\n", len(issues))
		for _, issue := range issues {
			fmt.Printf("%s [P%d] [%s] %s\n", issue.ID, issue.Priority, issue.IssueType, issue.Status)
			fmt.Printf("  %s\n", issue.Title)
			if issue.Assignee != "" {
				fmt.Printf("  Assignee: %s\n", issue.Assignee)
			}
			fmt.Println()
		}
	},
}

func init() {
	listCmd.Flags().StringP("status", "s", "", "Filter by status")
	listCmd.Flags().IntP("priority", "p", 0, "Filter by priority")
	listCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	listCmd.Flags().StringP("type", "t", "", "Filter by type")
	listCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (comma-separated)")
	listCmd.Flags().IntP("limit", "n", 0, "Limit results")
	rootCmd.AddCommand(listCmd)
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
