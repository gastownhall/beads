package issueops

import (
	"context"
	"sort"
)

// ReinitIssueCount separates an inspected but unrecognized schema from a failed
// inspection. UnknownTables is nonempty only when tables exist without issues;
// callers must require explicit confirmation rather than treating Count as zero.
type ReinitIssueCount struct {
	Count         int
	UnknownTables []string
}

// CountIssuesForReinitInTx counts existing rows without depending on columns
// added by migrations. Only a zero-table database is unambiguously fresh;
// missing issues in an otherwise populated schema is not an empty database.
func CountIssuesForReinitInTx(ctx context.Context, tx DBTX) (result ReinitIssueCount, err error) {
	rows, err := tx.QueryContext(ctx, "SHOW TABLES")
	if err != nil {
		return result, err
	}
	tables := make(map[string]bool)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return result, err
		}
		tables[table] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if len(tables) == 0 {
		return result, nil
	}
	if !tables["issues"] {
		for table := range tables {
			result.UnknownTables = append(result.UnknownTables, table)
		}
		sort.Strings(result.UnknownTables)
		return result, nil
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&result.Count); err != nil {
		return result, err
	}
	if tables["wisps"] {
		var wisps int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps").Scan(&wisps); err != nil {
			return result, err
		}
		result.Count += wisps
	}
	return result, nil
}
