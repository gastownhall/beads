package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateWispAuxiliaryTables creates auxiliary tables for wisps: labels,
// dependencies, events, and comments. These mirror the corresponding main
// tables but reference the wisps table instead of issues. They are covered
// by the dolt_ignore pattern "wisp_%" added in migration 004.
func MigrateWispAuxiliaryTables(db *sql.DB) error {
	tables := map[string]string{
		"wisp_labels":       wispLabelsSchema,
		"wisp_dependencies": wispDependenciesSchema,
		"wisp_events":       wispEventsSchema,
		"wisp_comments":     wispCommentsSchema,
	}

	for name, schema := range tables {
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("failed to create %s table: %w", name, err)
		}
	}

	return nil
}

const wispLabelsSchema = `CREATE TABLE IF NOT EXISTS wisp_labels (
    issue_id VARCHAR(255) NOT NULL,
    label VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, label),
    INDEX idx_wisp_labels_label (label)
)`

const wispDependenciesSchema = `CREATE TABLE IF NOT EXISTS wisp_dependencies (
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

const wispEventsSchema = `CREATE TABLE IF NOT EXISTS wisp_events (
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

const wispCommentsSchema = `CREATE TABLE IF NOT EXISTS wisp_comments (
    id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
    issue_id VARCHAR(255) NOT NULL,
    author VARCHAR(255) DEFAULT '',
    text TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_wisp_comments_issue (issue_id)
)`
