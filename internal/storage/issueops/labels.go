package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetLabelsInTx retrieves all labels for an issue within an existing transaction.
// Automatically routes to wisp_labels if the ID is an active wisp.
// Returns labels sorted alphabetically.
func GetLabelsInTx(ctx context.Context, tx *sql.Tx, table, issueID string) ([]string, error) {
	if table == "" {
		isWisp := IsActiveWispInTx(ctx, tx, issueID)
		_, table, _, _ = WispTableRouting(isWisp)
	}
	//nolint:gosec // G201: table is from WispTableRouting ("labels" or "wisp_labels")
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

// GetLabelsForIssuesInTx fetches labels for multiple issues in a single transaction.
// Routes each ID to labels or wisp_labels based on wisp status.
// Uses batched IN clauses (queryBatchSize) to avoid query-planner spikes.
//
// wispSet is an optional pre-built set of active wisp IDs scoped to
// cover issueIDs (see WispIDSetInTx). Pass nil to have the helper build
// a scoped set internally; callers hydrating multiple batches inside
// one tx can build the set once over the union of their IDs and
// reuse it across calls.
func GetLabelsForIssuesInTx(ctx context.Context, tx *sql.Tx, issueIDs []string, wispSet map[string]struct{}) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]string), nil
	}

	if wispSet == nil {
		var err error
		wispSet, err = WispIDSetInTx(ctx, tx, issueIDs)
		if err != nil {
			return nil, fmt.Errorf("get labels for issues: build wisp set: %w", err)
		}
	}

	result := make(map[string][]string)

	wispIDs, permIDs := partitionByWispSet(issueIDs, wispSet)

	for _, pair := range []struct {
		table string
		ids   []string
	}{
		{"wisp_labels", wispIDs},
		{"labels", permIDs},
	} {
		if len(pair.ids) == 0 {
			continue
		}
		for start := 0; start < len(pair.ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(pair.ids) {
				end = len(pair.ids)
			}
			batch := pair.ids[start:end]
			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for i, id := range batch {
				placeholders[i] = "?"
				args[i] = id
			}
			//nolint:gosec // G201: pair.table is hardcoded
			rows, err := tx.QueryContext(ctx, fmt.Sprintf(
				`SELECT issue_id, label FROM %s WHERE issue_id IN (%s) ORDER BY issue_id, label`,
				pair.table, strings.Join(placeholders, ",")), args...)
			if err != nil {
				return nil, fmt.Errorf("get labels for issues from %s: %w", pair.table, err)
			}
			for rows.Next() {
				var issueID, label string
				if err := rows.Scan(&issueID, &label); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("get labels for issues: scan: %w", err)
				}
				result[issueID] = append(result[issueID], label)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("get labels for issues: rows: %w", err)
			}
		}
	}

	return result, nil
}

// AddLabelInTx adds a label to an issue and records an event within an existing
// transaction. Automatically routes to wisp tables if the ID is an active wisp.
// Uses INSERT IGNORE for idempotency.
func AddLabelInTx(ctx context.Context, tx *sql.Tx, labelTable, eventTable, issueID, label, actor string) error {
	if labelTable == "" || eventTable == "" {
		isWisp := IsActiveWispInTx(ctx, tx, issueID)
		_, lt, et, _ := WispTableRouting(isWisp)
		if labelTable == "" {
			labelTable = lt
		}
		if eventTable == "" {
			eventTable = et
		}
	}
	//nolint:gosec // G201: labelTable is from WispTableRouting ("labels" or "wisp_labels")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT IGNORE INTO %s (issue_id, label) VALUES (?, ?)`, labelTable), issueID, label); err != nil {
		return fmt.Errorf("add label: %w", err)
	}
	comment := "Added label: " + label
	//nolint:gosec // G201: eventTable is from WispTableRouting ("events" or "wisp_events")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (issue_id, event_type, actor, comment) VALUES (?, ?, ?, ?)`, eventTable),
		issueID, types.EventLabelAdded, actor, comment); err != nil {
		return fmt.Errorf("add label: record event: %w", err)
	}
	return nil
}

// RemoveLabelInTx removes a label from an issue and records an event within
// an existing transaction. Automatically routes to wisp tables if the ID is
// an active wisp.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func RemoveLabelInTx(ctx context.Context, tx *sql.Tx, labelTable, eventTable, issueID, label, actor string) error {
	if labelTable == "" || eventTable == "" {
		isWisp := IsActiveWispInTx(ctx, tx, issueID)
		_, lt, et, _ := WispTableRouting(isWisp)
		if labelTable == "" {
			labelTable = lt
		}
		if eventTable == "" {
			eventTable = et
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE issue_id = ? AND label = ?`, labelTable), issueID, label); err != nil {
		return fmt.Errorf("remove label: %w", err)
	}
	comment := "Removed label: " + label
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (issue_id, event_type, actor, comment) VALUES (?, ?, ?, ?)`, eventTable),
		issueID, types.EventLabelRemoved, actor, comment); err != nil {
		return fmt.Errorf("remove label: record event: %w", err)
	}
	return nil
}
