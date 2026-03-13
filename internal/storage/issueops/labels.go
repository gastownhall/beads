package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// GetLabelsInTx retrieves all labels for an issue within an existing transaction.
// Returns labels sorted alphabetically.
//
//nolint:gosec // G201: table is caller-controlled ("labels" or "wisp_labels")
func GetLabelsInTx(ctx context.Context, tx *sql.Tx, table, issueID string) ([]string, error) {
	if table == "" {
		table = "labels"
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT label FROM %s WHERE issue_id = ? ORDER BY label`, table), issueID)
	if err != nil {
		return nil, fmt.Errorf("get labels: %w", err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("get labels: scan: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// AddLabelInTx adds a label to an issue and records an event within an existing
// transaction. Uses INSERT IGNORE for idempotency.
//
//nolint:gosec // G201: labelTable/eventTable are caller-controlled constants
func AddLabelInTx(ctx context.Context, tx *sql.Tx, labelTable, eventTable, issueID, label, actor string) error {
	if labelTable == "" {
		labelTable = "labels"
	}
	if eventTable == "" {
		eventTable = "events"
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT IGNORE INTO %s (issue_id, label) VALUES (?, ?)`, labelTable), issueID, label); err != nil {
		return fmt.Errorf("add label: %w", err)
	}
	comment := "Added label: " + label
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (issue_id, event_type, actor, comment) VALUES (?, ?, ?, ?)`, eventTable),
		issueID, types.EventLabelAdded, actor, comment); err != nil {
		return fmt.Errorf("add label: record event: %w", err)
	}
	return nil
}
