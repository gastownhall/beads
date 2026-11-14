package issueops

import (
	"context"
	"database/sql"
	"fmt"
)

func GetNextChildIDTx(ctx context.Context, tx *sql.Tx, parentID string) (string, error) {
	counterTable, issueTable := "child_counters", "issues"
	if IsActiveWispInTx(ctx, tx, parentID) {
		counterTable, issueTable = "wisp_child_counters", "wisps"
	}

	var lastChild int
	//nolint:gosec // G201: counterTable is one of two hardcoded constants.
	err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT last_child FROM %s WHERE parent_id = ?", counterTable),
		parentID).Scan(&lastChild)
	if err == sql.ErrNoRows {
		lastChild = 0
	} else if err != nil {
		return "", fmt.Errorf("get next child ID: read counter: %w", err)
	}

	usedIDs, err := UsedChildIDsInTx(ctx, tx, issueTable, parentID)
	if err != nil {
		return "", fmt.Errorf("get next child ID: query existing children: %w", err)
	}
	for _, id := range usedIDs {
		_, childNum, ok := ParseHierarchicalID(id)
		if ok && childNum > lastChild {
			lastChild = childNum
		}
	}

	nextChild := lastChild + 1

	//nolint:gosec // G201: counterTable is one of two hardcoded constants.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (parent_id, last_child) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE last_child = ?
	`, counterTable), parentID, nextChild, nextChild); err != nil {
		return "", fmt.Errorf("get next child ID: update counter: %w", err)
	}

	return fmt.Sprintf("%s.%d", parentID, nextChild), nil
}

// usedChildIDsSQL reads the direct children of a parent from its row table
// (%[1]s) and from both tombstone tables. Each leg takes the parent id twice.
const usedChildIDsSQL = `SELECT id FROM %[1]s WHERE id LIKE CONCAT(?, '.%%') AND id NOT LIKE CONCAT(?, '.%%.%%')
UNION ALL SELECT id FROM %[2]s WHERE id LIKE CONCAT(?, '.%%') AND id NOT LIKE CONCAT(?, '.%%.%%')
UNION ALL SELECT id FROM %[3]s WHERE id LIKE CONCAT(?, '.%%') AND id NOT LIKE CONCAT(?, '.%%.%%')`

// UsedChildIDsInTx returns every id that has held a direct child suffix of
// parentID: the live children in issueTable ("issues" or "wisps") and the
// children a retaining sweep retired, from either tombstone table (a child can
// be swept from either plane). Both child id generators (GetNextChildIDTx and
// domain/db's NextChildID) fold these into their high-water mark, so a
// generated child id is never a retired one: once a retaining sweep deletes
// the highest child, its tombstone is the only record that the suffix was
// used, and the parent's counter can lag it (a database written before
// GH#4750, or an explicit child created through a door that does not advance
// the counter). One statement, so on READ COMMITTED a sweep committing between
// two reads cannot hide a child from both (idOccupancySQL's reason).
func UsedChildIDsInTx(ctx context.Context, tx DBTX, issueTable, parentID string) ([]string, error) {
	if issueTable != "issues" && issueTable != "wisps" {
		return nil, fmt.Errorf("used child ids: unknown table %q", issueTable)
	}
	//nolint:gosec // G201: issueTable is checked against two constants above; the tombstone tables are constants.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(usedChildIDsSQL, issueTable, IDTombstoneTable, WispIDTombstoneTable),
		parentID, parentID, parentID, parentID, parentID, parentID)
	if err != nil {
		return nil, fmt.Errorf("read used child ids of %q: %w", parentID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read used child ids of %q: %w", parentID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read used child ids of %q: %w", parentID, err)
	}
	return ids, nil
}
