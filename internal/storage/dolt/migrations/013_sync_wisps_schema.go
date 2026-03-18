package migrations

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// MigrateSyncWispsSchema ensures the wisps table has all columns that exist
// on the issues table. This prevents schema drift where columns are added to
// issues (via schema.go or migrations) but not to wisps, causing SELECT queries
// using IssueSelectColumns to fail against the wisps table.
//
// Idempotent: only adds columns that are actually missing.
func MigrateSyncWispsSchema(db *sql.DB) error {
	// Check if wisps table exists — skip entirely if not yet created.
	exists, err := tableExists(db, "wisps")
	if err != nil {
		return fmt.Errorf("check wisps table: %w", err)
	}
	if !exists {
		return nil
	}

	// Get column details from both tables using SHOW COLUMNS.
	issuesCols, err := getColumnDetails(db, "issues")
	if err != nil {
		return fmt.Errorf("get issues columns: %w", err)
	}
	wispsCols, err := getColumnDetails(db, "wisps")
	if err != nil {
		return fmt.Errorf("get wisps columns: %w", err)
	}

	// Build set of wisps column names.
	wispsSet := make(map[string]bool, len(wispsCols))
	for _, c := range wispsCols {
		wispsSet[c.field] = true
	}

	// Add any missing columns to wisps with same type and default as issues.
	var added []string
	for _, col := range issuesCols {
		if wispsSet[col.field] {
			continue
		}
		alterSQL := fmt.Sprintf("ALTER TABLE `wisps` ADD COLUMN `%s` %s", col.field, col.typ)
		if col.def.Valid {
			alterSQL += " DEFAULT " + quoteDefault(col.def.String)
		}
		//nolint:gosec // G201: col.field and col.typ come from SHOW COLUMNS, not user input
		if _, err := db.Exec(alterSQL); err != nil {
			// Column may have been added concurrently; skip duplicates.
			if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				continue
			}
			return fmt.Errorf("add column %s to wisps: %w", col.field, err)
		}
		added = append(added, col.field)
	}

	if len(added) > 0 {
		log.Printf("sync_wisps_schema: added %d columns to wisps: %s", len(added), strings.Join(added, ", "))
	}

	return nil
}

// columnDetail holds SHOW COLUMNS output for a single column.
type columnDetail struct {
	field string
	typ   string
	def   sql.NullString
}

// getColumnDetails returns column name, type, and default from SHOW COLUMNS.
func getColumnDetails(db *sql.DB, table string) ([]columnDetail, error) {
	//nolint:gosec // G202: table is internal constant
	rows, err := db.Query("SHOW COLUMNS FROM `" + table + "`")
	if err != nil {
		return nil, fmt.Errorf("show columns for %s: %w", table, err)
	}
	defer rows.Close()

	var cols []columnDetail
	for rows.Next() {
		var c columnDetail
		var null, key string
		var extra sql.NullString
		if err := rows.Scan(&c.field, &c.typ, &null, &key, &c.def, &extra); err != nil {
			return nil, fmt.Errorf("scan column for %s: %w", table, err)
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// quoteDefault wraps a column default value for use in ALTER TABLE.
// Numeric and NULL-like values pass through; strings get single-quoted.
func quoteDefault(s string) string {
	if s == "" {
		return "''"
	}
	upper := strings.ToUpper(s)
	// Pass through SQL expressions and numeric values.
	if upper == "NULL" || upper == "CURRENT_TIMESTAMP" ||
		strings.HasPrefix(upper, "(") {
		return s
	}
	// Check if it's a number.
	isNum := true
	for _, ch := range s {
		if (ch < '0' || ch > '9') && ch != '.' && ch != '-' {
			isNum = false
			break
		}
	}
	if isNum {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
