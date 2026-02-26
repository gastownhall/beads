package migrations

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
)

// isTableNotFoundError checks if the error is a MySQL/Dolt "table not found"
// error (Error 1146). Dolt returns: Error 1146 (HY000): table not found: tablename
func isTableNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1146
	}
	// String fallback for non-MySQL error wrappers
	return strings.Contains(err.Error(), "table not found")
}

// columnExists checks if a column exists in a table using SHOW COLUMNS.
// Uses SHOW COLUMNS FROM ... LIKE instead of information_schema to avoid
// crashes when the Dolt server catalog contains stale database entries
// from cleaned-up worktrees (GH#2051). SHOW COLUMNS is inherently scoped
// to the current database, so it also avoids cross-database false positives.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	// Use string interpolation instead of parameterized query because Dolt
	// doesn't support prepared-statement parameters for SHOW commands.
	// Table/column names come from internal constants, not user input.
	// #nosec G202 -- table and column names come from internal constants, not user input.
	rows, err := db.Query("SHOW COLUMNS FROM `" + table + "` LIKE '" + column + "'") //nolint:gosec // G202: table/column are internal constants, not user input
	if err != nil {
		if isTableNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check column %s.%s: %w", table, column, err)
	}
	defer rows.Close()
	return rows.Next(), nil
}

// tableExists checks if a table exists using SHOW TABLES.
// Uses SHOW TABLES LIKE instead of information_schema to avoid crashes
// when the Dolt server catalog contains stale database entries from
// cleaned-up worktrees (GH#2051). SHOW TABLES is inherently scoped
// to the current database.
func tableExists(db *sql.DB, table string) (bool, error) {
	// Use string interpolation instead of parameterized query because Dolt
	// doesn't support prepared-statement parameters for SHOW commands.
	// Table names come from internal constants, not user input.
	// #nosec G202 -- table names come from internal constants, not user input.
	rows, err := db.Query("SHOW TABLES LIKE '" + table + "'") //nolint:gosec // G202: table name is an internal constant, not user input
	if err != nil {
		return false, fmt.Errorf("failed to check table %s: %w", table, err)
	}
	defer rows.Close()
	return rows.Next(), nil
}
