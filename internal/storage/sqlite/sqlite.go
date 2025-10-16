// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Import SQLite driver
	"github.com/steveyegge/beads/internal/types"
	_ "modernc.org/sqlite"
)

// SQLiteStorage implements the Storage interface using SQLite
type SQLiteStorage struct {
	db *sql.DB
}

// New creates a new SQLite storage backend
func New(path string) (*SQLiteStorage, error) {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open database with WAL mode for better concurrency and busy timeout for parallel writes
	// _pragma=busy_timeout(30000) means wait up to 30 seconds for locks instead of failing immediately
	// Higher timeout helps with parallel issue creation from multiple processes
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=ON&_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Initialize schema
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Migrate existing databases to add dirty_issues table if missing
	if err := migrateDirtyIssuesTable(db); err != nil {
		return nil, fmt.Errorf("failed to migrate dirty_issues table: %w", err)
	}

	// Migrate existing databases to add issue_counters table if missing
	if err := migrateIssueCountersTable(db); err != nil {
		return nil, fmt.Errorf("failed to migrate issue_counters table: %w", err)
	}

	// Migrate existing databases to add external_ref column if missing
	if err := migrateExternalRefColumn(db); err != nil {
		return nil, fmt.Errorf("failed to migrate external_ref column: %w", err)
	}

	// Migrate existing databases to add composite index on dependencies
	if err := migrateCompositeIndexes(db); err != nil {
		return nil, fmt.Errorf("failed to migrate composite indexes: %w", err)
	}

	// Migrate existing databases to add status/closed_at CHECK constraint
	if err := migrateClosedAtConstraint(db); err != nil {
		return nil, fmt.Errorf("failed to migrate closed_at constraint: %w", err)
	}

	return &SQLiteStorage{
		db: db,
	}, nil
}

// migrateDirtyIssuesTable checks if the dirty_issues table exists and creates it if missing.
// This ensures existing databases created before the incremental export feature get migrated automatically.
func migrateDirtyIssuesTable(db *sql.DB) error {
	// Check if dirty_issues table exists
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='dirty_issues'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		// Table doesn't exist, create it
		_, err := db.Exec(`
			CREATE TABLE dirty_issues (
				issue_id TEXT PRIMARY KEY,
				marked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
			);
			CREATE INDEX idx_dirty_issues_marked_at ON dirty_issues(marked_at);
		`)
		if err != nil {
			return fmt.Errorf("failed to create dirty_issues table: %w", err)
		}
		// Table created successfully - no need to log, happens silently
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for dirty_issues table: %w", err)
	}

	// Table exists, no migration needed
	return nil
}

// migrateIssueCountersTable checks if the issue_counters table needs initialization.
// This ensures existing databases created before the atomic counter feature get migrated automatically.
// The table may already exist (created by schema), but be empty - in that case we still need to sync.
func migrateIssueCountersTable(db *sql.DB) error {
	// Check if the table exists (it should, created by schema)
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='issue_counters'
	`).Scan(&tableName)

	tableExists := err == nil

	if !tableExists {
		if err != sql.ErrNoRows {
			return fmt.Errorf("failed to check for issue_counters table: %w", err)
		}
		// Table doesn't exist, create it (shouldn't happen with schema, but handle it)
		_, err := db.Exec(`
			CREATE TABLE issue_counters (
				prefix TEXT PRIMARY KEY,
				last_id INTEGER NOT NULL DEFAULT 0
			)
		`)
		if err != nil {
			return fmt.Errorf("failed to create issue_counters table: %w", err)
		}
	}

	// Check if table is empty - if so, we need to sync from existing issues
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM issue_counters`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to count issue_counters: %w", err)
	}

	if count == 0 {
		// Table is empty, sync counters from existing issues to prevent ID collisions
		// This is safe to do during migration since it's a one-time operation
		_, err = db.Exec(`
			INSERT INTO issue_counters (prefix, last_id)
			SELECT
				substr(id, 1, instr(id, '-') - 1) as prefix,
				MAX(CAST(substr(id, instr(id, '-') + 1) AS INTEGER)) as max_id
			FROM issues
			WHERE instr(id, '-') > 0
			  AND substr(id, instr(id, '-') + 1) GLOB '[0-9]*'
			GROUP BY prefix
			ON CONFLICT(prefix) DO UPDATE SET
				last_id = MAX(last_id, excluded.last_id)
		`)
		if err != nil {
			return fmt.Errorf("failed to sync counters during migration: %w", err)
		}
	}

	// Table exists and is initialized (either was already populated, or we just synced it)
	return nil
}

// migrateExternalRefColumn checks if the external_ref column exists and adds it if missing.
// This ensures existing databases created before the external reference feature get migrated automatically.
func migrateExternalRefColumn(db *sql.DB) error {
	// Check if external_ref column exists
	var columnExists bool
	rows, err := db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return fmt.Errorf("failed to check schema: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt *string
		err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		if err != nil {
			return fmt.Errorf("failed to scan column info: %w", err)
		}
		if name == "external_ref" {
			columnExists = true
			break
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error reading column info: %w", err)
	}

	if !columnExists {
		// Add external_ref column
		_, err := db.Exec(`ALTER TABLE issues ADD COLUMN external_ref TEXT`)
		if err != nil {
			return fmt.Errorf("failed to add external_ref column: %w", err)
		}
	}

	return nil
}

// migrateCompositeIndexes checks if composite indexes exist and creates them if missing.
// This ensures existing databases get performance optimizations from new indexes.
func migrateCompositeIndexes(db *sql.DB) error {
	// Check if idx_dependencies_depends_on_type exists
	var indexName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='index' AND name='idx_dependencies_depends_on_type'
	`).Scan(&indexName)

	if err == sql.ErrNoRows {
		// Index doesn't exist, create it
		_, err := db.Exec(`
			CREATE INDEX idx_dependencies_depends_on_type ON dependencies(depends_on_id, type)
		`)
		if err != nil {
			return fmt.Errorf("failed to create composite index idx_dependencies_depends_on_type: %w", err)
		}
		// Index created successfully
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for composite index: %w", err)
	}

	// Index exists, no migration needed
	return nil
}

// migrateClosedAtConstraint cleans up inconsistent status/closed_at data.
// The CHECK constraint is in the schema for new databases, but we can't easily
// add it to existing tables without recreating them. Instead, we clean the data
// and rely on application code (UpdateIssue, import.go) to maintain the invariant.
func migrateClosedAtConstraint(db *sql.DB) error {
	// Check if there are any inconsistent rows
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM issues
		WHERE (CASE WHEN status = 'closed' THEN 1 ELSE 0 END) <>
		      (CASE WHEN closed_at IS NOT NULL THEN 1 ELSE 0 END)
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to count inconsistent issues: %w", err)
	}

	if count == 0 {
		// No inconsistent data, nothing to do
		return nil
	}

	// Clean inconsistent data: trust the status field
	// Strategy: If status != 'closed' but closed_at is set, clear closed_at
	//          If status = 'closed' but closed_at is not set, set it to updated_at (best guess)
	_, err = db.Exec(`
		UPDATE issues
		SET closed_at = NULL
		WHERE status != 'closed' AND closed_at IS NOT NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to clear closed_at for non-closed issues: %w", err)
	}

	_, err = db.Exec(`
		UPDATE issues
		SET closed_at = COALESCE(updated_at, CURRENT_TIMESTAMP)
		WHERE status = 'closed' AND closed_at IS NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to set closed_at for closed issues: %w", err)
	}

	// Migration complete - data is now consistent
	return nil
}

// getNextIDForPrefix atomically generates the next ID for a given prefix
// Uses the issue_counters table for atomic, cross-process ID generation
func (s *SQLiteStorage) getNextIDForPrefix(ctx context.Context, prefix string) (int, error) {
	var nextID int
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO issue_counters (prefix, last_id)
		VALUES (?, 1)
		ON CONFLICT(prefix) DO UPDATE SET
			last_id = last_id + 1
		RETURNING last_id
	`, prefix).Scan(&nextID)
	if err != nil {
		return 0, fmt.Errorf("failed to generate next ID for prefix %s: %w", prefix, err)
	}
	return nextID, nil
}

// ensureCounterInitialized checks if a counter exists for the given prefix,
// and initializes it from existing issues if needed. This is lazy initialization
// to avoid scanning the entire issues table on every CreateIssue call.
func (s *SQLiteStorage) ensureCounterInitialized(ctx context.Context, prefix string) error {
	// Check if counter already exists for this prefix
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM issue_counters WHERE prefix = ?`, prefix).Scan(&exists)

	if err == nil {
		// Counter exists, we're good
		return nil
	}

	if err != sql.ErrNoRows {
		// Unexpected error
		return fmt.Errorf("failed to check counter existence: %w", err)
	}

	// Counter doesn't exist, initialize it from existing issues with this prefix
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO issue_counters (prefix, last_id)
		SELECT ?, COALESCE(MAX(CAST(substr(id, LENGTH(?) + 2) AS INTEGER)), 0)
		FROM issues
		WHERE id LIKE ? || '-%'
		  AND substr(id, LENGTH(?) + 2) GLOB '[0-9]*'
		ON CONFLICT(prefix) DO UPDATE SET
			last_id = MAX(last_id, excluded.last_id)
	`, prefix, prefix, prefix, prefix)
	if err != nil {
		return fmt.Errorf("failed to initialize counter for prefix %s: %w", prefix, err)
	}

	return nil
}

// SyncAllCounters synchronizes all ID counters based on existing issues in the database
// This scans all issues and updates counters to prevent ID collisions with auto-generated IDs
func (s *SQLiteStorage) SyncAllCounters(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO issue_counters (prefix, last_id)
		SELECT
			substr(id, 1, instr(id, '-') - 1) as prefix,
			MAX(CAST(substr(id, instr(id, '-') + 1) AS INTEGER)) as max_id
		FROM issues
		WHERE instr(id, '-') > 0
		  AND substr(id, instr(id, '-') + 1) GLOB '[0-9]*'
		GROUP BY prefix
		ON CONFLICT(prefix) DO UPDATE SET
			last_id = MAX(last_id, excluded.last_id)
	`)
	if err != nil {
		return fmt.Errorf("failed to sync counters: %w", err)
	}
	return nil
}

// CreateIssue creates a new issue
func (s *SQLiteStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	// Validate issue before creating
	if err := issue.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Set timestamps
	now := time.Now()
	issue.CreatedAt = now
	issue.UpdatedAt = now

	// Acquire a dedicated connection for the transaction.
	// This is necessary because we need to execute raw SQL ("BEGIN IMMEDIATE", "COMMIT")
	// on the same connection, and database/sql's connection pool would otherwise
	// use different connections for different queries.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	// Start IMMEDIATE transaction to acquire write lock early and prevent race conditions.
	// IMMEDIATE acquires a RESERVED lock immediately, preventing other IMMEDIATE or EXCLUSIVE
	// transactions from starting. This serializes ID generation across concurrent writers.
	//
	// We use raw Exec instead of BeginTx because database/sql doesn't support transaction
	// modes in BeginTx, and modernc.org/sqlite's BeginTx always uses DEFERRED mode.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("failed to begin immediate transaction: %w", err)
	}

	// Track commit state for defer cleanup
	// Use context.Background() for ROLLBACK to ensure cleanup happens even if ctx is cancelled
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Generate ID if not set (inside transaction to prevent race conditions)
	if issue.ID == "" {
		// Get prefix from config, default to "bd"
		var prefix string
		err := conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
		if err == sql.ErrNoRows || prefix == "" {
			prefix = "bd"
		} else if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}

		// Atomically initialize counter (if needed) and get next ID (within transaction)
		// This ensures the counter starts from the max existing ID, not 1
		// CRITICAL: We rely on BEGIN IMMEDIATE above to serialize this operation across processes
		//
		// The query works as follows:
		// 1. Try to INSERT with last_id = MAX(existing IDs) or 1 if none exist
		// 2. ON CONFLICT: update last_id to MAX(existing last_id, new calculated last_id) + 1
		// 3. RETURNING gives us the final incremented value
		//
		// This atomically handles three cases:
		// - Counter doesn't exist: initialize from existing issues and return next ID
		// - Counter exists but lower than max ID: update to max and return next ID
		// - Counter exists and correct: just increment and return next ID
		var nextID int
		err = conn.QueryRowContext(ctx, `
			INSERT INTO issue_counters (prefix, last_id)
			SELECT ?, COALESCE(MAX(CAST(substr(id, LENGTH(?) + 2) AS INTEGER)), 0) + 1
			FROM issues
			WHERE id LIKE ? || '-%'
			  AND substr(id, LENGTH(?) + 2) GLOB '[0-9]*'
			ON CONFLICT(prefix) DO UPDATE SET
				last_id = MAX(
					last_id,
					(SELECT COALESCE(MAX(CAST(substr(id, LENGTH(?) + 2) AS INTEGER)), 0)
					 FROM issues
					 WHERE id LIKE ? || '-%'
					   AND substr(id, LENGTH(?) + 2) GLOB '[0-9]*')
				) + 1
			RETURNING last_id
		`, prefix, prefix, prefix, prefix, prefix, prefix, prefix).Scan(&nextID)
		if err != nil {
			return fmt.Errorf("failed to generate next ID for prefix %s: %w", prefix, err)
		}

		issue.ID = fmt.Sprintf("%s-%d", prefix, nextID)
	}

	// Insert issue
	_, err = conn.ExecContext(ctx, `
		INSERT INTO issues (
			id, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		issue.ID, issue.Title, issue.Description, issue.Design,
		issue.AcceptanceCriteria, issue.Notes, issue.Status,
		issue.Priority, issue.IssueType, issue.Assignee,
		issue.EstimatedMinutes, issue.CreatedAt, issue.UpdatedAt,
		issue.ClosedAt, issue.ExternalRef,
	)
	if err != nil {
		return fmt.Errorf("failed to insert issue: %w", err)
	}

	// Record creation event
	eventData, err := json.Marshal(issue)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		eventData = []byte(fmt.Sprintf(`{"id":"%s","title":"%s"}`, issue.ID, issue.Title))
	}
	eventDataStr := string(eventData)
	_, err = conn.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, new_value)
		VALUES (?, ?, ?, ?)
	`, issue.ID, types.EventCreated, actor, eventDataStr)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = conn.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, issue.ID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	// Commit the transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	return nil
}

// CreateIssues creates multiple issues atomically in a single transaction.
// This provides significant performance improvements over calling CreateIssue in a loop:
// - Single connection acquisition
// - Single transaction
// - Atomic ID range reservation (one counter update for N issues)
// - All-or-nothing atomicity
//
// Expected 5-10x speedup for batches of 10+ issues.
func (s *SQLiteStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	// Handle empty batch
	if len(issues) == 0 {
		return nil
	}

	// Phase 1: Check for nil and validate all issues first (fail-fast)
	now := time.Now()
	for i, issue := range issues {
		if issue == nil {
			return fmt.Errorf("issue %d is nil", i)
		}

		issue.CreatedAt = now
		issue.UpdatedAt = now

		if err := issue.Validate(); err != nil {
			return fmt.Errorf("validation failed for issue %d: %w", i, err)
		}
	}

	// Phase 2: Acquire dedicated connection and start transaction
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	// Begin IMMEDIATE transaction to acquire write lock early
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("failed to begin immediate transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Phase 3: Batch ID generation
	// Count how many issues need IDs
	needIDCount := 0
	for _, issue := range issues {
		if issue.ID == "" {
			needIDCount++
		}
	}

	// Generate ID range atomically if needed
	if needIDCount > 0 {
		// Get prefix from config
		var prefix string
		err := conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
		if err == sql.ErrNoRows || prefix == "" {
			prefix = "bd"
		} else if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}

		// Atomically reserve ID range: [nextID-needIDCount+1, nextID]
		// This is the key optimization - one counter update instead of N
		var nextID int
		err = conn.QueryRowContext(ctx, `
			INSERT INTO issue_counters (prefix, last_id)
			SELECT ?, COALESCE(MAX(CAST(substr(id, LENGTH(?) + 2) AS INTEGER)), 0) + ?
			FROM issues
			WHERE id LIKE ? || '-%'
			  AND substr(id, LENGTH(?) + 2) GLOB '[0-9]*'
			ON CONFLICT(prefix) DO UPDATE SET
				last_id = MAX(
					last_id,
					(SELECT COALESCE(MAX(CAST(substr(id, LENGTH(?) + 2) AS INTEGER)), 0)
					 FROM issues
					 WHERE id LIKE ? || '-%'
					   AND substr(id, LENGTH(?) + 2) GLOB '[0-9]*')
				) + ?
			RETURNING last_id
		`, prefix, prefix, needIDCount, prefix, prefix, prefix, prefix, prefix, needIDCount).Scan(&nextID)
		if err != nil {
			return fmt.Errorf("failed to generate ID range: %w", err)
		}

		// Assign IDs sequentially from the reserved range
		currentID := nextID - needIDCount + 1
		for i := range issues {
			if issues[i].ID == "" {
				issues[i].ID = fmt.Sprintf("%s-%d", prefix, currentID)
				currentID++
			}
		}
	}

	// Phase 4: Bulk insert issues using prepared statement
	stmt, err := conn.PrepareContext(ctx, `
		INSERT INTO issues (
			id, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, issue := range issues {
		_, err = stmt.ExecContext(ctx,
			issue.ID, issue.Title, issue.Description, issue.Design,
			issue.AcceptanceCriteria, issue.Notes, issue.Status,
			issue.Priority, issue.IssueType, issue.Assignee,
			issue.EstimatedMinutes, issue.CreatedAt, issue.UpdatedAt,
			issue.ClosedAt, issue.ExternalRef,
		)
		if err != nil {
			return fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
		}
	}

	// Phase 5: Bulk record creation events
	eventStmt, err := conn.PrepareContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, new_value)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare event statement: %w", err)
	}
	defer eventStmt.Close()

	for _, issue := range issues {
		eventData, err := json.Marshal(issue)
		if err != nil {
			// Fall back to minimal description if marshaling fails
			eventData = []byte(fmt.Sprintf(`{"id":"%s","title":"%s"}`, issue.ID, issue.Title))
		}

		_, err = eventStmt.ExecContext(ctx, issue.ID, types.EventCreated, actor, string(eventData))
		if err != nil {
			return fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
	}

	// Phase 6: Bulk mark dirty for incremental export
	dirtyStmt, err := conn.PrepareContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare dirty statement: %w", err)
	}
	defer dirtyStmt.Close()

	dirtyTime := time.Now()
	for _, issue := range issues {
		_, err = dirtyStmt.ExecContext(ctx, issue.ID, dirtyTime)
		if err != nil {
			return fmt.Errorf("failed to mark dirty %s: %w", issue.ID, err)
		}
	}

	// Phase 7: Commit transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	committed = true
	return nil
}

// GetIssue retrieves an issue by ID
func (s *SQLiteStorage) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	var issue types.Issue
	var closedAt sql.NullTime
	var estimatedMinutes sql.NullInt64
	var assignee sql.NullString
	var externalRef sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, updated_at, closed_at, external_ref
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

	if closedAt.Valid {
		issue.ClosedAt = &closedAt.Time
	}
	if estimatedMinutes.Valid {
		mins := int(estimatedMinutes.Int64)
		issue.EstimatedMinutes = &mins
	}
	if assignee.Valid {
		issue.Assignee = assignee.String
	}
	if externalRef.Valid {
		issue.ExternalRef = &externalRef.String
	}

	return &issue, nil
}

// Allowed fields for update to prevent SQL injection
var allowedUpdateFields = map[string]bool{
	"status":              true,
	"priority":            true,
	"title":               true,
	"assignee":            true,
	"description":         true,
	"design":              true,
	"acceptance_criteria": true,
	"notes":               true,
	"issue_type":          true,
	"estimated_minutes":   true,
	"external_ref":        true,
}

// UpdateIssue updates fields on an issue
func (s *SQLiteStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	// Get old issue for event
	oldIssue, err := s.GetIssue(ctx, id)
	if err != nil {
		return err
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", id)
	}

	// Build update query with validated field names
	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now()}

	for key, value := range updates {
		// Prevent SQL injection by validating field names
		if !allowedUpdateFields[key] {
			return fmt.Errorf("invalid field for update: %s", key)
		}

		// Validate field values
		switch key {
		case "priority":
			if priority, ok := value.(int); ok {
				if priority < 0 || priority > 4 {
					return fmt.Errorf("priority must be between 0 and 4 (got %d)", priority)
				}
			}
		case "status":
			if status, ok := value.(string); ok {
				if !types.Status(status).IsValid() {
					return fmt.Errorf("invalid status: %s", status)
				}
			}
		case "issue_type":
			if issueType, ok := value.(string); ok {
				if !types.IssueType(issueType).IsValid() {
					return fmt.Errorf("invalid issue type: %s", issueType)
				}
			}
		case "title":
			if title, ok := value.(string); ok {
				if len(title) == 0 || len(title) > 500 {
					return fmt.Errorf("title must be 1-500 characters")
				}
			}
		case "estimated_minutes":
			if mins, ok := value.(int); ok {
				if mins < 0 {
					return fmt.Errorf("estimated_minutes cannot be negative")
				}
			}
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = ?", key))
		args = append(args, value)
	}

	// Auto-manage closed_at when status changes (enforce invariant)
	if statusVal, ok := updates["status"]; ok {
		newStatus := statusVal.(string)
		if newStatus == string(types.StatusClosed) {
			// Changing to closed: ensure closed_at is set
			if _, hasClosedAt := updates["closed_at"]; !hasClosedAt {
				now := time.Now()
				updates["closed_at"] = now
				setClauses = append(setClauses, "closed_at = ?")
				args = append(args, now)
			}
		} else if oldIssue.Status == types.StatusClosed {
			// Changing from closed to something else: clear closed_at
			updates["closed_at"] = nil
			setClauses = append(setClauses, "closed_at = ?")
			args = append(args, nil)
		}
	}

	args = append(args, id)

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Update issue
	query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err = tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}

	// Record event
	oldData, err := json.Marshal(oldIssue)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		oldData = []byte(fmt.Sprintf(`{"id":"%s"}`, id))
	}
	newData, err := json.Marshal(updates)
	if err != nil {
		// Fall back to minimal description if marshaling fails
		newData = []byte(`{}`)
	}
	oldDataStr := string(oldData)
	newDataStr := string(newData)

	eventType := types.EventUpdated
	if statusVal, ok := updates["status"]; ok {
		if statusVal == string(types.StatusClosed) {
			eventType = types.EventClosed
		} else if oldIssue.Status == types.StatusClosed {
			// Reopening a closed issue
			eventType = types.EventReopened
		} else {
			eventType = types.EventStatusChanged
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, id, eventType, actor, oldDataStr, newDataStr)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return tx.Commit()
}

// CloseIssue closes an issue with a reason
func (s *SQLiteStorage) CloseIssue(ctx context.Context, id string, reason string, actor string) error {
	now := time.Now()

	// Update with special event handling
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		UPDATE issues SET status = ?, closed_at = ?, updated_at = ?
		WHERE id = ?
	`, types.StatusClosed, now, now, id)
	if err != nil {
		return fmt.Errorf("failed to close issue: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, id, types.EventClosed, actor, reason)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, id, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return tx.Commit()
}

// SearchIssues finds issues matching query and filters
func (s *SQLiteStorage) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	whereClauses := []string{}
	args := []interface{}{}

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}

	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, *filter.IssueType)
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = " LIMIT ?"
		args = append(args, filter.Limit)
	}

	querySQL := fmt.Sprintf(`
		SELECT id, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, updated_at, closed_at, external_ref
		FROM issues
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}
	defer rows.Close()

	return scanIssues(rows)
}

// SetConfig sets a configuration value
func (s *SQLiteStorage) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// GetConfig gets a configuration value
func (s *SQLiteStorage) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetMetadata sets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) SetMetadata(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// GetMetadata gets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// Close closes the database connection
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}
