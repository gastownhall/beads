package migrations

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// infraTypes are issue types that belong in the wisps table, not the versioned issues table.
var infraTypes = []string{"agent", "rig", "role", "message"}

// MigrateInfraToWisps moves infrastructure beads (agent, rig, role, message)
// from the versioned issues table to the dolt-ignored wisps table.
// This keeps the issues table clean for real work items and prevents infra
// beads from bloating dolt history and stats.
//
// Idempotent: skips if no matching rows exist in issues table.
func MigrateInfraToWisps(db *sql.DB) error {
	// Check if wisps table exists (migration 004 must have run first)
	exists, err := tableExists(db, "wisps")
	if err != nil {
		return fmt.Errorf("checking wisps table: %w", err)
	}
	if !exists {
		// wisps table doesn't exist yet — nothing to migrate
		return nil
	}

	// Check if any infra beads exist in the issues table
	placeholders := make([]string, len(infraTypes))
	args := make([]interface{}, len(infraTypes))
	for i, t := range infraTypes {
		placeholders[i] = "?"
		args[i] = t
	}
	inClause := strings.Join(placeholders, ",")

	var count int
	//nolint:gosec // G201: placeholders contains only ? markers
	err = db.QueryRow(
		fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE issue_type IN (%s)", inClause),
		args...,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("counting infra beads: %w", err)
	}
	if count == 0 {
		return nil // Nothing to migrate
	}

	log.Printf("migration 007: moving %d infra beads to wisps table", count)

	// Copy data using only common columns to handle schema evolution (e.g. dropped/added columns)
	// where the source table and destination table might have different column counts.
	if err := copyCommonColumns(db, "wisps", "issues", "", fmt.Sprintf("src.issue_type IN (%s)", inClause), args); err != nil {
		return err
	}

	// Mark as ephemeral in wisps table (issues table may have ephemeral=0)
	//nolint:gosec // G201: inClause built from ? placeholders
	_, err = db.Exec(fmt.Sprintf(`
		UPDATE wisps SET ephemeral = 1 WHERE issue_type IN (%s)
	`, inClause), args...)
	if err != nil {
		return fmt.Errorf("setting ephemeral flag on migrated infra beads: %w", err)
	}

	// Copy labels
	if err := copyCommonColumns(db, "wisp_labels", "labels", "INNER JOIN issues i ON src.issue_id = i.id", fmt.Sprintf("i.issue_type IN (%s)", inClause), args); err != nil {
		return err
	}

	// Copy dependencies
	if err := copyCommonColumns(db, "wisp_dependencies", "dependencies", "INNER JOIN issues i ON src.issue_id = i.id", fmt.Sprintf("i.issue_type IN (%s)", inClause), args); err != nil {
		return err
	}

	// Copy events
	if err := copyCommonColumns(db, "wisp_events", "events", "INNER JOIN issues i ON src.issue_id = i.id", fmt.Sprintf("i.issue_type IN (%s)", inClause), args); err != nil {
		return err
	}

	// Copy comments
	if err := copyCommonColumns(db, "wisp_comments", "comments", "INNER JOIN issues i ON src.issue_id = i.id", fmt.Sprintf("i.issue_type IN (%s)", inClause), args); err != nil {
		return err
	}

	// Now delete originals from versioned tables (order: children first, then issues)
	for _, table := range []string{"comments", "events", "dependencies", "labels"} {
		//nolint:gosec // G201: table from hardcoded list, inClause from ? placeholders
		_, err = db.Exec(fmt.Sprintf(`
			DELETE FROM %s WHERE issue_id IN (
				SELECT id FROM issues WHERE issue_type IN (%s)
			)
		`, table, inClause), args...)
		if err != nil {
			return fmt.Errorf("deleting infra rows from %s: %w", table, err)
		}
	}

	// Delete infra issues themselves
	//nolint:gosec // G201: inClause built from ? placeholders
	_, err = db.Exec(fmt.Sprintf(`
		DELETE FROM issues WHERE issue_type IN (%s)
	`, inClause), args...)
	if err != nil {
		return fmt.Errorf("deleting infra issues: %w", err)
	}

	log.Printf("migration 007: migrated %d infra beads to wisps table", count)
	return nil
}

// copyCommonColumns copies rows from srcTable to destTable, mapping only the columns that exist in both.
// This prevents "number of values does not match number of columns" errors across schema evolutions.
func copyCommonColumns(db *sql.DB, destTable, srcTable, joinClause, whereClause string, args []interface{}) error {
	destCols, err := getTableColumns(db, destTable)
	if err != nil {
		return err
	}
	srcCols, err := getTableColumns(db, srcTable)
	if err != nil {
		return err
	}

	destMap := make(map[string]bool, len(destCols))
	for _, c := range destCols {
		destMap[c] = true
	}

	var commonCols []string
	for _, c := range srcCols {
		if destMap[c] {
			commonCols = append(commonCols, "`"+c+"`")
		}
	}

	if len(commonCols) == 0 {
		return fmt.Errorf("no common columns between %s and %s", destTable, srcTable)
	}

	colStr := strings.Join(commonCols, ", ")

	var srcColStr string
	if joinClause != "" {
		var srcAliased []string
		for _, c := range commonCols {
			srcAliased = append(srcAliased, "src.`"+strings.Trim(c, "`")+"`")
		}
		srcColStr = strings.Join(srcAliased, ", ")
	} else {
		srcColStr = colStr
	}

	//nolint:gosec // G201: table names and columns are internal strings, not user input
	query := fmt.Sprintf(`
		INSERT IGNORE INTO %s (%s)
		SELECT %s FROM %s src
		%s
		WHERE %s
	`, destTable, colStr, srcColStr, srcTable, joinClause, whereClause)

	if _, err := db.Exec(query, args...); err != nil {
		return fmt.Errorf("copying %s to %s: %w", srcTable, destTable, err)
	}
	return nil
}

// getTableColumns gets the list of columns for a table using SHOW COLUMNS.
func getTableColumns(db *sql.DB, table string) ([]string, error) {
	//nolint:gosec // G202: table is internal constant
	rows, err := db.Query("SHOW COLUMNS FROM `" + table + "`")
	if err != nil {
		return nil, fmt.Errorf("failed to get columns for %s: %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var field, typ, null, key string
		var def, extra sql.NullString
		if err := rows.Scan(&field, &typ, &null, &key, &def, &extra); err != nil {
			return nil, fmt.Errorf("failed to scan column info for %s: %w", table, err)
		}
		cols = append(cols, field)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns for %s: %w", table, err)
	}
	return cols, nil
}
