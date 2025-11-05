// Package sqlite - database migrations
package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// Migration represents a single database migration
type Migration struct {
	Name string
	Func func(*sql.DB) error
}

// migrations is the ordered list of all migrations to run
// Migrations are run in order during database initialization
var migrations = []Migration{
	{"dirty_issues_table", migrateDirtyIssuesTable},
	{"external_ref_column", migrateExternalRefColumn},
	{"composite_indexes", migrateCompositeIndexes},
	{"closed_at_constraint", migrateClosedAtConstraint},
	{"compaction_columns", migrateCompactionColumns},
	{"snapshots_table", migrateSnapshotsTable},
	{"compaction_config", migrateCompactionConfig},
	{"compacted_at_commit_column", migrateCompactedAtCommitColumn},
	{"export_hashes_table", migrateExportHashesTable},
	{"content_hash_column", migrateContentHashColumn},
	{"external_ref_unique", migrateExternalRefUnique},
	{"source_repo_column", migrateSourceRepoColumn},
	{"repo_mtimes_table", migrateRepoMtimesTable},
	{"child_counters_table", migrateChildCountersTable},
}

// MigrationInfo contains metadata about a migration for inspection
type MigrationInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListMigrations returns list of all registered migrations with descriptions
// Note: This returns ALL registered migrations, not just pending ones (all are idempotent)
func ListMigrations() []MigrationInfo {
	result := make([]MigrationInfo, len(migrations))
	for i, m := range migrations {
		result[i] = MigrationInfo{
			Name:        m.Name,
			Description: getMigrationDescription(m.Name),
		}
	}
	return result
}

// getMigrationDescription returns a human-readable description for a migration
func getMigrationDescription(name string) string {
	descriptions := map[string]string{
		"dirty_issues_table":           "Adds dirty_issues table for auto-export tracking",
		"external_ref_column":          "Adds external_ref column to issues table",
		"composite_indexes":            "Adds composite indexes for better query performance",
		"closed_at_constraint":         "Adds constraint ensuring closed issues have closed_at timestamp",
		"compaction_columns":           "Adds compaction tracking columns (compacted_at, compacted_at_commit)",
		"snapshots_table":              "Adds snapshots table for issue history",
		"compaction_config":            "Adds config entries for compaction",
		"compacted_at_commit_column":   "Adds compacted_at_commit to snapshots table",
		"export_hashes_table":          "Adds export_hashes table for idempotent exports",
		"content_hash_column":          "Adds content_hash column for collision resolution",
		"external_ref_unique":          "Adds UNIQUE constraint on external_ref column",
		"source_repo_column":           "Adds source_repo column for multi-repo support",
		"repo_mtimes_table":            "Adds repo_mtimes table for multi-repo hydration caching",
		"child_counters_table":         "Adds child_counters table for hierarchical ID generation with ON DELETE CASCADE",
	}
	
	if desc, ok := descriptions[name]; ok {
		return desc
	}
	return "Unknown migration"
}

// RunMigrations executes all registered migrations in order with invariant checking
func RunMigrations(db *sql.DB) error {
	// Capture pre-migration snapshot for validation
	snapshot, err := captureSnapshot(db)
	if err != nil {
		return fmt.Errorf("failed to capture pre-migration snapshot: %w", err)
	}

	// Run migrations (they are already idempotent)
	for _, migration := range migrations {
		if err := migration.Func(db); err != nil {
			return fmt.Errorf("migration %s failed: %w", migration.Name, err)
		}
	}

	// Verify invariants after migrations complete
	if err := verifyInvariants(db, snapshot); err != nil {
		return fmt.Errorf("post-migration validation failed: %w", err)
	}

	return nil
}

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

func migrateExternalRefUnique(db *sql.DB) error {
	var hasConstraint bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM sqlite_master
		WHERE type = 'index'
		  AND name = 'idx_issues_external_ref_unique'
	`).Scan(&hasConstraint)
	if err != nil {
		return fmt.Errorf("failed to check for UNIQUE constraint: %w", err)
	}

	if hasConstraint {
		return nil
	}

	existingDuplicates, err := findExternalRefDuplicates(db)
	if err != nil {
		return fmt.Errorf("failed to check for duplicate external_ref values: %w", err)
	}

	if len(existingDuplicates) > 0 {
		return fmt.Errorf("cannot add UNIQUE constraint: found %d duplicate external_ref values (resolve with 'bd duplicates' or manually)", len(existingDuplicates))
	}

	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_external_ref_unique ON issues(external_ref) WHERE external_ref IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("failed to create UNIQUE index on external_ref: %w", err)
	}

	return nil
}

func findExternalRefDuplicates(db *sql.DB) (map[string][]string, error) {
	rows, err := db.Query(`
		SELECT external_ref, GROUP_CONCAT(id, ',') as ids
		FROM issues
		WHERE external_ref IS NOT NULL
		GROUP BY external_ref
		HAVING COUNT(*) > 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	duplicates := make(map[string][]string)
	for rows.Next() {
		var externalRef, idsCSV string
		if err := rows.Scan(&externalRef, &idsCSV); err != nil {
			return nil, err
		}
		ids := strings.Split(idsCSV, ",")
		duplicates[externalRef] = ids
	}

	return duplicates, rows.Err()
}

// migrateSourceRepoColumn adds source_repo column for multi-repo support (bd-307).
// Defaults to "." (primary repo) for backward compatibility with existing issues.
func migrateSourceRepoColumn(db *sql.DB) error {
	// Check if source_repo column exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'source_repo'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check source_repo column: %w", err)
	}

	if columnExists {
		// Column already exists
		return nil
	}

	// Add source_repo column with default "." (primary repo)
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN source_repo TEXT DEFAULT '.'`)
	if err != nil {
		return fmt.Errorf("failed to add source_repo column: %w", err)
	}

	// Create index on source_repo for efficient filtering
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_source_repo ON issues(source_repo)`)
	if err != nil {
		return fmt.Errorf("failed to create source_repo index: %w", err)
	}

	return nil
}

// migrateRepoMtimesTable creates the repo_mtimes table for multi-repo hydration caching (bd-307)
func migrateRepoMtimesTable(db *sql.DB) error {
	// Check if repo_mtimes table exists
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='repo_mtimes'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		// Table doesn't exist, create it
		_, err := db.Exec(`
			CREATE TABLE repo_mtimes (
				repo_path TEXT PRIMARY KEY,
				jsonl_path TEXT NOT NULL,
				mtime_ns INTEGER NOT NULL,
				last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX idx_repo_mtimes_checked ON repo_mtimes(last_checked);
		`)
		if err != nil {
			return fmt.Errorf("failed to create repo_mtimes table: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for repo_mtimes table: %w", err)
	}

	// Table already exists
	return nil
}

// migrateChildCountersTable creates the child_counters table for hierarchical ID generation (bd-bb08)
func migrateChildCountersTable(db *sql.DB) error {
	// Check if child_counters table exists
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='child_counters'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		// Table doesn't exist, create it
		_, err := db.Exec(`
			CREATE TABLE child_counters (
				parent_id TEXT PRIMARY KEY,
				last_child INTEGER NOT NULL DEFAULT 0,
				FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return fmt.Errorf("failed to create child_counters table: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for child_counters table: %w", err)
	}

	// Table already exists
	return nil
}
