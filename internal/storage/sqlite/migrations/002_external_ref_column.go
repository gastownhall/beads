package migrations

import (
	"database/sql"
	"errors"
	"fmt"
)

func MigrateExternalRefColumn(db *sql.DB) (retErr error) {
	var columnExists bool
	rows, err := db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return fmt.Errorf("failed to check schema: %w", err)
	}
	defer func() {
		if rows != nil {
			if closeErr := rows.Close(); closeErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("failed to close schema rows: %w", closeErr))
			}
		}
	}()

	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
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

	// Close rows before executing any statements to avoid deadlock with MaxOpenConns(1).
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close schema rows: %w", err)
	}
	rows = nil

	if !columnExists {
		_, err := db.Exec(`ALTER TABLE issues ADD COLUMN external_ref TEXT`)
		if err != nil {
			return fmt.Errorf("failed to add external_ref column: %w", err)
		}
	}

	// Create index on external_ref (idempotent)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_external_ref ON issues(external_ref)`)
	if err != nil {
		return fmt.Errorf("failed to create index on external_ref: %w", err)
	}

	return nil
}
