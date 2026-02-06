//go:build cgo
package migrations

import (
	"database/sql"
	"fmt"
)

// columnExists checks if a column exists in a table using information_schema.
// Must include table_schema = DATABASE() to scope to current database,
// otherwise it may find columns in other Dolt databases.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?
	`, table, column).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to query information_schema: %w", err)
	}
	return count > 0, nil
}

// tableExists checks if a table exists using information_schema.
// Must include table_schema = DATABASE() to scope to current database,
// otherwise it may find tables in other Dolt databases.
func tableExists(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name = ?
	`, table).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to query information_schema: %w", err)
	}
	return count > 0, nil
}


