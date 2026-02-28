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
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
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

	// Guard: if metadata.json explicitly indicates SQLite backend, the user has
	// opted to keep SQLite. Do NOT auto-migrate. Fixes GH#2016.
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		// Use GetBackend() for SQLite check (normalizes case, checks env var)
		if cfg.GetBackend() == configfile.BackendSQLite {
			debug.Logf("shim-migrate: skipping — backend explicitly set to sqlite")
			return
		}
		// Use raw field for Dolt check: only match when metadata.json was
		// explicitly written with "dolt" (by a prior migration). Empty/missing
		// Backend field means legacy pre-Dolt config that needs migration.
		if strings.EqualFold(cfg.Backend, configfile.BackendDolt) {
			migratedPath := sqlitePath + ".migrated"
			if _, err := os.Stat(migratedPath); err != nil {
				if err := os.Rename(sqlitePath, migratedPath); err == nil {
					debug.Logf("shim-migrate: renamed stale %s (backend already dolt)", base)
				}
			}
			return
		}
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
		fmt.Fprintf(os.Stderr, "Hint: install sqlite3 and retry, or run 'bd migrate --to-dolt' with a CGO-enabled build\n")
		return
	}
	debug.Logf("shim-migrate: using sqlite3 at %s", sqlite3Path)

	ctx := context.Background()

	// Phase 1: Backup — ALWAYS backup before touching anything
	fmt.Fprintf(os.Stderr, "Backing up SQLite database...\n")
	backupPath, backupErr := backupSQLite(sqlitePath)
	if backupErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration aborted (backup failed): %v\n", backupErr)
		fmt.Fprintf(os.Stderr, "Hint: check disk space and permissions, then retry\n")
		return
	}
	fmt.Fprintf(os.Stderr, "  Backup saved to %s\n", filepath.Base(backupPath))

	// Extract data from SQLite via CLI
	fmt.Fprintf(os.Stderr, "Extracting data from SQLite (via sqlite3 CLI)...\n")
	data, err := extractViaSQLiteCLI(ctx, sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed (extract): %v\n", err)
		fmt.Fprintf(os.Stderr, "Hint: run 'bd migrate --to-dolt' manually, or remove %s to skip\n", base)
		fmt.Fprintf(os.Stderr, "  Your backup is at: %s\n", backupPath)
		return
	}

	if data.issueCount == 0 {
		debug.Logf("shim-migrate: SQLite database is empty, migrating empty database")
	}
	fmt.Fprintf(os.Stderr, "  Found %d issues\n", data.issueCount)

	// Determine database name from prefix
	dbName := "beads"
	if data.prefix != "" {
		dbName = data.prefix
	}

	// Resolve server connection settings
	resolvedHost := "127.0.0.1"
	resolvedPort := 0
	resolvedUser := "root"
	resolvedPassword := ""
	resolvedTLS := false
	autoStart := os.Getenv("GT_ROOT") == "" && os.Getenv("BEADS_DOLT_AUTO_START") != "0"
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		resolvedHost = cfg.GetDoltServerHost()
		resolvedPort = doltserver.DefaultConfig(beadsDir).Port
		resolvedUser = cfg.GetDoltServerUser()
		resolvedPassword = cfg.GetDoltServerPassword()
		resolvedTLS = cfg.GetDoltServerTLS()
	}

	// Run shared migration phases: verify → import → commit → verify data → finalize
	doltPath = filepath.Join(beadsDir, "dolt")
	params := &migrationParams{
		beadsDir:       beadsDir,
		sqlitePath:     sqlitePath,
		backupPath:     backupPath,
		data:           data,
		dbName:         dbName,
		serverHost:     resolvedHost,
		serverPort:     resolvedPort,
		serverUser:     resolvedUser,
		serverPassword: resolvedPassword,
		doltCfg: &dolt.Config{
			Path:            doltPath,
			Database:        dbName,
			CreateIfMissing: true, // migration creates a new Dolt database
			ServerHost:      resolvedHost,
			ServerPort:      resolvedPort,
			ServerUser:      resolvedUser,
			ServerPassword:  resolvedPassword,
			ServerTLS:       resolvedTLS,
			AutoStart:       autoStart,
		},
	}

	imported, skipped, migErr := runMigrationPhases(ctx, params)
	if migErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: SQLite auto-migration failed: %v\n", migErr)
		return
	}

	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "Migrated %d issues from SQLite to Dolt (%d skipped)\n", imported, skipped)
	} else {
		fmt.Fprintf(os.Stderr, "Migrated %d issues from SQLite to Dolt ✓\n", imported)
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

	// All columns that may be missing in older beads database schemas.
	// Uses sqliteOptionalTextExpr which checks PRAGMA table_info before querying.
	// Fixes "no such column" errors for pre-v0.49 databases (GH#2016).
	opt := func(col, fallback string) string {
		return sqliteOptionalTextExpr(issueCols, col, fallback)
	}

	// Extract issues
	issueQuery := fmt.Sprintf(`
		SELECT id, %s as content_hash,
			%s as title, %s as description,
			%s as design, %s as acceptance_criteria,
			%s as notes,
			%s as status, %s as priority,
			%s as issue_type,
			%s as assignee, %s as estimated_minutes,
			%s as created_at, %s as created_by,
			%s as owner,
			%s as updated_at, %s as closed_at, %s as external_ref, %s as spec_id,
			%s as compaction_level,
			%s as compacted_at, %s as compacted_at_commit,
			%s as original_size,
			%s as sender, %s as ephemeral, %s as wisp_type,
			%s as pinned,
			%s as is_template, %s as crystallizes,
			%s as mol_type, %s as work_type,
			%s as quality_score,
			%s as source_system, %s as source_repo,
			%s as close_reason, %s as closed_by_session,
			%s as event_kind, %s as actor,
			%s as target, %s as payload,
			%s as await_type, %s as await_id,
			%s as timeout_ns, %s as waiters,
			%s as hook_bead, %s as role_bead,
			%s as agent_state,
			%s as last_activity, %s as role_type,
			%s as rig,
			%s as due_at, %s as defer_until,
			%s as metadata
		FROM issues
		ORDER BY created_at, id
	`,
		opt("content_hash", "''"),
		opt("title", "''"), opt("description", "''"),
		opt("design", "''"), opt("acceptance_criteria", "''"),
		opt("notes", "''"),
		opt("status", "''"), opt("priority", "0"),
		opt("issue_type", "''"),
		opt("assignee", "''"), opt("estimated_minutes", "NULL"),
		opt("created_at", "''"), opt("created_by", "''"),
		opt("owner", "''"),
		opt("updated_at", "''"), opt("closed_at", "NULL"), opt("external_ref", "NULL"), opt("spec_id", "''"),
		opt("compaction_level", "0"),
		opt("compacted_at", "''"), opt("compacted_at_commit", "NULL"),
		opt("original_size", "0"),
		opt("sender", "''"), opt("ephemeral", "0"), opt("wisp_type", "''"),
		opt("pinned", "0"),
		opt("is_template", "0"), opt("crystallizes", "0"),
		opt("mol_type", "''"), opt("work_type", "''"),
		opt("quality_score", "NULL"),
		opt("source_system", "''"), opt("source_repo", "''"),
		opt("close_reason", "''"), opt("closed_by_session", "''"),
		opt("event_kind", "''"), opt("actor", "''"),
		opt("target", "''"), opt("payload", "''"),
		opt("await_type", "''"), opt("await_id", "''"),
		opt("timeout_ns", "0"), opt("waiters", "''"),
		opt("hook_bead", "''"), opt("role_bead", "''"),
		opt("agent_state", "''"),
		opt("last_activity", "''"), opt("role_type", "''"),
		opt("rig", "''"),
		opt("due_at", "''"), opt("defer_until", "''"),
		opt("metadata", "'{}'"),
	)
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
	depOpt := func(col, fallback string) string {
		return sqliteOptionalTextExpr(depCols, col, fallback)
	}
	depQuery := fmt.Sprintf(`
		SELECT issue_id, depends_on_id, COALESCE(type,'') as type, %s as created_by, COALESCE(created_at,'') as created_at,
			%s as metadata, %s as thread_id
		FROM dependencies
		ORDER BY created_at, issue_id, depends_on_id
	`, depOpt("created_by", "''"), depOpt("metadata", "'{}'"), depOpt("thread_id", "''"))
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
