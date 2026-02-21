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
		exists, err := tableExists(db, name)
		if err != nil {
			return fmt.Errorf("failed to check %s existence: %w", name, err)
		}
		if exists {
			continue
		}
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("failed to create %s table: %w", name, err)
		}
	}

	return nil
}

const wispLabelsSchema = `CREATE TABLE wisp_labels (
    issue_id VARCHAR(255) NOT NULL,
    label VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, label),
    INDEX idx_wisp_labels_label (label)
)`

const wispDependenciesSchema = `CREATE TABLE wisp_dependencies (
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

const wispEventsSchema = `CREATE TABLE wisp_events (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    issue_id VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) DEFAULT '',
    old_value TEXT DEFAULT '',
    new_value TEXT DEFAULT '',
    comment TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_wisp_events_issue (issue_id)
)`

const wispCommentsSchema = `CREATE TABLE wisp_comments (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    issue_id VARCHAR(255) NOT NULL,
    author VARCHAR(255) DEFAULT '',
    text TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_wisp_comments_issue (issue_id)
)`
