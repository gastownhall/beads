package issueops

import (
	"context"
	"database/sql"
	"fmt"
)

// GetNextChildIDTx atomically generates the next child ID for a parent issue
// within an existing transaction. It reads the child_counters table, reconciles
// with any existing children in the issues table (to handle imports that bypass
// the counter), increments, and upserts the counter.
//
// Returns the full child ID string (e.g., "parent-id.3").
func GetNextChildIDTx(ctx context.Context, tx *sql.Tx, parentID string) (string, error) {
	var lastChild int
	err := tx.QueryRowContext(ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", parentID).Scan(&lastChild)
	if err == sql.ErrNoRows {
		lastChild = 0
	} else if err != nil {
		return "", fmt.Errorf("get next child ID: read counter: %w", err)
	}

	// Check existing children to prevent overwrites after JSONL import (GH#2166).
	// The counter may be stale if issues were imported without reconciling child_counters.
	var maxExisting sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT MAX(CAST(SUBSTRING_INDEX(id, '.', -1) AS UNSIGNED))
		FROM issues
		WHERE id LIKE CONCAT(?, '.%')
		  AND id NOT LIKE CONCAT(?, '.%.%')
	`, parentID, parentID).Scan(&maxExisting)
	if err != nil {
		return "", fmt.Errorf("get next child ID: scan existing children: %w", err)
	}
	if maxExisting.Valid && int(maxExisting.Int64) > lastChild {
		lastChild = int(maxExisting.Int64)
	}

	nextChild := lastChild + 1

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE last_child = ?
	`, parentID, nextChild, nextChild); err != nil {
		return "", fmt.Errorf("get next child ID: update counter: %w", err)
	}

	return fmt.Sprintf("%s.%d", parentID, nextChild), nil
}
