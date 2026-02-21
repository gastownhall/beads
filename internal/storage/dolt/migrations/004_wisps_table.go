package migrations

import (
	"database/sql"
	"fmt"
	"strings"
)

// MigrateWispsTable adds dolt_ignore patterns for wisps tables and creates the
// wisps table. The dolt_ignore entry MUST be added before the table is created,
// because dolt_ignore only works on untracked tables. Once a table has been
// committed, the ignore pattern has no effect.
//
// The wisps table has the same schema as the issues table — it stores ephemeral
// "wisp" beads that should not be version-tracked in Dolt history.
func MigrateWispsTable(db *sql.DB) error {
	// Step 1: Add dolt_ignore patterns BEFORE creating the table.
	// Use REPLACE to be idempotent (dolt_ignore has pattern as PK).
	for _, pattern := range []string{"wisps", "wisp_%"} {
		_, err := db.Exec("REPLACE INTO dolt_ignore VALUES (?, true)", pattern)
		if err != nil {
			return fmt.Errorf("failed to add %q to dolt_ignore: %w", pattern, err)
		}
	}

	// Explicitly stage dolt_ignore and commit so the ignore is active before table creation.
	_, err := db.Exec("CALL DOLT_ADD('dolt_ignore')")
	if err != nil {
		return fmt.Errorf("failed to stage dolt_ignore: %w", err)
	}
	_, err = db.Exec("CALL DOLT_COMMIT('-m', 'chore: add wisps patterns to dolt_ignore')")
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
		return fmt.Errorf("failed to commit dolt_ignore changes: %w", err)
	}

	// Step 2: Check if wisps table already exists.
	exists, err := tableExists(db, "wisps")
	if err != nil {
		return fmt.Errorf("failed to check wisps table existence: %w", err)
	}
	if exists {
		return nil
	}

	// Step 3: Create wisps table with same schema as issues.
	_, err = db.Exec(wispsTableSchema)
	if err != nil {
		return fmt.Errorf("failed to create wisps table: %w", err)
	}

	return nil
}

// wispsTableSchema mirrors the issues table schema exactly.
// This table is ignored by dolt_ignore and will not appear in Dolt commits.
const wispsTableSchema = `CREATE TABLE wisps (
    id VARCHAR(255) PRIMARY KEY,
    content_hash VARCHAR(64),
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL,
    design TEXT NOT NULL,
    acceptance_criteria TEXT NOT NULL,
    notes TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    priority INT NOT NULL DEFAULT 2,
    issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
    assignee VARCHAR(255),
    estimated_minutes INT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    owner VARCHAR(255) DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    closed_at DATETIME,
    closed_by_session VARCHAR(255) DEFAULT '',
    external_ref VARCHAR(255),
    spec_id VARCHAR(1024),
    compaction_level INT DEFAULT 0,
    compacted_at DATETIME,
    compacted_at_commit VARCHAR(64),
    original_size INT,
    sender VARCHAR(255) DEFAULT '',
    ephemeral TINYINT(1) DEFAULT 0,
    wisp_type VARCHAR(32) DEFAULT '',
    pinned TINYINT(1) DEFAULT 0,
    is_template TINYINT(1) DEFAULT 0,
    crystallizes TINYINT(1) DEFAULT 0,
    mol_type VARCHAR(32) DEFAULT '',
    work_type VARCHAR(32) DEFAULT 'mutex',
    quality_score DOUBLE,
    source_system VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    source_repo VARCHAR(512) DEFAULT '',
    close_reason TEXT DEFAULT '',
    event_kind VARCHAR(32) DEFAULT '',
    actor VARCHAR(255) DEFAULT '',
    target VARCHAR(255) DEFAULT '',
    payload TEXT DEFAULT '',
    await_type VARCHAR(32) DEFAULT '',
    await_id VARCHAR(255) DEFAULT '',
    timeout_ns BIGINT DEFAULT 0,
    waiters TEXT DEFAULT '',
    hook_bead VARCHAR(255) DEFAULT '',
    role_bead VARCHAR(255) DEFAULT '',
    agent_state VARCHAR(32) DEFAULT '',
    last_activity DATETIME,
    role_type VARCHAR(32) DEFAULT '',
    rig VARCHAR(255) DEFAULT '',
    due_at DATETIME,
    defer_until DATETIME,
    INDEX idx_wisps_status (status),
    INDEX idx_wisps_priority (priority),
    INDEX idx_wisps_issue_type (issue_type),
    INDEX idx_wisps_assignee (assignee),
    INDEX idx_wisps_created_at (created_at),
    INDEX idx_wisps_spec_id (spec_id),
    INDEX idx_wisps_external_ref (external_ref)
)`
