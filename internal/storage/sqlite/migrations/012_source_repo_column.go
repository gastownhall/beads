package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateSourceRepoColumn(db *sql.DB) error {
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
		return nil
	}

	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN source_repo TEXT DEFAULT '.'`)
	if err != nil {
		return fmt.Errorf("failed to add source_repo column: %w", err)
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_source_repo ON issues(source_repo)`)
	if err != nil {
		return fmt.Errorf("failed to create source_repo index: %w", err)
	}

	return nil
}
