package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateExternalRefColumn(db *sql.DB) error {
	var columnExists bool
	rows, err := db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return fmt.Errorf("failed to check schema: %w", err)
	}

	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt *string
		err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		if err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan column info: %w", err)
		}
		if name == "external_ref" {
			columnExists = true
			break
		}
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("error reading column info: %w", err)
	}

	// Close rows before executing any statements to avoid deadlock with MaxOpenConns(1)
	rows.Close()

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
