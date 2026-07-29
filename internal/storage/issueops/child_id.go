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

	//nolint:gosec // G201: issueTable is one of two hardcoded constants.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id FROM %s
		WHERE id LIKE CONCAT(?, '.%%')
		  AND id NOT LIKE CONCAT(?, '.%%.%%')
	`, issueTable), parentID, parentID)
	if err != nil {
		return "", fmt.Errorf("get next child ID: query existing children: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("get next child ID: scan child row: %w", err)
		}
		_, childNum, ok := ParseHierarchicalID(id)
		if ok && childNum > lastChild {
			lastChild = childNum
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("get next child ID: iterate children: %w", err)
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
