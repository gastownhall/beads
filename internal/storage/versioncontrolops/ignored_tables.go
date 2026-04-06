package versioncontrolops

import (
	"context"
	"fmt"
)

// EnsureIgnoredTables checks whether the dolt_ignore'd wisp tables exist in
// the current working set and creates them if missing. This is the fast path
// called after branch creation, checkout, and on session init — it executes a
// single SHOW TABLES query and returns immediately when the tables are present.
//
// dolt_ignore entries are committed and persist across branches; only the
// tables themselves (which live in the working set) need recreation.
func EnsureIgnoredTables(ctx context.Context, db DBConn) error {
	exists, err := TableExists(ctx, db, "wisps")
	if err != nil {
		return fmt.Errorf("check wisps table: %w", err)
	}
	if exists {
		return nil
	}
	return CreateIgnoredTables(ctx, db)
}

// CreateIgnoredTables unconditionally creates all dolt_ignore'd tables
// (wisps, wisp_labels, wisp_dependencies, wisp_events, wisp_comments).
// All statements use CREATE TABLE IF NOT EXISTS, so this is idempotent.
//
// This does NOT set up dolt_ignore entries or commit — those are migration
// concerns handled separately during bd init.
func CreateIgnoredTables(ctx context.Context, db DBConn) error {
	for _, ddl := range IgnoredTableDDL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create ignored table: %w", err)
		}
	}
	return nil
}

// TableExists checks if a table exists using SHOW TABLES LIKE.
// Uses SHOW TABLES rather than information_schema to avoid crashes when the
// Dolt server catalog contains stale database entries from cleaned-up
// worktrees (GH#2051). SHOW TABLES is inherently scoped to the current
// database.
func TableExists(ctx context.Context, db DBConn, table string) (bool, error) {
	// Use string interpolation because Dolt doesn't support prepared-statement
	// parameters for SHOW commands. Table names come from internal constants.
	// #nosec G202 -- table names come from internal constants, not user input.
	rows, err := db.QueryContext(ctx, "SHOW TABLES LIKE '"+table+"'") //nolint:gosec // G202: table name is an internal constant
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", table, err)
	}
	defer rows.Close()
	return rows.Next(), nil
}

// IgnoredTableDDL is the ordered list of CREATE TABLE IF NOT EXISTS statements
// for all dolt_ignore'd tables. This is the single source of truth for the
// wisp table schemas used by both DoltStore and EmbeddedDoltStore.
var IgnoredTableDDL = []string{
	WispsTableSchema,
	WispLabelsSchema,
	WispDependenciesSchema,
	WispEventsSchema,
	WispCommentsSchema,
}

// WispsTableSchema mirrors the issues table schema exactly.
// This table is ignored by dolt_ignore and will not appear in Dolt commits.
const WispsTableSchema = `CREATE TABLE IF NOT EXISTS wisps (
    id VARCHAR(255) PRIMARY KEY,
    content_hash VARCHAR(64),
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    design TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
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
    no_history TINYINT(1) DEFAULT 0,
    wisp_type VARCHAR(32) DEFAULT '',
    pinned TINYINT(1) DEFAULT 0,
    is_template TINYINT(1) DEFAULT 0,
    mol_type VARCHAR(32) DEFAULT '',
    work_type VARCHAR(32) DEFAULT 'mutex',
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

const WispLabelsSchema = `CREATE TABLE IF NOT EXISTS wisp_labels (
    issue_id VARCHAR(255) NOT NULL,
    label VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, label),
    INDEX idx_wisp_labels_label (label)
)`

const WispDependenciesSchema = `CREATE TABLE IF NOT EXISTS wisp_dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    PRIMARY KEY (issue_id, depends_on_id),
    INDEX idx_wisp_dep_depends (depends_on_id)
)`

const WispEventsSchema = `CREATE TABLE IF NOT EXISTS wisp_events (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) DEFAULT '',
    old_value TEXT DEFAULT '',
    new_value TEXT DEFAULT '',
    comment TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_wisp_events_issue (issue_id),
    INDEX idx_wisp_events_created_at (created_at)
)`

const WispCommentsSchema = `CREATE TABLE IF NOT EXISTS wisp_comments (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    author VARCHAR(255) DEFAULT '',
    text TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_wisp_comments_issue (issue_id)
)`
