//go:build cgo && !windows
package migrations

import (
	"database/sql"
	"fmt"
)

// columnExists checks if a column exists in a table using information_schema.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_name = ? AND column_name = ?
	`, table, column).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to query information_schema: %w", err)
	}
	return count > 0, nil
}

// tableExists checks if a table exists using information_schema.
func tableExists(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_name = ?
	`, table).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to query information_schema: %w", err)
	}
	return count > 0, nil
}

