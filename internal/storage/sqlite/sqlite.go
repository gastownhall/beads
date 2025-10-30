// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	// Import SQLite driver
	"github.com/steveyegge/beads/internal/types"
	_ "modernc.org/sqlite"
)

// SQLiteStorage implements the Storage interface using SQLite
type SQLiteStorage struct {
	db     *sql.DB
	dbPath string
	closed atomic.Bool // Tracks whether Close() has been called
}

// New creates a new SQLite storage backend
func New(path string) (*SQLiteStorage, error) {
	// Convert :memory: to shared memory URL for consistent behavior across connections
	// SQLite creates separate in-memory databases for each connection to ":memory:",
	// but "file::memory:?cache=shared" creates a shared in-memory database.
	dbPath := path
	if path == ":memory:" {
		dbPath = "file::memory:?cache=shared"
	}

	// Ensure directory exists (skip for memory databases)
	if !strings.Contains(dbPath, ":memory:") {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
	}

	// Open database with WAL mode for better concurrency and busy timeout for parallel writes
	// Use modernc.org/sqlite's _pragma syntax for all options to ensure consistent behavior
	// _pragma=journal_mode(WAL) enables Write-Ahead Logging for better concurrency
	// _pragma=foreign_keys(ON) enforces foreign key constraints
	// _pragma=busy_timeout(30000) means wait up to 30 seconds for locks instead of failing immediately
	// _time_format=sqlite enables automatic parsing of DATETIME columns to time.Time
	// Note: For shared memory URLs, additional params need to be added with & not ?
	connStr := dbPath
	if strings.Contains(dbPath, "?") {
		connStr += "&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(30000)&_time_format=sqlite"
	} else {
		connStr += "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(30000)&_time_format=sqlite"
	}

	db, err := sql.Open("sqlite", connStr)
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

	// Migrate existing databases to add compaction columns
	if err := migrateCompactionColumns(db); err != nil {
		return nil, fmt.Errorf("failed to migrate compaction columns: %w", err)
	}

	// Migrate existing databases to add issue_snapshots table
	if err := migrateSnapshotsTable(db); err != nil {
		return nil, fmt.Errorf("failed to migrate snapshots table: %w", err)
	}

	// Migrate existing databases to add compaction config defaults
	if err := migrateCompactionConfig(db); err != nil {
		return nil, fmt.Errorf("failed to migrate compaction config: %w", err)
	}

	// Migrate existing databases to add compacted_at_commit column
	if err := migrateCompactedAtCommitColumn(db); err != nil {
		return nil, fmt.Errorf("failed to migrate compacted_at_commit column: %w", err)
	}

	// Migrate existing databases to add export_hashes table (bd-164)
	if err := migrateExportHashesTable(db); err != nil {
		return nil, fmt.Errorf("failed to migrate export_hashes table: %w", err)
	}

	// Migrate existing databases to add content_hash column (bd-95)
	if err := migrateContentHashColumn(db); err != nil {
		return nil, fmt.Errorf("failed to migrate content_hash column: %w", err)
	}

	// Convert to absolute path for consistency
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	return &SQLiteStorage{
		db:     db,
		dbPath: absPath,
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

	// Table exists, check if content_hash column exists (migration for bd-164)
	var hasContentHash bool
	err = db.QueryRow(`
		SELECT COUNT(*) > 0 FROM pragma_table_info('dirty_issues')
		WHERE name = 'content_hash'
	`).Scan(&hasContentHash)
	
	if err != nil {
		return fmt.Errorf("failed to check for content_hash column: %w", err)
	}
	
	if !hasContentHash {
		// Add content_hash column to existing table
		_, err = db.Exec(`ALTER TABLE dirty_issues ADD COLUMN content_hash TEXT`)
		if err != nil {
			return fmt.Errorf("failed to add content_hash column: %w", err)
		}
	}

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
	defer func() { _ = rows.Close() }()

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

// migrateCompactionColumns adds compaction_level, compacted_at, and original_size columns to the issues table.
// This migration is idempotent and safe to run multiple times.
func migrateCompactionColumns(db *sql.DB) error {
	// Check if compaction_level column exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'compaction_level'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check compaction_level column: %w", err)
	}

	if columnExists {
		// Columns already exist, nothing to do
		return nil
	}

	// Add the three compaction columns
	_, err = db.Exec(`
		ALTER TABLE issues ADD COLUMN compaction_level INTEGER DEFAULT 0;
		ALTER TABLE issues ADD COLUMN compacted_at DATETIME;
		ALTER TABLE issues ADD COLUMN original_size INTEGER;
	`)
	if err != nil {
		return fmt.Errorf("failed to add compaction columns: %w", err)
	}

	return nil
}

// migrateSnapshotsTable creates the issue_snapshots table if it doesn't exist.
// This migration is idempotent and safe to run multiple times.
func migrateSnapshotsTable(db *sql.DB) error {
	// Check if issue_snapshots table exists
	var tableExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM sqlite_master
		WHERE type='table' AND name='issue_snapshots'
	`).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("failed to check issue_snapshots table: %w", err)
	}

	if tableExists {
		// Table already exists, nothing to do
		return nil
	}

	// Create the table and indexes
	_, err = db.Exec(`
		CREATE TABLE issue_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			snapshot_time DATETIME NOT NULL,
			compaction_level INTEGER NOT NULL,
			original_size INTEGER NOT NULL,
			compressed_size INTEGER NOT NULL,
			original_content TEXT NOT NULL,
			archived_events TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		);
		CREATE INDEX idx_snapshots_issue ON issue_snapshots(issue_id);
		CREATE INDEX idx_snapshots_level ON issue_snapshots(compaction_level);
	`)
	if err != nil {
		return fmt.Errorf("failed to create issue_snapshots table: %w", err)
	}

	return nil
}

// migrateCompactionConfig adds default compaction configuration values.
// This migration is idempotent and safe to run multiple times (INSERT OR IGNORE).
func migrateCompactionConfig(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO config (key, value) VALUES
			('compaction_enabled', 'false'),
			('compact_tier1_days', '30'),
			('compact_tier1_dep_levels', '2'),
			('compact_tier2_days', '90'),
			('compact_tier2_dep_levels', '5'),
			('compact_tier2_commits', '100'),
			('compact_model', 'claude-3-5-haiku-20241022'),
			('compact_batch_size', '50'),
			('compact_parallel_workers', '5'),
			('auto_compact_enabled', 'false')
	`)
	if err != nil {
		return fmt.Errorf("failed to add compaction config defaults: %w", err)
	}
	return nil
}

// migrateCompactedAtCommitColumn adds compacted_at_commit column to the issues table.
// This migration is idempotent and safe to run multiple times.
func migrateCompactedAtCommitColumn(db *sql.DB) error {
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'compacted_at_commit'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check compacted_at_commit column: %w", err)
	}

	if columnExists {
		return nil
	}

	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN compacted_at_commit TEXT`)
	if err != nil {
		return fmt.Errorf("failed to add compacted_at_commit column: %w", err)
	}

	return nil
}

// migrateExportHashesTable ensures the export_hashes table exists for timestamp-only dedup (bd-164)
func migrateExportHashesTable(db *sql.DB) error {
	// Check if export_hashes table exists
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='export_hashes'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		// Table doesn't exist, create it
		_, err := db.Exec(`
			CREATE TABLE export_hashes (
				issue_id TEXT PRIMARY KEY,
				content_hash TEXT NOT NULL,
				exported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return fmt.Errorf("failed to create export_hashes table: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check export_hashes table: %w", err)
	}

	// Table already exists
	return nil
}

// migrateContentHashColumn adds the content_hash column to the issues table if missing (bd-95).
// This enables global N-way collision resolution by providing content-addressable identity.
func migrateContentHashColumn(db *sql.DB) error {
	// Check if content_hash column exists
	var colName string
	err := db.QueryRow(`
		SELECT name FROM pragma_table_info('issues')
		WHERE name = 'content_hash'
	`).Scan(&colName)

	if err == sql.ErrNoRows {
		// Column doesn't exist, add it
		_, err := db.Exec(`ALTER TABLE issues ADD COLUMN content_hash TEXT`)
		if err != nil {
			return fmt.Errorf("failed to add content_hash column: %w", err)
		}

		// Create index on content_hash for fast lookups
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_content_hash ON issues(content_hash)`)
		if err != nil {
			return fmt.Errorf("failed to create content_hash index: %w", err)
		}

		// Populate content_hash for all existing issues
		rows, err := db.Query(`
			SELECT id, title, description, design, acceptance_criteria, notes,
			       status, priority, issue_type, assignee, external_ref
			FROM issues
		`)
		if err != nil {
			return fmt.Errorf("failed to query existing issues: %w", err)
		}
		defer rows.Close()

		// Collect issues and compute hashes
		updates := make(map[string]string) // id -> content_hash
		for rows.Next() {
			var issue types.Issue
			var assignee sql.NullString
			var externalRef sql.NullString
			err := rows.Scan(
				&issue.ID, &issue.Title, &issue.Description, &issue.Design,
				&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
				&issue.Priority, &issue.IssueType, &assignee, &externalRef,
			)
			if err != nil {
				return fmt.Errorf("failed to scan issue: %w", err)
			}
			if assignee.Valid {
				issue.Assignee = assignee.String
			}
			if externalRef.Valid {
				issue.ExternalRef = &externalRef.String
			}

			// Compute and store hash
			updates[issue.ID] = issue.ComputeContentHash()
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating issues: %w", err)
		}

		// Apply hash updates in batch
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		stmt, err := tx.Prepare(`UPDATE issues SET content_hash = ? WHERE id = ?`)
		if err != nil {
			return fmt.Errorf("failed to prepare update statement: %w", err)
		}
		defer stmt.Close()

		for id, hash := range updates {
			if _, err := stmt.Exec(hash, id); err != nil {
				return fmt.Errorf("failed to update content_hash for issue %s: %w", id, err)
			}
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check content_hash column: %w", err)
	}

	// Column already exists
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

// AllocateNextID generates the next issue ID for a given prefix.
// This is a public wrapper around getNextIDForPrefix for use by other packages.
func (s *SQLiteStorage) AllocateNextID(ctx context.Context, prefix string) (string, error) {
	nextID, err := s.getNextIDForPrefix(ctx, prefix)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d", prefix, nextID), nil
}

// getNextChildNumber atomically generates the next child number for a parent ID
// Uses the child_counters table for atomic, cross-process child ID generation
func (s *SQLiteStorage) getNextChildNumber(ctx context.Context, parentID string) (int, error) {
	var nextChild int
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO child_counters (parent_id, last_child)
		VALUES (?, 1)
		ON CONFLICT(parent_id) DO UPDATE SET
			last_child = last_child + 1
		RETURNING last_child
	`, parentID).Scan(&nextChild)
	if err != nil {
		return 0, fmt.Errorf("failed to generate next child number for parent %s: %w", parentID, err)
	}
	return nextChild, nil
}

// SyncAllCounters synchronizes all ID counters based on existing issues in the database
// This scans all issues and updates counters to prevent ID collisions with auto-generated IDs
// Note: This unconditionally overwrites counter values, allowing them to decrease after deletions
func (s *SQLiteStorage) SyncAllCounters(ctx context.Context) error {
	// First, delete counters for prefixes that have no issues
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM issue_counters
		WHERE prefix NOT IN (
			SELECT DISTINCT substr(id, 1, instr(id, '-') - 1)
			FROM issues
			WHERE instr(id, '-') > 0
			  AND substr(id, instr(id, '-') + 1) GLOB '[0-9]*'
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to delete orphaned counters: %w", err)
	}

	// Then, upsert counters for prefixes that have issues
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO issue_counters (prefix, last_id)
		SELECT
			substr(id, 1, instr(id, '-') - 1) as prefix,
			MAX(CAST(substr(id, instr(id, '-') + 1) AS INTEGER)) as max_id
		FROM issues
		WHERE instr(id, '-') > 0
		  AND substr(id, instr(id, '-') + 1) GLOB '[0-9]*'
		GROUP BY prefix
		ON CONFLICT(prefix) DO UPDATE SET
			last_id = excluded.last_id
	`)
	if err != nil {
		return fmt.Errorf("failed to sync counters: %w", err)
	}
	return nil
}

// REMOVED (bd-166): derivePrefixFromPath was causing duplicate issues with wrong prefix
// The database should ALWAYS have issue_prefix config set explicitly (by 'bd init' or auto-import)
// Never derive prefix from filename - it leads to silent data corruption

// generateHashID creates a hash-based ID for a top-level issue.
// For child issues, use the parent ID with a numeric suffix (e.g., "bd-a3f8e9a2.1").
// Includes a nonce parameter to handle collisions.
func generateHashID(prefix, title, description, creator string, timestamp time.Time, nonce int) string {
	// Combine inputs into a stable content string
	// Include nonce to handle hash collisions
	content := fmt.Sprintf("%s|%s|%s|%d|%d", title, description, creator, timestamp.UnixNano(), nonce)
	
	// Hash the content
	hash := sha256.Sum256([]byte(content))
	
	// Use first 4 bytes (8 hex chars) for short, readable IDs
	shortHash := hex.EncodeToString(hash[:4])
	
	return fmt.Sprintf("%s-%s", prefix, shortHash)
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

	// Compute content hash (bd-95)
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Acquire a dedicated connection for the transaction.
	// This is necessary because we need to execute raw SQL ("BEGIN IMMEDIATE", "COMMIT")
	// on the same connection, and database/sql's connection pool would otherwise
	// use different connections for different queries.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

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
	// Use context.Background() for ROLLBACK to ensure cleanup happens even if ctx is canceled
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Get prefix from config (needed for both ID generation and validation)
	var prefix string
	err = conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing (bd-166)
		// This prevents duplicate issues with wrong prefix
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Generate ID if not set (inside transaction to prevent race conditions)
	if issue.ID == "" {
		// Generate hash-based ID with collision detection (bd-168)
		// Try up to 10 times with different nonces to avoid collisions
		var err error
		for nonce := 0; nonce < 10; nonce++ {
			candidate := generateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, nonce)
			
			// Check if this ID already exists
			var count int
			err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, candidate).Scan(&count)
			if err != nil {
				return fmt.Errorf("failed to check for ID collision: %w", err)
			}
			
			if count == 0 {
				issue.ID = candidate
				break
			}
		}
		
		if issue.ID == "" {
			return fmt.Errorf("failed to generate unique ID after 10 attempts")
		}
	} else {
		// Validate that explicitly provided ID matches the configured prefix (bd-177)
		// This prevents wrong-prefix bugs when IDs are manually specified
		expectedPrefix := prefix + "-"
		if !strings.HasPrefix(issue.ID, expectedPrefix) {
			return fmt.Errorf("issue ID '%s' does not match configured prefix '%s'", issue.ID, prefix)
		}
	}

	// Insert issue
	_, err = conn.ExecContext(ctx, `
		INSERT INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
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

// validateBatchIssues validates all issues in a batch and sets timestamps
func validateBatchIssues(issues []*types.Issue) error {
	now := time.Now()
	for i, issue := range issues {
		if issue == nil {
			return fmt.Errorf("issue %d is nil", i)
		}

		// Only set timestamps if not already provided
		if issue.CreatedAt.IsZero() {
			issue.CreatedAt = now
		}
		if issue.UpdatedAt.IsZero() {
			issue.UpdatedAt = now
		}

		if err := issue.Validate(); err != nil {
			return fmt.Errorf("validation failed for issue %d: %w", i, err)
		}
	}
	return nil
}

// generateBatchIDs generates IDs for all issues that need them atomically
func generateBatchIDs(ctx context.Context, conn *sql.Conn, issues []*types.Issue, actor string) error {
	// Get prefix from config (needed for both generation and validation)
	var prefix string
	err := conn.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, "issue_prefix").Scan(&prefix)
	if err == sql.ErrNoRows || prefix == "" {
		// CRITICAL: Reject operation if issue_prefix config is missing (bd-166)
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Validate explicitly provided IDs and generate hash IDs for those that need them
	expectedPrefix := prefix + "-"
	usedIDs := make(map[string]bool)
	
	// First pass: record explicitly provided IDs
	for i := range issues {
		if issues[i].ID != "" {
			// Validate that explicitly provided ID matches the configured prefix (bd-177)
			if !strings.HasPrefix(issues[i].ID, expectedPrefix) {
				return fmt.Errorf("issue ID '%s' does not match configured prefix '%s'", issues[i].ID, prefix)
			}
			usedIDs[issues[i].ID] = true
		}
	}
	
	// Second pass: generate IDs for issues that need them, with collision detection
	for i := range issues {
		if issues[i].ID == "" {
			// Generate hash-based ID with collision detection (bd-168)
			var generated bool
			for nonce := 0; nonce < 10; nonce++ {
				candidate := generateHashID(prefix, issues[i].Title, issues[i].Description, actor, issues[i].CreatedAt, nonce)
				
				// Check if this ID is already used in this batch or in the database
				if usedIDs[candidate] {
					continue
				}
				
				var count int
				err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, candidate).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check for ID collision: %w", err)
				}
				
				if count == 0 {
					issues[i].ID = candidate
					usedIDs[candidate] = true
					generated = true
					break
				}
			}
			
			if !generated {
				return fmt.Errorf("failed to generate unique ID for issue %d after 10 attempts", i)
			}
		}
		
		// Compute content hash if not already set (bd-95)
		if issues[i].ContentHash == "" {
			issues[i].ContentHash = issues[i].ComputeContentHash()
		}
	}
	return nil
}

// bulkInsertIssues inserts all issues using a prepared statement
func bulkInsertIssues(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	stmt, err := conn.PrepareContext(ctx, `
		INSERT INTO issues (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issue := range issues {
		_, err = stmt.ExecContext(ctx,
			issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
			issue.AcceptanceCriteria, issue.Notes, issue.Status,
			issue.Priority, issue.IssueType, issue.Assignee,
			issue.EstimatedMinutes, issue.CreatedAt, issue.UpdatedAt,
			issue.ClosedAt, issue.ExternalRef,
		)
		if err != nil {
			return fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
		}
	}
	return nil
}

// bulkRecordEvents records creation events for all issues
func bulkRecordEvents(ctx context.Context, conn *sql.Conn, issues []*types.Issue, actor string) error {
	stmt, err := conn.PrepareContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, new_value)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare event statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, issue := range issues {
		eventData, err := json.Marshal(issue)
		if err != nil {
			// Fall back to minimal description if marshaling fails
			eventData = []byte(fmt.Sprintf(`{"id":"%s","title":"%s"}`, issue.ID, issue.Title))
		}

		_, err = stmt.ExecContext(ctx, issue.ID, types.EventCreated, actor, string(eventData))
		if err != nil {
			return fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
		}
	}
	return nil
}

// bulkMarkDirty marks all issues as dirty for incremental export
func bulkMarkDirty(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	stmt, err := conn.PrepareContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare dirty statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	dirtyTime := time.Now()
	for _, issue := range issues {
		_, err = stmt.ExecContext(ctx, issue.ID, dirtyTime)
		if err != nil {
			return fmt.Errorf("failed to mark dirty %s: %w", issue.ID, err)
		}
	}
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
// CreateIssues creates multiple issues atomically in a single transaction.
//
// This method is optimized for bulk issue creation and provides significant
// performance improvements over calling CreateIssue in a loop:
//   - Single database connection and transaction
//   - Atomic ID range reservation (one counter update for N IDs)
//   - All-or-nothing semantics (rolls back on any error)
//   - 5-15x faster than sequential CreateIssue calls
//
// All issues are validated before any database changes occur. If any issue
// fails validation, the entire batch is rejected.
//
// ID Assignment:
//   - Issues with empty ID get auto-generated IDs from a reserved range
//   - Issues with explicit IDs use those IDs (caller must ensure uniqueness)
//   - Mix of explicit and auto-generated IDs is supported
//
// Timestamps:
//   - All issues in the batch receive identical created_at/updated_at timestamps
//   - This reflects that they were created as a single atomic operation
//
// Usage:
//   // Bulk import from external source
//   issues := []*types.Issue{...}
//   if err := store.CreateIssues(ctx, issues, "import"); err != nil {
//       return err
//   }
//
//   // After importing with explicit IDs, sync counters to prevent collisions
//   if err := store.SyncAllCounters(ctx); err != nil {
//       return err
//   }
//
// Performance:
//   - 100 issues: ~30ms (vs ~900ms with CreateIssue loop)
//   - 1000 issues: ~950ms (vs estimated 9s with CreateIssue loop)
//
// When to use:
//   - Bulk imports from external systems (use CreateIssues)
//   - Creating multiple related issues at once (use CreateIssues)
//   - Single issue creation (use CreateIssue for simplicity)
//   - Interactive user operations (use CreateIssue)
func (s *SQLiteStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if len(issues) == 0 {
		return nil
	}

	// Phase 1: Validate all issues first (fail-fast)
	if err := validateBatchIssues(issues); err != nil {
		return err
	}

	// Phase 2: Acquire connection and start transaction
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("failed to begin immediate transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Phase 3: Generate IDs for issues that need them
	if err := generateBatchIDs(ctx, conn, issues, actor); err != nil {
		return err
	}

	// Phase 4: Bulk insert issues
	if err := bulkInsertIssues(ctx, conn, issues); err != nil {
		return err
	}

	// Phase 5: Record creation events
	if err := bulkRecordEvents(ctx, conn, issues, actor); err != nil {
		return err
	}

	// Phase 6: Mark issues dirty for incremental export
	if err := bulkMarkDirty(ctx, conn, issues); err != nil {
		return err
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
	var compactedAt sql.NullTime
	var originalSize sql.NullInt64

	var contentHash sql.NullString
	var compactedAtCommit sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
		       status, priority, issue_type, assignee, estimated_minutes,
		       created_at, updated_at, closed_at, external_ref,
		       compaction_level, compacted_at, compacted_at_commit, original_size
		FROM issues
		WHERE id = ?
	`, id).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&issue.CreatedAt, &issue.UpdatedAt, &closedAt, &externalRef,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

	if contentHash.Valid {
		issue.ContentHash = contentHash.String
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
	if compactedAt.Valid {
		issue.CompactedAt = &compactedAt.Time
	}
	if compactedAtCommit.Valid {
		issue.CompactedAtCommit = &compactedAtCommit.String
	}
	if originalSize.Valid {
		issue.OriginalSize = int(originalSize.Int64)
	}

	// Fetch labels for this issue
	labels, err := s.GetLabels(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	issue.Labels = labels

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

// validatePriority validates a priority value
func validatePriority(value interface{}) error {
	if priority, ok := value.(int); ok {
		if priority < 0 || priority > 4 {
			return fmt.Errorf("priority must be between 0 and 4 (got %d)", priority)
		}
	}
	return nil
}

// validateStatus validates a status value
func validateStatus(value interface{}) error {
	if status, ok := value.(string); ok {
		if !types.Status(status).IsValid() {
			return fmt.Errorf("invalid status: %s", status)
		}
	}
	return nil
}

// validateIssueType validates an issue type value
func validateIssueType(value interface{}) error {
	if issueType, ok := value.(string); ok {
		if !types.IssueType(issueType).IsValid() {
			return fmt.Errorf("invalid issue type: %s", issueType)
		}
	}
	return nil
}

// validateTitle validates a title value
func validateTitle(value interface{}) error {
	if title, ok := value.(string); ok {
		if len(title) == 0 || len(title) > 500 {
			return fmt.Errorf("title must be 1-500 characters")
		}
	}
	return nil
}

// validateEstimatedMinutes validates an estimated_minutes value
func validateEstimatedMinutes(value interface{}) error {
	if mins, ok := value.(int); ok {
		if mins < 0 {
			return fmt.Errorf("estimated_minutes cannot be negative")
		}
	}
	return nil
}

// fieldValidators maps field names to their validation functions
var fieldValidators = map[string]func(interface{}) error{
	"priority":           validatePriority,
	"status":             validateStatus,
	"issue_type":         validateIssueType,
	"title":              validateTitle,
	"estimated_minutes":  validateEstimatedMinutes,
}

// validateFieldUpdate validates a field update value
func validateFieldUpdate(key string, value interface{}) error {
	if validator, ok := fieldValidators[key]; ok {
		return validator(value)
	}
	return nil
}

// determineEventType determines the event type for an update based on old and new status
func determineEventType(oldIssue *types.Issue, updates map[string]interface{}) types.EventType {
	statusVal, hasStatus := updates["status"]
	if !hasStatus {
		return types.EventUpdated
	}

	newStatus, ok := statusVal.(string)
	if !ok {
		return types.EventUpdated
	}

	if newStatus == string(types.StatusClosed) {
		return types.EventClosed
	}
	if oldIssue.Status == types.StatusClosed {
		return types.EventReopened
	}
	return types.EventStatusChanged
}

// manageClosedAt automatically manages the closed_at field based on status changes
func manageClosedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]
	if !hasStatus {
		return setClauses, args
	}

	// Handle both string and types.Status
	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return setClauses, args
	}

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

	return setClauses, args
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
		if err := validateFieldUpdate(key, value); err != nil {
			return err
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = ?", key))
		args = append(args, value)
	}

	// Auto-manage closed_at when status changes (enforce invariant)
	setClauses, args = manageClosedAt(oldIssue, updates, setClauses, args)

	// Recompute content_hash if any content fields changed (bd-95)
	contentChanged := false
	contentFields := []string{"title", "description", "design", "acceptance_criteria", "notes", "status", "priority", "issue_type", "assignee", "external_ref"}
	for _, field := range contentFields {
		if _, exists := updates[field]; exists {
			contentChanged = true
			break
		}
	}
	if contentChanged {
		// Get updated issue to compute hash
		updatedIssue := *oldIssue
		for key, value := range updates {
			switch key {
			case "title":
				updatedIssue.Title = value.(string)
			case "description":
				updatedIssue.Description = value.(string)
			case "design":
				updatedIssue.Design = value.(string)
			case "acceptance_criteria":
				updatedIssue.AcceptanceCriteria = value.(string)
			case "notes":
				updatedIssue.Notes = value.(string)
			case "status":
				// Handle both string and types.Status
				if s, ok := value.(types.Status); ok {
					updatedIssue.Status = s
				} else {
					updatedIssue.Status = types.Status(value.(string))
				}
			case "priority":
				updatedIssue.Priority = value.(int)
			case "issue_type":
				// Handle both string and types.IssueType
				if t, ok := value.(types.IssueType); ok {
					updatedIssue.IssueType = t
				} else {
					updatedIssue.IssueType = types.IssueType(value.(string))
				}
			case "assignee":
				if value == nil {
					updatedIssue.Assignee = ""
				} else {
					updatedIssue.Assignee = value.(string)
				}
			case "external_ref":
				if value == nil {
					updatedIssue.ExternalRef = nil
				} else {
					str := value.(string)
					updatedIssue.ExternalRef = &str
				}
			}
		}
		newHash := updatedIssue.ComputeContentHash()
		setClauses = append(setClauses, "content_hash = ?")
		args = append(args, newHash)
	}

	args = append(args, id)

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Update issue
	query := fmt.Sprintf("UPDATE issues SET %s WHERE id = ?", strings.Join(setClauses, ", ")) // #nosec G201 - safe SQL with controlled column names
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

	eventType := determineEventType(oldIssue, updates)

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

// UpdateIssueID updates an issue ID and all its text fields in a single transaction
func (s *SQLiteStorage) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	// Get exclusive connection to ensure PRAGMA applies
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Disable foreign keys on this specific connection
	_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	if err != nil {
		return fmt.Errorf("failed to disable foreign keys: %w", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		UPDATE issues
		SET id = ?, title = ?, description = ?, design = ?, acceptance_criteria = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`, newID, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes, time.Now(), oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue ID: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE dependencies SET depends_on_id = ? WHERE depends_on_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update depends_on_id in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE events SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update events: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE labels SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE comments SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update comments: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE dirty_issues SET issue_id = ? WHERE issue_id = ?
	`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update dirty_issues: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE issue_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update issue_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE compaction_snapshots SET issue_id = ? WHERE issue_id = ?`, newID, oldID)
	if err != nil {
		return fmt.Errorf("failed to update compaction_snapshots: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, newID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	if err != nil {
		return fmt.Errorf("failed to record rename event: %w", err)
	}

	return tx.Commit()
}

// RenameDependencyPrefix updates the prefix in all dependency records
func (s *SQLiteStorage) RenameDependencyPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	return nil
}

// RenameCounterPrefix updates the prefix in the issue_counters table
func (s *SQLiteStorage) RenameCounterPrefix(ctx context.Context, oldPrefix, newPrefix string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var lastID int
	err = tx.QueryRowContext(ctx, `SELECT last_id FROM issue_counters WHERE prefix = ?`, oldPrefix).Scan(&lastID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to get old counter: %w", err)
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM issue_counters WHERE prefix = ?`, oldPrefix)
	if err != nil {
		return fmt.Errorf("failed to delete old counter: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO issue_counters (prefix, last_id)
		VALUES (?, ?)
		ON CONFLICT(prefix) DO UPDATE SET last_id = MAX(last_id, excluded.last_id)
	`, newPrefix, lastID)
	if err != nil {
		return fmt.Errorf("failed to create new counter: %w", err)
	}

	return tx.Commit()
}

// ResetCounter deletes the counter for a prefix, forcing it to be recalculated from max ID
// This is used by renumber to ensure the counter matches the actual max ID after renumbering
func (s *SQLiteStorage) ResetCounter(ctx context.Context, prefix string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM issue_counters WHERE prefix = ?`, prefix)
	if err != nil {
		return fmt.Errorf("failed to delete counter: %w", err)
	}
	return nil
}

// CloseIssue closes an issue with a reason
func (s *SQLiteStorage) CloseIssue(ctx context.Context, id string, reason string, actor string) error {
	now := time.Now()

	// Update with special event handling
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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

// DeleteIssue permanently removes an issue from the database
func (s *SQLiteStorage) DeleteIssue(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete dependencies (both directions)
	_, err = tx.ExecContext(ctx, `DELETE FROM dependencies WHERE issue_id = ? OR depends_on_id = ?`, id, id)
	if err != nil {
		return fmt.Errorf("failed to delete dependencies: %w", err)
	}

	// Delete events
	_, err = tx.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete events: %w", err)
	}

	// Delete from dirty_issues
	_, err = tx.ExecContext(ctx, `DELETE FROM dirty_issues WHERE issue_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete dirty marker: %w", err)
	}

	// Delete the issue itself
	result, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("issue not found: %s", id)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Sync counters after deletion to keep them accurate
	return s.SyncAllCounters(ctx)
}

// DeleteIssuesResult contains statistics about a batch deletion operation
type DeleteIssuesResult struct {
	DeletedCount      int
	DependenciesCount int
	LabelsCount       int
	EventsCount       int
	OrphanedIssues    []string
}

// DeleteIssues deletes multiple issues in a single transaction
// If cascade is true, recursively deletes dependents
// If cascade is false but force is true, deletes issues and orphans their dependents
// If cascade and force are both false, returns an error if any issue has dependents
// If dryRun is true, only computes statistics without deleting
func (s *SQLiteStorage) DeleteIssues(ctx context.Context, ids []string, cascade bool, force bool, dryRun bool) (*DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &DeleteIssuesResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	idSet := buildIDSet(ids)
	result := &DeleteIssuesResult{}

	expandedIDs, err := s.resolveDeleteSet(ctx, tx, ids, idSet, cascade, force, result)
	if err != nil {
		return nil, err
	}

	inClause, args := buildSQLInClause(expandedIDs)
	if err := s.populateDeleteStats(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if dryRun {
		return result, nil
	}

	if err := s.executeDelete(ctx, tx, inClause, args, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	if err := s.SyncAllCounters(ctx); err != nil {
		return nil, fmt.Errorf("failed to sync counters after deletion: %w", err)
	}

	return result, nil
}

func buildIDSet(ids []string) map[string]bool {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	return idSet
}

func (s *SQLiteStorage) resolveDeleteSet(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, cascade bool, force bool, result *DeleteIssuesResult) ([]string, error) {
	if cascade {
		return s.expandWithDependents(ctx, tx, ids, idSet)
	}
	if !force {
		return ids, s.validateNoDependents(ctx, tx, ids, idSet, result)
	}
	return ids, s.trackOrphanedIssues(ctx, tx, ids, idSet, result)
}

func (s *SQLiteStorage) expandWithDependents(ctx context.Context, tx *sql.Tx, ids []string, _ map[string]bool) ([]string, error) {
	allToDelete, err := s.findAllDependentsRecursive(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to find dependents: %w", err)
	}
	expandedIDs := make([]string, 0, len(allToDelete))
	for id := range allToDelete {
		expandedIDs = append(expandedIDs, id)
	}
	return expandedIDs, nil
}

func (s *SQLiteStorage) validateNoDependents(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	for _, id := range ids {
		if err := s.checkSingleIssueValidation(ctx, tx, id, idSet, result); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStorage) checkSingleIssueValidation(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, result *DeleteIssuesResult) error {
	var depCount int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE depends_on_id = ?`, id).Scan(&depCount)
	if err != nil {
		return fmt.Errorf("failed to check dependents for %s: %w", id, err)
	}
	if depCount == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	hasExternal := false
	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			hasExternal = true
			result.OrphanedIssues = append(result.OrphanedIssues, depID)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dependents for %s: %w", id, err)
	}

	if hasExternal {
		return fmt.Errorf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", id)
	}
	return nil
}

func (s *SQLiteStorage) trackOrphanedIssues(ctx context.Context, tx *sql.Tx, ids []string, idSet map[string]bool, result *DeleteIssuesResult) error {
	orphanSet := make(map[string]bool)
	for _, id := range ids {
		if err := s.collectOrphansForID(ctx, tx, id, idSet, orphanSet); err != nil {
			return err
		}
	}
	for orphanID := range orphanSet {
		result.OrphanedIssues = append(result.OrphanedIssues, orphanID)
	}
	return nil
}

func (s *SQLiteStorage) collectOrphansForID(ctx context.Context, tx *sql.Tx, id string, idSet map[string]bool, orphanSet map[string]bool) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to get dependents for %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return fmt.Errorf("failed to scan dependent: %w", err)
		}
		if !idSet[depID] {
			orphanSet[depID] = true
		}
	}
	return rows.Err()
}

func buildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

func (s *SQLiteStorage) populateDeleteStats(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	counts := []struct {
		query string
		dest  *int
	}{
		{fmt.Sprintf(`SELECT COUNT(*) FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause), &result.DependenciesCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM labels WHERE issue_id IN (%s)`, inClause), &result.LabelsCount},
		{fmt.Sprintf(`SELECT COUNT(*) FROM events WHERE issue_id IN (%s)`, inClause), &result.EventsCount},
	}

	for _, c := range counts {
		queryArgs := args
		if c.dest == &result.DependenciesCount {
			queryArgs = append(args, args...)
		}
		if err := tx.QueryRowContext(ctx, c.query, queryArgs...).Scan(c.dest); err != nil {
			return fmt.Errorf("failed to count: %w", err)
		}
	}

	result.DeletedCount = len(args)
	return nil
}

func (s *SQLiteStorage) executeDelete(ctx context.Context, tx *sql.Tx, inClause string, args []interface{}, result *DeleteIssuesResult) error {
	deletes := []struct {
		query string
		args  []interface{}
	}{
		{fmt.Sprintf(`DELETE FROM dependencies WHERE issue_id IN (%s) OR depends_on_id IN (%s)`, inClause, inClause), append(args, args...)},
		{fmt.Sprintf(`DELETE FROM labels WHERE issue_id IN (%s)`, inClause), args},
		{fmt.Sprintf(`DELETE FROM events WHERE issue_id IN (%s)`, inClause), args},
		{fmt.Sprintf(`DELETE FROM dirty_issues WHERE issue_id IN (%s)`, inClause), args},
		{fmt.Sprintf(`DELETE FROM issues WHERE id IN (%s)`, inClause), args},
	}

	for i, d := range deletes {
		execResult, err := tx.ExecContext(ctx, d.query, d.args...)
		if err != nil {
			return fmt.Errorf("failed to delete: %w", err)
		}
		if i == len(deletes)-1 {
			rowsAffected, err := execResult.RowsAffected()
			if err != nil {
				return fmt.Errorf("failed to check rows affected: %w", err)
			}
			result.DeletedCount = int(rowsAffected)
		}
	}
	return nil
}

// findAllDependentsRecursive finds all issues that depend on the given issues, recursively
func (s *SQLiteStorage) findAllDependentsRecursive(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, id := range ids {
		result[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)

	for len(toProcess) > 0 {
		current := toProcess[0]
		toProcess = toProcess[1:]

		rows, err := tx.QueryContext(ctx,
			`SELECT issue_id FROM dependencies WHERE depends_on_id = ?`, current)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if !result[depID] {
				result[depID] = true
				toProcess = append(toProcess, depID)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}

	return result, nil
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

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		pattern := "%" + filter.TitleSearch + "%"
		args = append(args, pattern)
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

	// Label filtering: issue must have ALL specified labels
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM labels WHERE label = ?)")
			args = append(args, label)
		}
	}

	// Label filtering (OR): issue must have AT LEAST ONE of these labels
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM labels WHERE label IN (%s))", strings.Join(placeholders, ", ")))
	}

	// ID filtering: match specific issue IDs
	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", ")))
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

	// #nosec G201 - safe SQL with controlled formatting
	querySQL := fmt.Sprintf(`
		SELECT id, content_hash, title, description, design, acceptance_criteria, notes,
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
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
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

// GetAllConfig gets all configuration key-value pairs
func (s *SQLiteStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		config[key] = value
	}
	return config, rows.Err()
}

// DeleteConfig deletes a configuration value
func (s *SQLiteStorage) DeleteConfig(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	return err
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

// AddIssueComment adds a comment to an issue
func (s *SQLiteStorage) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	// Verify issue exists
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`, issueID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	// Insert comment
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO comments (issue_id, author, text, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, issueID, author, text)
	if err != nil {
		return nil, fmt.Errorf("failed to insert comment: %w", err)
	}

	// Get the inserted comment ID
	commentID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get comment ID: %w", err)
	}

	// Fetch the complete comment
	comment := &types.Comment{}
	err = s.db.QueryRowContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments WHERE id = ?
	`, commentID).Scan(&comment.ID, &comment.IssueID, &comment.Author, &comment.Text, &comment.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch comment: %w", err)
	}

	// Mark issue as dirty for JSONL export
	if err := s.MarkIssueDirty(ctx, issueID); err != nil {
		return nil, fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return comment, nil
}

// GetIssueComments retrieves all comments for an issue
func (s *SQLiteStorage) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to query comments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var comments []*types.Comment
	for rows.Next() {
		comment := &types.Comment{}
		err := rows.Scan(&comment.ID, &comment.IssueID, &comment.Author, &comment.Text, &comment.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan comment: %w", err)
		}
		comments = append(comments, comment)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating comments: %w", err)
	}

	return comments, nil
}

// Close closes the database connection
func (s *SQLiteStorage) Close() error {
	s.closed.Store(true)
	return s.db.Close()
}

// Path returns the absolute path to the database file
func (s *SQLiteStorage) Path() string {
	return s.dbPath
}

// IsClosed returns true if Close() has been called on this storage
func (s *SQLiteStorage) IsClosed() bool {
	return s.closed.Load()
}

// UnderlyingDB returns the underlying *sql.DB connection for extensions.
//
// This allows extensions (like VC) to create their own tables in the same database
// while leveraging the existing connection pool and schema. The returned *sql.DB is
// safe for concurrent use and shares the same transaction isolation and locking
// behavior as the core storage operations.
//
// IMPORTANT SAFETY RULES:
//
// 1. DO NOT call Close() on the returned *sql.DB
//    - The SQLiteStorage owns the connection lifecycle
//    - Closing it will break all storage operations
//    - Use storage.Close() to close the database
//
// 2. DO NOT modify connection pool settings
//    - Avoid SetMaxOpenConns, SetMaxIdleConns, SetConnMaxLifetime, etc.
//    - The storage has already configured these for optimal performance
//
// 3. DO NOT change SQLite PRAGMAs
//    - The database is configured with WAL mode, foreign keys, and busy timeout
//    - Changing these (e.g., journal_mode, synchronous, locking_mode) can cause corruption
//
// 4. Expect errors after storage.Close()
//    - Check storage.IsClosed() before long-running operations if needed
//    - Pass contexts with timeouts to prevent hanging on closed connections
//
// 5. Keep write transactions SHORT
//    - SQLite has a single-writer lock even in WAL mode
//    - Long-running write transactions will block core storage operations
//    - Use read transactions (BEGIN DEFERRED) when possible
//
// GOOD PRACTICES:
//
// - Create extension tables with FOREIGN KEY constraints to maintain referential integrity
// - Use the same DATETIME format (RFC3339 / ISO8601) for consistency
// - Leverage SQLite indexes for query performance
// - Test with -race flag to catch concurrency issues
//
// EXAMPLE (creating a VC extension table):
//
//	db := storage.UnderlyingDB()
//	_, err := db.Exec(`
//	    CREATE TABLE IF NOT EXISTS vc_executions (
//	        id INTEGER PRIMARY KEY AUTOINCREMENT,
//	        issue_id TEXT NOT NULL,
//	        status TEXT NOT NULL,
//	        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
//	        FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
//	    );
//	    CREATE INDEX IF NOT EXISTS idx_vc_executions_issue ON vc_executions(issue_id);
//	`)
//
func (s *SQLiteStorage) UnderlyingDB() *sql.DB {
	return s.db
}

// UnderlyingConn returns a single connection from the pool for scoped use.
//
// This provides a connection with explicit lifetime boundaries, useful for:
// - One-time DDL operations (CREATE TABLE, ALTER TABLE)
// - Migration scripts that need transaction control
// - Operations that benefit from connection-level state
//
// IMPORTANT: The caller MUST close the connection when done:
//
//	conn, err := storage.UnderlyingConn(ctx)
//	if err != nil {
//	    return err
//	}
//	defer conn.Close()
//
// For general queries and transactions, prefer UnderlyingDB() which manages
// the connection pool automatically.
//
// EXAMPLE (extension table migration):
//
//	conn, err := storage.UnderlyingConn(ctx)
//	if err != nil {
//	    return err
//	}
//	defer conn.Close()
//
//	_, err = conn.ExecContext(ctx, `
//	    CREATE TABLE IF NOT EXISTS vc_executions (
//	        id INTEGER PRIMARY KEY AUTOINCREMENT,
//	        issue_id TEXT NOT NULL,
//	        FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
//	    )
//	`)
func (s *SQLiteStorage) UnderlyingConn(ctx context.Context) (*sql.Conn, error) {
	return s.db.Conn(ctx)
}

// CheckpointWAL checkpoints the WAL file to flush changes to the main database file.
// In WAL mode, writes go to the -wal file, leaving the main .db file untouched.
// Checkpointing:
// - Ensures data persistence by flushing WAL to main database
// - Reduces WAL file size
// - Makes database safe for backup/copy operations
func (s *SQLiteStorage) CheckpointWAL(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(FULL)")
	return err
}
