//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// handleToDoltMigration migrates from SQLite to Dolt backend.
// 1. Finds SQLite .db files in .beads/
// 2. Creates Dolt database in `.beads/dolt/`
// 3. Imports all issues, labels, dependencies, events
// 4. Copies all config values
// 5. Updates `metadata.json` to use Dolt
func handleToDoltMigration(dryRun bool, autoYes bool) {
	ctx := context.Background()

	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		exitWithError("no_beads_directory", "No .beads directory found. Run 'bd init' first.",
			"run 'bd init' to initialize bd")
	}

	// Find SQLite database by scanning for .db files
	sqlitePath := findSQLiteDB(beadsDir)
	if sqlitePath == "" {
		exitWithError("no_sqlite_database", "No SQLite database found to migrate",
			"no .db files found in "+beadsDir)
	}

	// Dolt path
	doltPath := filepath.Join(beadsDir, "dolt")

	// Track whether the dolt directory pre-existed so cleanup doesn't
	// destroy data from a prior migration or server start.
	doltDirExisted := false
	if _, err := os.Stat(doltPath); err == nil {
		doltDirExisted = true
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

	// Respect existing config's database name to avoid creating phantom catalog
	// entries when a user has renamed their database (GH#2051).
	dbName := ""
	if existingCfg, _ := configfile.Load(beadsDir); existingCfg != nil && existingCfg.DoltDatabase != "" {
		dbName = existingCfg.DoltDatabase
	} else if data.prefix != "" {
		dbName = data.prefix
	} else {
		dbName = "beads"
	}
	doltStore, err := dolt.New(ctx, &dolt.Config{
		Path:            doltPath,
		Database:        dbName,
		CreateIfMissing: true, // migration creates a new Dolt database
		AutoStart:       !doltserver.IsDaemonManaged() && os.Getenv("BEADS_DOLT_AUTO_START") != "0",
	})
	if err != nil {
		exitWithError("dolt_create_failed", err.Error(), "")
	}

	// Import data with cleanup on failure
	imported, skipped, importErr := importToDolt(ctx, doltStore, data)
	if importErr != nil {
		_ = doltStore.Close()
		if !doltDirExisted {
			_ = os.RemoveAll(doltPath)
		}
		hint := "import failed"
		if !doltDirExisted {
			hint = "partial Dolt directory has been cleaned up"
		}
		exitWithError("import_failed", importErr.Error(), hint)
	}

	// Set sync.mode to dolt-native in the DB.
	if err := doltStore.SetConfig(ctx, "sync.mode", "dolt-native"); err != nil {
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

	// Load and update metadata.json
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		cfg = configfile.DefaultConfig()
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltDatabase = dbName
	// Don't set DoltServerPort — let doltserver.DefaultConfig derive it from
	// the project path at runtime. Writing an explicit port here would freeze
	// a value that conflicts with the hash-based port in doltserver.DerivePort.
	if err := cfg.Save(beadsDir); err != nil {
		exitWithError("config_save_failed", err.Error(),
			"data was imported but metadata.json was not updated - manually set backend to 'dolt'")
	}

	printSuccess("Updated metadata.json to use Dolt backend")

	// Write sync.mode to config.yaml so viper-based code reads the correct mode.
	// The DB config table was already updated above; this fixes the split-brain
	// where config.yaml still says "git-portable" (GH #1723, #1794).
	if err := config.SaveConfigValue("sync.mode", string(config.SyncModeDoltNative), beadsDir); err != nil {
		printWarning(fmt.Sprintf("failed to write sync.mode to config.yaml: %v (set manually: sync.mode: dolt-native)", err))
	} else {
		printSuccess("Set sync.mode = dolt-native in config.yaml")
	}

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
	repoRoot := filepath.Dir(beadsDir)
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")

	postMergePath := filepath.Join(hooksDir, "post-merge")
	// #nosec G304 -- postMergePath is derived from the local repo's .git/hooks directory.
	content, err := os.ReadFile(postMergePath)
	if err != nil {
		return false
	}

	contentStr := string(content)

	if strings.Contains(contentStr, "bd-shim") {
		return false
	}
	if !strings.Contains(contentStr, "bd") {
		return false
	}
	if strings.Contains(contentStr, `"backend"`) && strings.Contains(contentStr, `"dolt"`) {
		return false
	}
	return true
}

// extractFromSQLite extracts all data from a SQLite database using raw SQL.
// This is the CGO path — it reads SQLite directly via the ncruces/go-sqlite3 driver.
// For non-CGO builds, see migrate_shim.go which uses the sqlite3 CLI instead.
func extractFromSQLite(ctx context.Context, dbPath string) (*migrationData, error) {
	// Escape '#' in the path to prevent URI fragment truncation (GH#2115).
	escapedPath := strings.ReplaceAll(dbPath, "#", "%23")
	db, err := sql.Open("sqlite3", "file:"+escapedPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("failed to open SQLite database: %w", err)
	}
	defer db.Close()

	// Get prefix from config table
	prefix := ""
	_ = db.QueryRowContext(ctx, "SELECT value FROM config WHERE key = 'issue_prefix'").Scan(&prefix)

	// Get all config
	config := make(map[string]string)
	configRows, err := db.QueryContext(ctx, "SELECT key, value FROM config")
	if err == nil {
		defer configRows.Close()
		for configRows.Next() {
			var k, v string
			if err := configRows.Scan(&k, &v); err == nil {
				config[k] = v
			}
		}
	}

	issueCols, _ := sqliteTableColumns(ctx, db, "issues")
	depCols, _ := sqliteTableColumns(ctx, db, "dependencies")

	// All columns that may be missing in older beads database schemas.
	// Uses sqliteOptionalTextExpr which checks PRAGMA table_info before querying.
	// Fixes "no such column" errors for pre-v0.49 databases (GH#2016).
	opt := func(col, fallback string) string {
		return sqliteOptionalTextExpr(issueCols, col, fallback)
	}

	// Get all issues
	//nolint:gosec // SQL fragments come from fixed column names discovered via PRAGMA table_info.
	// Build query with ALL non-id columns optional to handle any schema version.
	// Core columns (id, title, status, priority, created_at, updated_at) are
	// assumed present in all versions. Everything else uses opt() to gracefully
	// handle missing columns in older databases.
	issueQuery := fmt.Sprintf(`
		SELECT id, %s, %s, %s,
			%s, %s, %s,
			%s, %s, %s,
			%s, %s,
			%s, %s, %s,
			%s, %s, %s, %s,
			%s, %s, %s,
			%s,
			%s, %s, %s, %s,
			%s, %s,
			%s, %s, %s,
			%s, %s, %s, %s,
			%s, %s, %s, %s,
			%s, %s, %s, %s,
			%s, %s, %s,
			%s, %s, %s,
			%s, %s, %s
		FROM issues
		ORDER BY created_at, id
	`,
		opt("content_hash", "''"), opt("title", "''"), opt("description", "''"),
		opt("design", "''"), opt("acceptance_criteria", "''"), opt("notes", "''"),
		opt("status", "''"), opt("priority", "0"), opt("issue_type", "''"),
		opt("assignee", "''"), opt("estimated_minutes", "NULL"),
		opt("created_at", "''"), opt("created_by", "''"), opt("owner", "''"),
		opt("updated_at", "''"), opt("closed_at", "NULL"), opt("external_ref", "NULL"), opt("spec_id", "''"),
		opt("compaction_level", "0"), opt("compacted_at", "''"), opt("compacted_at_commit", "NULL"),
		opt("original_size", "0"),
		opt("sender", "''"), opt("ephemeral", "0"), opt("wisp_type", "''"), opt("pinned", "0"),
		opt("is_template", "0"), opt("crystallizes", "0"),
		opt("mol_type", "''"), opt("work_type", "''"), opt("quality_score", "NULL"),
		opt("source_system", "''"), opt("source_repo", "''"), opt("close_reason", "''"), opt("closed_by_session", "''"),
		opt("event_kind", "''"), opt("actor", "''"), opt("target", "''"), opt("payload", "''"),
		opt("await_type", "''"), opt("await_id", "''"), opt("timeout_ns", "0"), opt("waiters", "''"),
		opt("hook_bead", "''"), opt("role_bead", "''"), opt("agent_state", "''"),
		opt("last_activity", "''"), opt("role_type", "''"), opt("rig", "''"),
		opt("due_at", "''"), opt("defer_until", "''"), opt("metadata", "'{}'"),
	)
	issueRows, err := db.QueryContext(ctx, issueQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query issues: %w", err)
	}
	defer issueRows.Close()

	var issues []*types.Issue
	for issueRows.Next() {
		var issue types.Issue
		var estMin sql.NullInt64
		var extRef, compactCommit sql.NullString
		var qualScore sql.NullFloat64
		var timeoutNs int64
		var waitersJSON string
		var closedAt, compactedAt, lastActivity, dueAt, deferUntil sql.NullString
		var specID, wispType, closedBySession, metadataRaw sql.NullString
		var createdByVal, ownerVal sql.NullString
		if err := issueRows.Scan(
			&issue.ID, &issue.ContentHash, &issue.Title, &issue.Description,
			&issue.Design, &issue.AcceptanceCriteria, &issue.Notes,
			&issue.Status, &issue.Priority, &issue.IssueType,
			&issue.Assignee, &estMin,
			&issue.CreatedAt, &createdByVal, &ownerVal,
			&issue.UpdatedAt, &closedAt, &extRef, &specID,
			&issue.CompactionLevel, &compactedAt, &compactCommit,
			&issue.OriginalSize,
			&issue.Sender, &issue.Ephemeral, &wispType, &issue.Pinned,
			&issue.IsTemplate, &issue.Crystallizes,
			&issue.MolType, &issue.WorkType, &qualScore,
			&issue.SourceSystem, &issue.SourceRepo, &issue.CloseReason, &closedBySession,
			&issue.EventKind, &issue.Actor, &issue.Target, &issue.Payload,
			&issue.AwaitType, &issue.AwaitID, &timeoutNs, &waitersJSON,
			&issue.HookBead, &issue.RoleBead, &issue.AgentState,
			&lastActivity, &issue.RoleType, &issue.Rig,
			&dueAt, &deferUntil, &metadataRaw,
		); err != nil {
			return nil, fmt.Errorf("failed to scan issue: %w", err)
		}
		if createdByVal.Valid {
			issue.CreatedBy = createdByVal.String
		}
		if ownerVal.Valid {
			issue.Owner = ownerVal.String
		}
		if estMin.Valid {
			v := int(estMin.Int64)
			issue.EstimatedMinutes = &v
		}
		if extRef.Valid {
			issue.ExternalRef = &extRef.String
		}
		if specID.Valid {
			issue.SpecID = specID.String
		}
		if compactCommit.Valid {
			issue.CompactedAtCommit = &compactCommit.String
		}
		if wispType.Valid {
			issue.WispType = types.WispType(wispType.String)
		}
		if closedBySession.Valid {
			issue.ClosedBySession = closedBySession.String
		}
		if qualScore.Valid {
			v := float32(qualScore.Float64)
			issue.QualityScore = &v
		}
		if metadataRaw.Valid && strings.TrimSpace(metadataRaw.String) != "" {
			issue.Metadata = normalizedJSONBytes(metadataRaw.String)
		}
		issue.ClosedAt = parseNullTime(closedAt.String)
		issue.CompactedAt = parseNullTime(compactedAt.String)
		issue.LastActivity = parseNullTime(lastActivity.String)
		issue.DueAt = parseNullTime(dueAt.String)
		issue.DeferUntil = parseNullTime(deferUntil.String)
		issue.Timeout = time.Duration(timeoutNs)
		if waitersJSON != "" {
			_ = json.Unmarshal([]byte(waitersJSON), &issue.Waiters)
		}
		issues = append(issues, &issue)
	}

	// Get labels
	labelsMap := make(map[string][]string)
	labelRows, err := db.QueryContext(ctx, "SELECT issue_id, label FROM labels ORDER BY issue_id, label")
	if err == nil {
		defer labelRows.Close()
		for labelRows.Next() {
			var issueID, label string
			if err := labelRows.Scan(&issueID, &label); err == nil {
				labelsMap[issueID] = append(labelsMap[issueID], label)
			}
		}
	}

	// Get dependencies
	depsMap := make(map[string][]*types.Dependency)
	depOpt := func(col, fallback string) string {
		return sqliteOptionalTextExpr(depCols, col, fallback)
	}
	//nolint:gosec // SQL fragments come from fixed column names discovered via PRAGMA table_info.
	depQuery := fmt.Sprintf(`
		SELECT issue_id, depends_on_id, COALESCE(type,''), %s, COALESCE(created_at,''), %s, %s
		FROM dependencies
		ORDER BY created_at, issue_id, depends_on_id
	`, depOpt("created_by", "''"), depOpt("metadata", "'{}'"), depOpt("thread_id", "''"))
	depRows, err := db.QueryContext(ctx, depQuery)
	if err == nil {
		defer depRows.Close()
		for depRows.Next() {
			var dep types.Dependency
			var metadata, threadID, depCreatedBy sql.NullString
			if err := depRows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type, &depCreatedBy, &dep.CreatedAt, &metadata, &threadID); err == nil {
				if depCreatedBy.Valid {
					dep.CreatedBy = depCreatedBy.String
				}
				if metadata.Valid {
					dep.Metadata = strings.TrimSpace(metadata.String)
				}
				if dep.Metadata == "" {
					dep.Metadata = "{}"
				}
				if threadID.Valid {
					dep.ThreadID = threadID.String
				}
				depsMap[dep.IssueID] = append(depsMap[dep.IssueID], &dep)
			}
		}
	}

	// Get events
	eventsMap := make(map[string][]*types.Event)
	eventRows, err := db.QueryContext(ctx, `
		SELECT issue_id, COALESCE(event_type,''), COALESCE(actor,''), old_value, new_value, comment, COALESCE(created_at,'')
		FROM events
		ORDER BY created_at, rowid
	`)
	if err == nil {
		defer eventRows.Close()
		for eventRows.Next() {
			var issueID string
			var event types.Event
			var oldVal, newVal, comment sql.NullString
			if err := eventRows.Scan(&issueID, &event.EventType, &event.Actor, &oldVal, &newVal, &comment, &event.CreatedAt); err == nil {
				if oldVal.Valid {
					event.OldValue = &oldVal.String
				}
				if newVal.Valid {
					event.NewValue = &newVal.String
				}
				if comment.Valid {
					event.Comment = &comment.String
				}
				eventsMap[issueID] = append(eventsMap[issueID], &event)
			}
		}
	}

	// Get comments (legacy table may be absent; ignore errors).
	commentsMap := make(map[string][]*types.Comment)
	commentRows, err := db.QueryContext(ctx, `
		SELECT issue_id, COALESCE(author,''), COALESCE(text,''), COALESCE(created_at,'')
		FROM comments
		ORDER BY created_at, rowid
	`)
	if err == nil {
		defer commentRows.Close()
		for commentRows.Next() {
			var issueID, author, text, createdAt string
			if err := commentRows.Scan(&issueID, &author, &text, &createdAt); err == nil {
				commentsMap[issueID] = append(commentsMap[issueID], &types.Comment{
					IssueID:   issueID,
					Author:    author,
					Text:      text,
					CreatedAt: parseNullableSQLiteTime(createdAt),
				})
			}
		}
	}

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
		issues:      issues,
		labelsMap:   labelsMap,
		depsMap:     depsMap,
		eventsMap:   eventsMap,
		commentsMap: commentsMap,
		config:      config,
		prefix:      prefix,
		issueCount:  len(issues),
	}, nil
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, nil
}

func parseNullableSQLiteTime(raw string) time.Time {
	if t := parseNullTime(raw); t != nil {
		return *t
	}
	return time.Time{}
}

// Helper functions for output (CGO build only — used by handleToDoltMigration)

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

	eventCount, commentCount := migrationRecordCounts(data)
	fmt.Printf("Events to migrate: %d\n", eventCount)
	fmt.Printf("Comments to migrate: %d\n", commentCount)
	fmt.Printf("Config keys: %d\n", len(data.config))

	if data.prefix != "" {
		fmt.Printf("Issue prefix: %s\n", data.prefix)
	}
	fmt.Println()
}

func printDryRun(source, target string, data *migrationData, withBackup bool) {
	eventCount, commentCount := migrationRecordCounts(data)

	if jsonOutput {
		result := map[string]interface{}{
			"dry_run":       true,
			"source":        source,
			"target":        target,
			"issue_count":   data.issueCount,
			"event_count":   eventCount,
			"comment_count": commentCount,
			"config_keys":   len(data.config),
			"prefix":        data.prefix,
			"would_backup":  withBackup,
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
		fmt.Printf("  %d. Import %d events (issue history)\n", step, eventCount)
		step++
		fmt.Printf("  %d. Import %d comments (legacy comments table)\n", step, commentCount)
		step++
		fmt.Printf("  %d. Copy %d config values\n", step, len(data.config))
		step++
		fmt.Printf("  %d. Update metadata.json\n", step)
	}
}

func migrationRecordCounts(data *migrationData) (eventCount int, commentCount int) {
	for _, events := range data.eventsMap {
		eventCount += len(events)
	}
	for _, comments := range data.commentsMap {
		commentCount += len(comments)
	}
	return eventCount, commentCount
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
		fmt.Println("  - Verify data: bd list")
		fmt.Println("  - After verification, you can delete the old database:")
		fmt.Printf("    rm %s\n", oldPath)
	}
}

// listMigrations returns registered Dolt migrations (CGO build).
func listMigrations() []string {
	return dolt.ListMigrations()
}
