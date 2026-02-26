package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// shimMigrateSQLiteToDolt performs automatic SQLite→Dolt migration using the
// system sqlite3 CLI to export data as JSON, avoiding any CGO dependency.
// This is the v1.0.0 upgrade path for users on SQLite who upgrade to a
// Dolt-only bd binary.
//
// Steps:
//  1. Detect beads.db (SQLite) in .beads/ with no Dolt database present
//  2. Export all tables to JSON via the system sqlite3 CLI
//  3. Create a new Dolt database
//  4. Import all data into Dolt
//  5. Rename beads.db to beads.db.migrated
func shimMigrateSQLiteToDolt() {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}
	doShimMigrate(beadsDir)
}

// doShimMigrate performs the actual migration for the given .beads directory.
func doShimMigrate(beadsDir string) {
	// Check for SQLite database
	sqlitePath := findSQLiteDB(beadsDir)
	if sqlitePath == "" {
		return // No SQLite database, nothing to migrate
	}

	// Skip backup/migrated files
	base := filepath.Base(sqlitePath)
	if strings.Contains(base, ".backup") || strings.Contains(base, ".migrated") {
		return
	}

	// Guard: if the file is empty (0 bytes), it's not a real SQLite database.
	// This happens when a process creates beads.db but crashes before writing.
	// Remove the empty file to prevent repeated failed migration attempts.
	if info, err := os.Stat(sqlitePath); err == nil && info.Size() == 0 {
		debug.Logf("shim-migrate: removing empty %s (not a valid database)", base)
		_ = os.Remove(sqlitePath)
		return
	}

	// Guard: if metadata.json already indicates Dolt backend, the beads.db is stale
	// leftover — not a database that needs migrating. This covers dolt-server mode
	// where there is no local dolt/ directory (data is stored remotely on the Dolt
	// SQL server), so the dolt/ directory check below would miss it.
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Backend == configfile.BackendDolt {
		migratedPath := sqlitePath + ".migrated"
		if _, err := os.Stat(migratedPath); err != nil {
			if err := os.Rename(sqlitePath, migratedPath); err == nil {
				debug.Logf("shim-migrate: renamed stale %s (backend already dolt)", base)
			}
		}
		return
	}

	// Check if Dolt already exists — if so, SQLite is leftover from a prior migration
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); err == nil {
		// Dolt exists alongside SQLite. Rename the leftover SQLite file.
		migratedPath := sqlitePath + ".migrated"
		if _, err := os.Stat(migratedPath); err != nil {
			// No .migrated file yet — rename now
			if err := os.Rename(sqlitePath, migratedPath); err == nil {
				debug.Logf("shim-migrate: renamed leftover %s to %s", filepath.Base(sqlitePath), filepath.Base(migratedPath))
			}
		}
		return
	}

	// Verify sqlite3 CLI is available
	sqlite3Path, err := exec.LookPath("sqlite3")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration requires the sqlite3 CLI tool\n")
		fmt.Fprintf(os.Stderr, "Hint: install sqlite3 and retry, or run 'bd migrate dolt' with a CGO-enabled build\n")
		return
	}
	debug.Logf("shim-migrate: using sqlite3 at %s", sqlite3Path)

	ctx := context.Background()

	// Extract data from SQLite via CLI
	fmt.Fprintf(os.Stderr, "Migrating SQLite database to Dolt (via sqlite3 CLI)...\n")
	data, err := extractViaSQLiteCLI(ctx, sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed (extract): %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: run 'bd migrate dolt' manually, or remove %s to skip\n", base)
		return
	}

	if data.issueCount == 0 {
		debug.Logf("shim-migrate: SQLite database is empty, skipping import")
	}

	// Determine database name from prefix
	dbName := "beads"
	if data.prefix != "" {
		dbName = data.prefix
	}

	// Load existing config for server connection settings
	doltCfg := &dolt.Config{
		Path:      doltPath,
		Database:  dbName,
		AutoStart: os.Getenv("GT_ROOT") == "" && os.Getenv("BEADS_DOLT_AUTO_START") != "0",
	}
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		doltCfg.ServerHost = cfg.GetDoltServerHost()
		doltCfg.ServerPort = cfg.GetDoltServerPort()
		doltCfg.ServerUser = cfg.GetDoltServerUser()
		doltCfg.ServerPassword = cfg.GetDoltServerPassword()
		doltCfg.ServerTLS = cfg.GetDoltServerTLS()
	}

	// Create Dolt store
	doltStore, err := dolt.New(ctx, doltCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed (dolt init): %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: ensure the Dolt server is running, then retry any bd command\n")
		return
	}

	// Import data
	imported, skipped, importErr := importToDolt(ctx, doltStore, data)
	if importErr != nil {
		_ = doltStore.Close()
		_ = os.RemoveAll(doltPath)
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed (import): %v\n", importErr)
		return
	}

	// Set sync mode
	if err := doltStore.SetConfig(ctx, "sync.mode", "dolt-native"); err != nil {
		debug.Logf("shim-migrate: failed to set sync.mode: %v", err)
	}

	// Commit the migration
	commitMsg := fmt.Sprintf("Auto-migrate from SQLite (shim): %d issues imported", imported)
	if err := doltStore.Commit(ctx, commitMsg); err != nil {
		debug.Logf("shim-migrate: failed to create Dolt commit: %v", err)
	}

	_ = doltStore.Close()

	// Update metadata.json to point to Dolt
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltDatabase = dbName
	// Don't set DoltServerPort — let doltserver.DefaultConfig derive it from
	// the project path at runtime (same rationale as migrate_dolt.go).
	if err := cfg.Save(beadsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update metadata.json: %v\n", err)
	}

	// Write sync.mode to config.yaml
	if err := config.SaveConfigValue("sync.mode", string(config.SyncModeDoltNative), beadsDir); err != nil {
		debug.Logf("shim-migrate: failed to write sync.mode to config.yaml: %v", err)
	}

	// Rename SQLite file to mark migration complete
	migratedPath := sqlitePath + ".migrated"
	if err := os.Rename(sqlitePath, migratedPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: migration succeeded but failed to rename %s: %v\n", base, err)
		fmt.Fprintf(os.Stderr, "Hint: manually rename or remove %s\n", sqlitePath)
	}

	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "Migrated %d issues from SQLite to Dolt (%d skipped)\n", imported, skipped)
	} else {
		fmt.Fprintf(os.Stderr, "Migrated %d issues from SQLite to Dolt\n", imported)
	}
}

// extractViaSQLiteCLI extracts all data from a SQLite database by shelling
// out to the system sqlite3 CLI. Each table is queried with .mode json and
// the resulting JSON array is parsed into Go structs.
func extractViaSQLiteCLI(_ context.Context, dbPath string) (*migrationData, error) {
	// Verify the file looks like a real SQLite database (check magic bytes)
	if err := verifySQLiteFile(dbPath); err != nil {
		return nil, err
	}

	// Extract config
	configMap, err := queryJSON(dbPath, "SELECT key, value FROM config")
	if err != nil {
		// Config table might not exist in very old databases
		debug.Logf("shim-migrate: config query failed (non-fatal): %v", err)
		configMap = nil
	}

	config := make(map[string]string)
	prefix := ""
	for _, row := range configMap {
		k, _ := row["key"].(string)
		v, _ := row["value"].(string)
		if k != "" {
			config[k] = v
		}
		if k == "issue_prefix" {
			prefix = v
		}
	}

	issueCols := queryColumnSet(dbPath, "issues")
	depCols := queryColumnSet(dbPath, "dependencies")

	specIDExpr := sqliteOptionalTextExpr(issueCols, "spec_id", "''")
	closedBySessionExpr := sqliteOptionalTextExpr(issueCols, "closed_by_session", "''")
	wispTypeExpr := sqliteOptionalTextExpr(issueCols, "wisp_type", "''")
	metadataExpr := sqliteOptionalTextExpr(issueCols, "metadata", "'{}'")

	// Extract issues
	issueQuery := fmt.Sprintf(`
		SELECT id, COALESCE(content_hash,'') as content_hash,
			COALESCE(title,'') as title, COALESCE(description,'') as description,
			COALESCE(design,'') as design, COALESCE(acceptance_criteria,'') as acceptance_criteria,
			COALESCE(notes,'') as notes,
			COALESCE(status,'') as status, COALESCE(priority,0) as priority,
			COALESCE(issue_type,'') as issue_type,
			COALESCE(assignee,'') as assignee, estimated_minutes,
			COALESCE(created_at,'') as created_at, COALESCE(created_by,'') as created_by,
			COALESCE(owner,'') as owner,
			COALESCE(updated_at,'') as updated_at, closed_at, external_ref, %s as spec_id,
			COALESCE(compaction_level,0) as compaction_level,
			COALESCE(compacted_at,'') as compacted_at, compacted_at_commit,
			COALESCE(original_size,0) as original_size,
			COALESCE(sender,'') as sender, COALESCE(ephemeral,0) as ephemeral, %s as wisp_type,
			COALESCE(pinned,0) as pinned,
			COALESCE(is_template,0) as is_template, COALESCE(crystallizes,0) as crystallizes,
			COALESCE(mol_type,'') as mol_type, COALESCE(work_type,'') as work_type,
			quality_score,
			COALESCE(source_system,'') as source_system, COALESCE(source_repo,'') as source_repo,
			COALESCE(close_reason,'') as close_reason, %s as closed_by_session,
			COALESCE(event_kind,'') as event_kind, COALESCE(actor,'') as actor,
			COALESCE(target,'') as target, COALESCE(payload,'') as payload,
			COALESCE(await_type,'') as await_type, COALESCE(await_id,'') as await_id,
			COALESCE(timeout_ns,0) as timeout_ns, COALESCE(waiters,'') as waiters,
			COALESCE(hook_bead,'') as hook_bead, COALESCE(role_bead,'') as role_bead,
			COALESCE(agent_state,'') as agent_state,
			COALESCE(last_activity,'') as last_activity, COALESCE(role_type,'') as role_type,
			COALESCE(rig,'') as rig,
			COALESCE(due_at,'') as due_at, COALESCE(defer_until,'') as defer_until,
			%s as metadata
		FROM issues
		ORDER BY created_at, id
	`, specIDExpr, wispTypeExpr, closedBySessionExpr, metadataExpr)
	issueRows, err := queryJSON(dbPath, issueQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query issues: %w", err)
	}

	issues := make([]*types.Issue, 0, len(issueRows))
	for _, row := range issueRows {
		issue := parseIssueRow(row)
		issues = append(issues, issue)
	}

	// Extract labels
	labelsMap := make(map[string][]string)
	labelRows, err := queryJSON(dbPath, "SELECT issue_id, label FROM labels ORDER BY issue_id, label")
	if err == nil {
		for _, row := range labelRows {
			issueID, _ := row["issue_id"].(string)
			label, _ := row["label"].(string)
			if issueID != "" && label != "" {
				labelsMap[issueID] = append(labelsMap[issueID], label)
			}
		}
	}

	// Extract dependencies
	depsMap := make(map[string][]*types.Dependency)
	depMetadataExpr := sqliteOptionalTextExpr(depCols, "metadata", "'{}'")
	depThreadExpr := sqliteOptionalTextExpr(depCols, "thread_id", "''")
	depQuery := fmt.Sprintf(`
		SELECT issue_id, depends_on_id, COALESCE(type,'') as type, COALESCE(created_by,'') as created_by, COALESCE(created_at,'') as created_at,
			%s as metadata, %s as thread_id
		FROM dependencies
		ORDER BY created_at, issue_id, depends_on_id
	`, depMetadataExpr, depThreadExpr)
	depRows, err := queryJSON(dbPath, depQuery)
	if err == nil {
		for _, row := range depRows {
			dep := &types.Dependency{
				IssueID:     jsonStr(row, "issue_id"),
				DependsOnID: jsonStr(row, "depends_on_id"),
				Type:        types.DependencyType(jsonStr(row, "type")),
				CreatedBy:   jsonStr(row, "created_by"),
				CreatedAt:   jsonTime(row, "created_at"),
				Metadata:    strings.TrimSpace(jsonFlexibleText(row, "metadata")),
				ThreadID:    jsonStr(row, "thread_id"),
			}
			if dep.Metadata == "" {
				dep.Metadata = "{}"
			}
			if dep.IssueID != "" {
				depsMap[dep.IssueID] = append(depsMap[dep.IssueID], dep)
			}
		}
	}

	// Extract events
	eventsMap := make(map[string][]*types.Event)
	eventRows, err := queryJSON(dbPath, `
		SELECT issue_id, COALESCE(event_type,'') as event_type, COALESCE(actor,'') as actor, old_value, new_value, comment, COALESCE(created_at,'') as created_at
		FROM events
		ORDER BY created_at, rowid
	`)
	if err == nil {
		for _, row := range eventRows {
			issueID := jsonStr(row, "issue_id")
			event := &types.Event{
				EventType: types.EventType(jsonStr(row, "event_type")),
				Actor:     jsonStr(row, "actor"),
				CreatedAt: jsonTime(row, "created_at"),
			}
			if v := jsonNullableStr(row, "old_value"); v != nil {
				event.OldValue = v
			}
			if v := jsonNullableStr(row, "new_value"); v != nil {
				event.NewValue = v
			}
			if v := jsonNullableStr(row, "comment"); v != nil {
				event.Comment = v
			}
			if issueID != "" {
				eventsMap[issueID] = append(eventsMap[issueID], event)
			}
		}
	}

	// Extract comments (legacy table may be absent).
	commentsMap := make(map[string][]*types.Comment)
	commentRows, err := queryJSON(dbPath, `
		SELECT issue_id, COALESCE(author,'') as author, COALESCE(text,'') as text, COALESCE(created_at,'') as created_at
		FROM comments
		ORDER BY created_at, rowid
	`)
	if err == nil {
		for _, row := range commentRows {
			issueID := jsonStr(row, "issue_id")
			if issueID == "" {
				continue
			}
			commentsMap[issueID] = append(commentsMap[issueID], &types.Comment{
				IssueID:   issueID,
				Author:    jsonStr(row, "author"),
				Text:      jsonStr(row, "text"),
				CreatedAt: jsonTime(row, "created_at"),
			})
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

func queryColumnSet(dbPath, table string) map[string]bool {
	rows, err := queryJSON(dbPath, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil
	}
	columns := make(map[string]bool, len(rows))
	for _, row := range rows {
		name := jsonStr(row, "name")
		if name != "" {
			columns[name] = true
		}
	}
	return columns
}

// queryJSON runs a SQL query against a SQLite database using the sqlite3 CLI
// with JSON output mode. Returns a slice of maps representing each row.
func queryJSON(dbPath, query string) ([]map[string]interface{}, error) {
	// Build sqlite3 command: .mode json + query
	input := fmt.Sprintf(".mode json\n%s\n", strings.TrimSpace(query))

	cmd := exec.Command("sqlite3", "-readonly", dbPath)
	cmd.Stdin = strings.NewReader(input)

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("sqlite3 query failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("sqlite3 query failed: %w", err)
	}

	// Empty result
	output := strings.TrimSpace(string(out))
	if output == "" || output == "[]" {
		return nil, nil
	}

	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		return nil, fmt.Errorf("failed to parse sqlite3 JSON output: %w", err)
	}

	return rows, nil
}

// verifySQLiteFile checks that a file starts with the SQLite magic bytes.
func verifySQLiteFile(path string) error {
	f, err := os.Open(path) //nolint:gosec // path is constructed internally, not from user input
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	magic := make([]byte, 16)
	n, err := f.Read(magic)
	if err != nil || n < 16 {
		return fmt.Errorf("file too small to be a SQLite database")
	}

	if string(magic[:15]) != "SQLite format 3" {
		return fmt.Errorf("file is not a SQLite database (bad magic bytes)")
	}

	return nil
}

// parseIssueRow converts a JSON row map into a types.Issue.
func parseIssueRow(row map[string]interface{}) *types.Issue {
	issue := &types.Issue{
		ID:                 jsonStr(row, "id"),
		ContentHash:        jsonStr(row, "content_hash"),
		Title:              jsonStr(row, "title"),
		Description:        jsonStr(row, "description"),
		Design:             jsonStr(row, "design"),
		AcceptanceCriteria: jsonStr(row, "acceptance_criteria"),
		Notes:              jsonStr(row, "notes"),
		Status:             types.Status(jsonStr(row, "status")),
		Priority:           jsonInt(row, "priority"),
		IssueType:          types.IssueType(jsonStr(row, "issue_type")),
		Assignee:           jsonStr(row, "assignee"),
		CreatedAt:          jsonTime(row, "created_at"),
		CreatedBy:          jsonStr(row, "created_by"),
		Owner:              jsonStr(row, "owner"),
		UpdatedAt:          jsonTime(row, "updated_at"),
		SpecID:             jsonStr(row, "spec_id"),
		CompactionLevel:    jsonInt(row, "compaction_level"),
		OriginalSize:       jsonInt(row, "original_size"),
		Sender:             jsonStr(row, "sender"),
		Ephemeral:          jsonBool(row, "ephemeral"),
		WispType:           types.WispType(jsonStr(row, "wisp_type")),
		Pinned:             jsonBool(row, "pinned"),
		IsTemplate:         jsonBool(row, "is_template"),
		Crystallizes:       jsonBool(row, "crystallizes"),
		MolType:            types.MolType(jsonStr(row, "mol_type")),
		WorkType:           types.WorkType(jsonStr(row, "work_type")),
		SourceSystem:       jsonStr(row, "source_system"),
		SourceRepo:         jsonStr(row, "source_repo"),
		CloseReason:        jsonStr(row, "close_reason"),
		ClosedBySession:    jsonStr(row, "closed_by_session"),
		EventKind:          jsonStr(row, "event_kind"),
		Actor:              jsonStr(row, "actor"),
		Target:             jsonStr(row, "target"),
		Payload:            jsonStr(row, "payload"),
		AwaitType:          jsonStr(row, "await_type"),
		AwaitID:            jsonStr(row, "await_id"),
		HookBead:           jsonStr(row, "hook_bead"),
		RoleBead:           jsonStr(row, "role_bead"),
		AgentState:         types.AgentState(jsonStr(row, "agent_state")),
		RoleType:           jsonStr(row, "role_type"),
		Rig:                jsonStr(row, "rig"),
	}

	// Nullable fields
	if v := jsonNullableInt(row, "estimated_minutes"); v != nil {
		issue.EstimatedMinutes = v
	}
	if v := jsonNullableStr(row, "external_ref"); v != nil {
		issue.ExternalRef = v
	}
	if v := jsonNullableStr(row, "compacted_at_commit"); v != nil {
		issue.CompactedAtCommit = v
	}
	if v := jsonNullableFloat32(row, "quality_score"); v != nil {
		issue.QualityScore = v
	}
	issue.Metadata = normalizedJSONBytes(jsonFlexibleText(row, "metadata"))

	// Time fields
	issue.ClosedAt = parseNullTime(jsonStr(row, "closed_at"))
	issue.CompactedAt = parseNullTime(jsonStr(row, "compacted_at"))
	issue.LastActivity = parseNullTime(jsonStr(row, "last_activity"))
	issue.DueAt = parseNullTime(jsonStr(row, "due_at"))
	issue.DeferUntil = parseNullTime(jsonStr(row, "defer_until"))

	// Timeout duration
	issue.Timeout = time.Duration(jsonInt64(row, "timeout_ns"))

	// Waiters
	waitersJSON := jsonStr(row, "waiters")
	if waitersJSON != "" {
		_ = json.Unmarshal([]byte(waitersJSON), &issue.Waiters)
	}

	return issue
}

// JSON row accessor helpers

func jsonFlexibleText(row map[string]interface{}, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	default:
		data, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}

func jsonStr(row map[string]interface{}, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		// JSON numbers come as float64
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func jsonNullableStr(row map[string]interface{}, key string) *string {
	v, ok := row[key]
	if !ok || v == nil {
		return nil
	}
	s := fmt.Sprintf("%v", v)
	return &s
}

func jsonInt(row map[string]interface{}, key string) int {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case string:
		i, _ := strconv.Atoi(val)
		return i
	default:
		return 0
	}
}

func jsonInt64(row map[string]interface{}, key string) int64 {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int64(val)
	case string:
		i, _ := strconv.ParseInt(val, 10, 64)
		return i
	default:
		return 0
	}
}

func jsonBool(row map[string]interface{}, key string) bool {
	v, ok := row[key]
	if !ok || v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	case string:
		return val == "1" || val == "true"
	default:
		return false
	}
}

func jsonNullableInt(row map[string]interface{}, key string) *int {
	v, ok := row[key]
	if !ok || v == nil {
		return nil
	}
	switch val := v.(type) {
	case float64:
		i := int(val)
		return &i
	case string:
		i, err := strconv.Atoi(val)
		if err != nil {
			return nil
		}
		return &i
	default:
		return nil
	}
}

func jsonTime(row map[string]interface{}, key string) time.Time {
	s := jsonStr(row, key)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func jsonNullableFloat32(row map[string]interface{}, key string) *float32 {
	v, ok := row[key]
	if !ok || v == nil {
		return nil
	}
	switch val := v.(type) {
	case float64:
		f := float32(val)
		return &f
	case string:
		f, err := strconv.ParseFloat(val, 32)
		if err != nil {
			return nil
		}
		f32 := float32(f)
		return &f32
	default:
		return nil
	}
}
