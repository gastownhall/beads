//go:build cgo
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// migrationData holds all data extracted from the source database
type migrationData struct {
	issues     []*types.Issue
	labelsMap  map[string][]string
	depsMap    map[string][]*types.Dependency
	eventsMap  map[string][]*types.Event
	config     map[string]string
	prefix     string
	issueCount int
}

// handleToDoltMigration migrates from SQLite to Dolt backend.
// This implements Part 7 of DOLT-STORAGE-DESIGN.md:
// 1. Creates Dolt database in `.beads/dolt/`
// 2. Imports all issues, labels, dependencies, events
// 3. Copies all config values
// 4. Updates `metadata.json` to use Dolt
// 5. Keeps JSONL export enabled by default
func handleToDoltMigration(dryRun bool, autoYes bool) {
	ctx := context.Background()

	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		exitWithError("no_beads_directory", "No .beads directory found. Run 'bd init' first.",
			"run 'bd init' to initialize bd")
	}

	// Check if daemon is running - migration with a running daemon risks race conditions
	pidFile := filepath.Join(beadsDir, "daemon.pid")
	if isRunning, pid := isDaemonRunning(pidFile); isRunning {
		exitWithError("daemon_running",
			fmt.Sprintf("bd daemon is running (PID %d). Stop it before migrating to avoid race conditions.", pid),
			"run 'bd daemon stop' first, then retry migration")
	}

	// Load config
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		exitWithError("config_load_failed", err.Error(), "")
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	// Check if already using Dolt
	if cfg.GetBackend() == configfile.BackendDolt {
		printNoop("Already using Dolt backend")
		return
	}

	// Find SQLite database
	sqlitePath := cfg.DatabasePath(beadsDir)
	if _, err := os.Stat(sqlitePath); os.IsNotExist(err) {
		exitWithError("no_sqlite_database", "No SQLite database found to migrate",
			fmt.Sprintf("run 'bd init' first (expected: %s)", sqlitePath))
	}

	// Dolt path
	doltPath := filepath.Join(beadsDir, "dolt")

	// Check if Dolt directory already exists
	if _, err := os.Stat(doltPath); err == nil {
		exitWithError("dolt_exists", fmt.Sprintf("Dolt directory already exists at %s", doltPath),
			"remove it first if you want to re-migrate")
	}

	// Extract all data from SQLite
	data, err := extractFromSQLite(ctx, sqlitePath)
	if err != nil {
		exitWithError("extraction_failed", err.Error(), "")
	}

	// Show migration plan
	printMigrationPlan("SQLite to Dolt", sqlitePath, doltPath, data)

	// Dry run mode
	if dryRun {
		printDryRun(sqlitePath, doltPath, data, true)
		return
	}

	// Prompt for confirmation
	if !autoYes && !jsonOutput {
		if !confirmBackendMigration("SQLite", "Dolt", true) {
			fmt.Println("Migration canceled")
			return
		}
	}

	// Create backup
	backupPath := strings.TrimSuffix(sqlitePath, ".db") + ".backup-pre-dolt-" + time.Now().Format("20060102-150405") + ".db"
	if err := copyFile(sqlitePath, backupPath); err != nil {
		exitWithError("backup_failed", err.Error(), "")
	}
	printSuccess(fmt.Sprintf("Created backup: %s", filepath.Base(backupPath)))

	// Create Dolt database
	printProgress("Creating Dolt database...")

	// Use prefix-based database name to avoid cross-rig contamination (bd-u8rda).
	// E.g., prefix "gt" → database "beads_gt", prefix "bd" → database "beads_bd".
	dbName := "beads"
	if data.prefix != "" {
		dbName = "beads_" + data.prefix
	}
	doltStore, err := dolt.New(ctx, &dolt.Config{Path: doltPath, Database: dbName})
	if err != nil {
		exitWithError("dolt_create_failed", err.Error(), "")
	}

	// Import data with cleanup on failure
	imported, skipped, importErr := importToDolt(ctx, doltStore, data)
	if importErr != nil {
		_ = doltStore.Close()
		// Cleanup partial Dolt directory
		_ = os.RemoveAll(doltPath)
		exitWithError("import_failed", importErr.Error(), "partial Dolt directory has been cleaned up")
	}

	// Set sync.mode to dolt-native in the DB so ShouldExportJSONL skips
	// the expensive JSONL export. Without this, the migrated DB has no
	// sync.mode set, defaulting to git-portable (10-25s export tax per write).
	if err := doltStore.SetConfig(ctx, SyncModeConfigKey, SyncModeDoltNative); err != nil {
		printWarning(fmt.Sprintf("failed to set sync.mode in DB: %v", err))
	} else {
		printSuccess("Set sync.mode = dolt-native in database")
	}

	// Commit the migration
	commitMsg := fmt.Sprintf("Migrate from SQLite: %d issues imported", imported)
	if err := doltStore.Commit(ctx, commitMsg); err != nil {
		printWarning(fmt.Sprintf("failed to create Dolt commit: %v", err))
	}

	_ = doltStore.Close()

	printSuccess(fmt.Sprintf("Imported %d issues (%d skipped)", imported, skipped))

	// Update metadata.json
	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltDatabase = dbName
	cfg.DoltServerPort = configfile.DefaultDoltServerPort
	if err := cfg.Save(beadsDir); err != nil {
		exitWithError("config_save_failed", err.Error(),
			"data was imported but metadata.json was not updated - manually set backend to 'dolt'")
	}

	printSuccess("Updated metadata.json to use Dolt backend")

	// Check if git hooks need updating for Dolt compatibility
	if hooksNeedDoltUpdate(beadsDir) {
		printWarning("Git hooks need updating for Dolt backend")
		if !jsonOutput {
			fmt.Println("  The pre-commit and post-merge hooks use JSONL sync which doesn't apply to Dolt.")
			fmt.Println("  Run 'bd hooks install --force' to update them.")
		}
	}

	// Final status
	printFinalStatus("dolt", imported, skipped, backupPath, doltPath, sqlitePath, true)
}

// hooksNeedDoltUpdate checks if installed git hooks lack the Dolt backend skip logic.
func hooksNeedDoltUpdate(beadsDir string) bool {
	// Find git hooks directory
	repoRoot := filepath.Dir(beadsDir)
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")

	// Check post-merge hook (most likely to cause issues)
	postMergePath := filepath.Join(hooksDir, "post-merge")
	// #nosec G304 -- postMergePath is derived from the local repo's .git/hooks directory.
	content, err := os.ReadFile(postMergePath)
	if err != nil {
		return false // No hook installed
	}

	contentStr := string(content)

	// Shim hooks (bd-shim) delegate to 'bd hook' which handles Dolt correctly
	if strings.Contains(contentStr, "bd-shim") {
		return false // Shim hooks are fine
	}

	// Check if it's a bd inline hook
	if !strings.Contains(contentStr, "bd") {
		return false // Not a bd hook
	}

	// Check if inline hook has the Dolt skip logic
	if strings.Contains(contentStr, `"backend"`) && strings.Contains(contentStr, `"dolt"`) {
		return false // Already has Dolt check
	}

	return true // bd inline hook without Dolt check
}

// handleToSQLiteMigration migrates from Dolt to SQLite backend (escape hatch).
func handleToSQLiteMigration(dryRun bool, autoYes bool) {
	ctx := context.Background()

	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		exitWithError("no_beads_directory", "No .beads directory found. Run 'bd init' first.",
			"run 'bd init' to initialize bd")
	}

	// Load config
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		exitWithError("config_load_failed", err.Error(), "")
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	// Check if already using SQLite
	if cfg.GetBackend() == configfile.BackendSQLite {
		printNoop("Already using SQLite backend")
		return
	}

	// Find Dolt database
	doltPath := cfg.DatabasePath(beadsDir)
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		exitWithError("no_dolt_database", "No Dolt database found to migrate",
			fmt.Sprintf("expected at: %s", doltPath))
	}

	// SQLite path
	sqlitePath := filepath.Join(beadsDir, "beads.db")

	// Check if SQLite database already exists
	if _, err := os.Stat(sqlitePath); err == nil {
		exitWithError("sqlite_exists", fmt.Sprintf("SQLite database already exists at %s", sqlitePath),
			"remove it first if you want to re-migrate")
	}

	// Extract all data from Dolt
	data, err := extractFromDolt(ctx, doltPath)
	if err != nil {
		exitWithError("extraction_failed", err.Error(), "")
	}

	// Show migration plan
	printMigrationPlan("Dolt to SQLite", doltPath, sqlitePath, data)

	// Dry run mode
	if dryRun {
		printDryRun(doltPath, sqlitePath, data, false)
		return
	}

	// Prompt for confirmation
	if !autoYes && !jsonOutput {
		if !confirmBackendMigration("Dolt", "SQLite", false) {
			fmt.Println("Migration canceled")
			return
		}
	}

	// Create SQLite database
	printProgress("Creating SQLite database...")

	sqliteStore, err := sqlite.New(ctx, sqlitePath)
	if err != nil {
		exitWithError("sqlite_create_failed", err.Error(), "")
	}

	// Import data with cleanup on failure
	imported, skipped, importErr := importToSQLite(ctx, sqliteStore, data)
	if importErr != nil {
		_ = sqliteStore.Close()
		// Cleanup partial SQLite database
		_ = os.Remove(sqlitePath)
		_ = os.Remove(sqlitePath + "-wal")
		_ = os.Remove(sqlitePath + "-shm")
		exitWithError("import_failed", importErr.Error(), "partial SQLite database has been cleaned up")
	}

	_ = sqliteStore.Close()

	printSuccess(fmt.Sprintf("Imported %d issues (%d skipped)", imported, skipped))

	// Update metadata.json
	cfg.Backend = configfile.BackendSQLite
	cfg.Database = "beads.db"
	if err := cfg.Save(beadsDir); err != nil {
		exitWithError("config_save_failed", err.Error(),
			"data was imported but metadata.json was not updated - manually set backend to 'sqlite'")
	}

	printSuccess("Updated metadata.json to use SQLite backend")

	// Final status
	printFinalStatus("sqlite", imported, skipped, "", sqlitePath, doltPath, false)
}

// extractFromSQLite extracts all data from a SQLite database
func extractFromSQLite(ctx context.Context, dbPath string) (*migrationData, error) {
	store, err := sqlite.NewReadOnly(ctx, dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}
	defer func() { _ = store.Close() }()

	return extractFromStore(ctx, store)
}

// extractFromDolt extracts all data from a Dolt database
func extractFromDolt(ctx context.Context, dbPath string) (*migrationData, error) {
	store, err := dolt.New(ctx, &dolt.Config{Path: dbPath, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("failed to open Dolt database: %w", err)
	}
	defer func() { _ = store.Close() }()

	return extractFromStore(ctx, store)
}

// storageReader is a minimal interface for reading from storage
type storageReader interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
	GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
	GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error)
	GetAllConfig(ctx context.Context) (map[string]string, error)
	GetConfig(ctx context.Context, key string) (string, error)
}

// extractFromStore extracts all data from a storage backend
func extractFromStore(ctx context.Context, store storageReader) (*migrationData, error) {
	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch issues: %w", err)
	}

	// Build issue ID list
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}

	// Get labels
	labelsMap, err := store.GetLabelsForIssues(ctx, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch labels: %w", err)
	}

	// Get dependencies
	depsMap, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch dependencies: %w", err)
	}

	// Get events for all issues (includes comments)
	eventsMap := make(map[string][]*types.Event)
	for _, issueID := range issueIDs {
		events, err := store.GetEvents(ctx, issueID, 0) // 0 = no limit
		if err != nil {
			return nil, fmt.Errorf("failed to fetch events for %s: %w", issueID, err)
		}
		if len(events) > 0 {
			eventsMap[issueID] = events
		}
	}

	// Get all config
	config, err := store.GetAllConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch config: %w", err)
	}

	// Get prefix
	prefix, _ := store.GetConfig(ctx, "issue_prefix")

	// Assign labels and dependencies to issues
	for _, issue := range issues {
		if labels, ok := labelsMap[issue.ID]; ok {
			issue.Labels = labels
		}
		if deps, ok := depsMap[issue.ID]; ok {
			issue.Dependencies = deps
		}
	}

	return &migrationData{
		issues:     issues,
		labelsMap:  labelsMap,
		depsMap:    depsMap,
		eventsMap:  eventsMap,
		config:     config,
		prefix:     prefix,
		issueCount: len(issues),
	}, nil
}

// importToDolt imports all data to Dolt, returning (imported, skipped, error)
func importToDolt(ctx context.Context, store *dolt.DoltStore, data *migrationData) (int, int, error) {
	// Set all config values first
	for key, value := range data.config {
		if err := store.SetConfig(ctx, key, value); err != nil {
			return 0, 0, fmt.Errorf("failed to set config %s: %w", key, err)
		}
	}

	tx, err := store.UnderlyingDB().BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	imported := 0
	skipped := 0
	seenIDs := make(map[string]bool)
	total := len(data.issues)

	for i, issue := range data.issues {
		// Progress indicator
		if !jsonOutput && total > 100 && (i+1)%100 == 0 {
			fmt.Printf("  Importing issues: %d/%d\r", i+1, total)
		}

		if seenIDs[issue.ID] {
			skipped++
			continue
		}
		seenIDs[issue.ID] = true

		if issue.ContentHash == "" {
			issue.ContentHash = issue.ComputeContentHash()
		}

		// Insert issue
		_, err := tx.ExecContext(ctx, `
			INSERT INTO issues (
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, estimated_minutes,
				created_at, created_by, owner, updated_at, closed_at, external_ref,
				compaction_level, compacted_at, compacted_at_commit, original_size,
				deleted_at, deleted_by, delete_reason, original_type,
				sender, ephemeral, pinned, is_template, crystallizes,
				mol_type, work_type, quality_score, source_system, source_repo, close_reason,
				event_kind, actor, target, payload,
				await_type, await_id, timeout_ns, waiters,
				hook_bead, role_bead, agent_state, last_activity, role_type, rig,
				due_at, defer_until
			) VALUES (
				?, ?, ?, ?, ?, ?, ?,
				?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				?, ?
			)
		`,
			issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
			issue.Status, issue.Priority, issue.IssueType, nullableString(issue.Assignee), nullableIntPtr(issue.EstimatedMinutes),
			issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.ClosedAt, nullableStringPtr(issue.ExternalRef),
			issue.CompactionLevel, issue.CompactedAt, nullableStringPtr(issue.CompactedAtCommit), nullableInt(issue.OriginalSize),
			issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
			issue.Sender, issue.Ephemeral, issue.Pinned, issue.IsTemplate, issue.Crystallizes,
			issue.MolType, issue.WorkType, nullableFloat32Ptr(issue.QualityScore), issue.SourceSystem, issue.SourceRepo, issue.CloseReason,
			issue.EventKind, issue.Actor, issue.Target, issue.Payload,
			issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), formatJSONArray(issue.Waiters),
			issue.HookBead, issue.RoleBead, issue.AgentState, issue.LastActivity, issue.RoleType, issue.Rig,
			issue.DueAt, issue.DeferUntil,
		)
		if err != nil {
			if strings.Contains(err.Error(), "Duplicate entry") ||
				strings.Contains(err.Error(), "UNIQUE constraint") {
				skipped++
				continue
			}
			return imported, skipped, fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
		}

		// Insert labels
		for _, label := range issue.Labels {
			if _, err := tx.ExecContext(ctx, `INSERT INTO labels (issue_id, label) VALUES (?, ?)`, issue.ID, label); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to insert label %q for issue %s: %v\n", label, issue.ID, err)
			}
		}

		imported++
	}

	if !jsonOutput && total > 100 {
		fmt.Printf("  Importing issues: %d/%d\n", total, total)
	}

	// Import dependencies
	printProgress("Importing dependencies...")
	for _, issue := range data.issues {
		for _, dep := range issue.Dependencies {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM issues WHERE id = ?", dep.DependsOnID).Scan(&exists); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: skipping dependency %s -> %s: target issue not found\n", dep.IssueID, dep.DependsOnID)
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
				VALUES (?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE type = type
			`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedBy, dep.CreatedAt); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to insert dependency %s -> %s: %v\n", dep.IssueID, dep.DependsOnID, err)
			}
		}
	}

	// Import events (includes comments)
	printProgress("Importing events...")
	eventCount := 0
	for issueID, events := range data.eventsMap {
		for _, event := range events {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, issueID, event.EventType, event.Actor,
				nullableStringPtr(event.OldValue), nullableStringPtr(event.NewValue),
				nullableStringPtr(event.Comment), event.CreatedAt)
			if err == nil {
				eventCount++
			}
		}
	}
	if !jsonOutput {
		fmt.Printf("  Imported %d events\n", eventCount)
	}

	if err := tx.Commit(); err != nil {
		return imported, skipped, fmt.Errorf("failed to commit: %w", err)
	}

	return imported, skipped, nil
}

// importToSQLite imports all data to SQLite, returning (imported, skipped, error)
func importToSQLite(ctx context.Context, store *sqlite.SQLiteStorage, data *migrationData) (int, int, error) {
	// Set all config values first
	for key, value := range data.config {
		if err := store.SetConfig(ctx, key, value); err != nil {
			return 0, 0, fmt.Errorf("failed to set config %s: %w", key, err)
		}
	}

	imported := 0
	skipped := 0

	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		seenIDs := make(map[string]bool)
		total := len(data.issues)

		for i, issue := range data.issues {
			// Progress indicator
			if !jsonOutput && total > 100 && (i+1)%100 == 0 {
				fmt.Printf("  Importing issues: %d/%d\r", i+1, total)
			}

			if seenIDs[issue.ID] {
				skipped++
				continue
			}
			seenIDs[issue.ID] = true

			savedLabels := issue.Labels
			savedDeps := issue.Dependencies
			issue.Labels = nil
			issue.Dependencies = nil

			if err := tx.CreateIssue(ctx, issue, "migration"); err != nil {
				if strings.Contains(err.Error(), "UNIQUE constraint") ||
					strings.Contains(err.Error(), "already exists") {
					skipped++
					issue.Labels = savedLabels
					issue.Dependencies = savedDeps
					continue
				}
				return fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
			}

			for _, label := range savedLabels {
				if err := tx.AddLabel(ctx, issue.ID, label, "migration"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add label %q for issue %s: %v\n", label, issue.ID, err)
				}
			}

			issue.Labels = savedLabels
			issue.Dependencies = savedDeps
			imported++
		}

		if !jsonOutput && total > 100 {
			fmt.Printf("  Importing issues: %d/%d\n", total, total)
		}

		// Import dependencies
		printProgress("Importing dependencies...")
		for _, issue := range data.issues {
			for _, dep := range issue.Dependencies {
				if err := tx.AddDependency(ctx, dep, "migration"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add dependency %s -> %s: %v\n", dep.IssueID, dep.DependsOnID, err)
				}
			}
		}

		return nil
	})

	if err != nil {
		return imported, skipped, err
	}

	// Import events outside transaction (SQLite events table may have different structure)
	printProgress("Importing events...")
	eventCount := 0
	db := store.UnderlyingDB()
	for issueID, events := range data.eventsMap {
		for _, event := range events {
			_, err := db.ExecContext(ctx, `
				INSERT INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, issueID, event.EventType, event.Actor,
				nullableStringPtr(event.OldValue), nullableStringPtr(event.NewValue),
				nullableStringPtr(event.Comment), event.CreatedAt)
			if err == nil {
				eventCount++
			}
		}
	}
	if !jsonOutput {
		fmt.Printf("  Imported %d events\n", eventCount)
	}

	return imported, skipped, nil
}

// Helper functions for output

func exitWithError(code, message, hint string) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"error":   code,
			"message": message,
		})
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", message)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "Hint: %s\n", hint)
		}
	}
	os.Exit(1)
}

func printNoop(message string) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":  "noop",
			"message": message,
		})
	} else {
		fmt.Printf("%s\n", ui.RenderPass("✓ "+message))
		fmt.Println("No migration needed")
	}
}

func printSuccess(message string) {
	if !jsonOutput {
		fmt.Printf("%s\n", ui.RenderPass("✓ "+message))
	}
}

func printWarning(message string) {
	if !jsonOutput {
		fmt.Printf("%s\n", ui.RenderWarn("Warning: "+message))
	}
}

func printProgress(message string) {
	if !jsonOutput {
		fmt.Printf("%s\n", message)
	}
}

func printMigrationPlan(title, source, target string, data *migrationData) {
	if jsonOutput {
		return
	}
	fmt.Printf("%s Migration\n", title)
	fmt.Printf("%s\n\n", strings.Repeat("=", len(title)+10))
	fmt.Printf("Source: %s\n", source)
	fmt.Printf("Target: %s\n", target)
	fmt.Printf("Issues to migrate: %d\n", data.issueCount)

	eventCount := 0
	for _, events := range data.eventsMap {
		eventCount += len(events)
	}
	fmt.Printf("Events to migrate: %d\n", eventCount)
	fmt.Printf("Config keys: %d\n", len(data.config))

	if data.prefix != "" {
		fmt.Printf("Issue prefix: %s\n", data.prefix)
	}
	fmt.Println()
}

func printDryRun(source, target string, data *migrationData, withBackup bool) {
	eventCount := 0
	for _, events := range data.eventsMap {
		eventCount += len(events)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"dry_run":      true,
			"source":       source,
			"target":       target,
			"issue_count":  data.issueCount,
			"event_count":  eventCount,
			"config_keys":  len(data.config),
			"prefix":       data.prefix,
			"would_backup": withBackup,
		}
		outputJSON(result)
	} else {
		fmt.Println("Dry run mode - no changes will be made")
		fmt.Println("Would perform:")
		step := 1
		if withBackup {
			fmt.Printf("  %d. Create backup of source database\n", step)
			step++
		}
		fmt.Printf("  %d. Create target database at %s\n", step, target)
		step++
		fmt.Printf("  %d. Import %d issues with labels and dependencies\n", step, data.issueCount)
		step++
		fmt.Printf("  %d. Import %d events (history/comments)\n", step, eventCount)
		step++
		fmt.Printf("  %d. Copy %d config values\n", step, len(data.config))
		step++
		fmt.Printf("  %d. Update metadata.json\n", step)
	}
}

func confirmBackendMigration(from, to string, withBackup bool) bool {
	fmt.Printf("This will:\n")
	step := 1
	if withBackup {
		fmt.Printf("  %d. Create a backup of your %s database\n", step, from)
		step++
	}
	fmt.Printf("  %d. Create a %s database and import all data\n", step, to)
	step++
	fmt.Printf("  %d. Update metadata.json to use %s backend\n", step, to)
	step++
	fmt.Printf("  %d. Keep your %s database (can be deleted after verification)\n\n", step, from)
	fmt.Printf("Continue? [y/N] ")
	var response string
	_, _ = fmt.Scanln(&response)
	return strings.ToLower(response) == "y" || strings.ToLower(response) == "yes"
}

func printFinalStatus(backend string, imported, skipped int, backupPath, newPath, oldPath string, toDolt bool) {
	if jsonOutput {
		result := map[string]interface{}{
			"status":          "success",
			"backend":         backend,
			"issues_imported": imported,
			"issues_skipped":  skipped,
		}
		if backupPath != "" {
			result["backup_path"] = backupPath
		}
		if toDolt {
			result["dolt_path"] = newPath
		} else {
			result["sqlite_path"] = newPath
		}
		outputJSON(result)
	} else {
		fmt.Println()
		fmt.Printf("%s\n", ui.RenderPass("✓ Migration complete!"))
		fmt.Println()
		fmt.Printf("Your beads now use %s storage.\n", strings.ToUpper(backend))
		if backupPath != "" {
			fmt.Printf("Backup: %s\n", backupPath)
		}
		fmt.Println()
		fmt.Println("Next steps:")
		fmt.Println("  • Verify data: bd list")
		fmt.Println("  • After verification, you can delete the old database:")
		if toDolt {
			fmt.Printf("    rm %s\n", oldPath)
			fmt.Println()
			fmt.Println("To switch back to SQLite: bd migrate --to-sqlite")
		} else {
			fmt.Printf("    rm -rf %s\n", oldPath)
			fmt.Println()
			fmt.Println("To switch back to Dolt: bd migrate --to-dolt")
		}
	}
}

// Helper functions for nullable values

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullableStringPtr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

func nullableIntPtr(i *int) interface{} {
	if i == nil {
		return nil
	}
	return *i
}

func nullableInt(i int) interface{} {
	if i == 0 {
		return nil
	}
	return i
}

func nullableFloat32Ptr(f *float32) interface{} {
	if f == nil {
		return nil
	}
	return *f
}

// formatJSONArray formats a string slice as JSON (matches Dolt schema expectation)
func formatJSONArray(arr []string) string {
	if len(arr) == 0 {
		return ""
	}
	data, err := json.Marshal(arr)
	if err != nil {
		return ""
	}
	return string(data)
}
