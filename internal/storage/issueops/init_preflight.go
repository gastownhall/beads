package issueops

import (
	"context"
	"fmt"
)

// CountIssuesForReinitInTx counts existing rows without depending on columns
// added by migrations. Only a zero-table database is unambiguously fresh;
// missing issues in an otherwise populated schema is not an empty database.
func CountIssuesForReinitInTx(ctx context.Context, tx DBTX) (int, error) {
	rows, err := tx.QueryContext(ctx, "SHOW TABLES")
	if err != nil {
		return 0, err
	}
	tables := make(map[string]bool)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return 0, err
		}
		tables[table] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(tables) == 0 {
		return 0, nil
	}
	if !tables["issues"] {
		return 0, fmt.Errorf("existing database has tables but no issues table")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
		return 0, err
	}
	if tables["wisps"] {
		var wisps int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps").Scan(&wisps); err != nil {
			return 0, err
		}
		count += wisps
	}
	return count, nil
}
