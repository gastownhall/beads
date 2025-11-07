package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateCompositeIndexes(db *sql.DB) error {
	var indexName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='index' AND name='idx_dependencies_depends_on_type'
	`).Scan(&indexName)

	if err == sql.ErrNoRows {
		_, err := db.Exec(`
			CREATE INDEX idx_dependencies_depends_on_type ON dependencies(depends_on_id, type)
		`)
		if err != nil {
			return fmt.Errorf("failed to create composite index idx_dependencies_depends_on_type: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for composite index: %w", err)
	}

	return nil
}
