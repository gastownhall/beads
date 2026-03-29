package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateCommentSyncColumns adds external_ref and updated_at columns to the
// comments and wisp_comments tables, plus creates the attachments table.
// These are needed for bidirectional comment sync and attachment pull.
func MigrateCommentSyncColumns(db *sql.DB) error {
	// Add external_ref and updated_at to comments and wisp_comments.
	for _, table := range []string{"comments", "wisp_comments"} {
		exists, err := tableExists(db, table)
		if err != nil {
			return fmt.Errorf("check table %s: %w", table, err)
		}
		if !exists {
			continue
		}

		// external_ref column
		hasExtRef, err := columnExists(db, table, "external_ref")
		if err != nil {
			return fmt.Errorf("check %s.external_ref: %w", table, err)
		}
		if !hasExtRef {
			//nolint:gosec // G202: table is an internal constant
			if _, err := db.Exec(fmt.Sprintf(
				"ALTER TABLE `%s` ADD COLUMN `external_ref` VARCHAR(255) DEFAULT ''", table)); err != nil {
				return fmt.Errorf("add %s.external_ref: %w", table, err)
			}
		}

		// updated_at column
		hasUpdatedAt, err := columnExists(db, table, "updated_at")
		if err != nil {
			return fmt.Errorf("check %s.updated_at: %w", table, err)
		}
		if !hasUpdatedAt {
			//nolint:gosec // G202: table is an internal constant
			if _, err := db.Exec(fmt.Sprintf(
				"ALTER TABLE `%s` ADD COLUMN `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP", table)); err != nil {
				return fmt.Errorf("add %s.updated_at: %w", table, err)
			}
		}

		// Index on external_ref
		idxName := "idx_" + table + "_external_ref"
		if !indexExists(db, table, idxName) {
			//nolint:gosec // G202: table and idxName are internal constants
			if _, err := db.Exec(fmt.Sprintf(
				"CREATE INDEX `%s` ON `%s` (`external_ref`)", idxName, table)); err != nil {
				return fmt.Errorf("create index %s on %s: %w", idxName, table, err)
			}
		}
	}

	// Create the attachments table if it doesn't exist.
	attExists, err := tableExists(db, "attachments")
	if err != nil {
		return fmt.Errorf("check attachments table: %w", err)
	}
	if !attExists {
		if _, err := db.Exec(`
			CREATE TABLE attachments (
				id CHAR(36) NOT NULL PRIMARY KEY DEFAULT (UUID()),
				issue_id VARCHAR(255) NOT NULL,
				external_ref VARCHAR(255) NOT NULL DEFAULT '',
				filename VARCHAR(500) NOT NULL DEFAULT '',
				url TEXT NOT NULL,
				mime_type VARCHAR(255) NOT NULL DEFAULT '',
				size_bytes BIGINT NOT NULL DEFAULT 0,
				source VARCHAR(64) NOT NULL DEFAULT '',
				creator VARCHAR(255) NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				INDEX idx_attachments_issue (issue_id),
				INDEX idx_attachments_external_ref (external_ref)
			)
		`); err != nil {
			return fmt.Errorf("create attachments table: %w", err)
		}
	}

	return nil
}
