package issueops

import (
	"context"
	"database/sql"
	"strings"
)

// IsActiveWispInTx checks whether the given ID exists in the wisps table
// within an existing transaction. Returns true if the ID is found.
// This handles both auto-generated wisp IDs (containing "-wisp-") and
// ephemeral issues created with explicit IDs that were routed to wisps.
func IsActiveWispInTx(ctx context.Context, tx *sql.Tx, id string) bool {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
}

// PartitionByWispStatusInTx splits issue IDs into wisp and permanent (non-wisp)
// groups using batch queries instead of per-ID lookups. This avoids N+1 query
// patterns that are catastrophic over high-latency connections.
func PartitionByWispStatusInTx(ctx context.Context, tx *sql.Tx, ids []string) (wispIDs, permIDs []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}

	wispSet := make(map[string]bool, len(ids))
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]

		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}

		query := "SELECT id FROM wisps WHERE id IN (" + strings.Join(placeholders, ",") + ")"
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			// If wisps table doesn't exist, all IDs are permanent.
			if isTableNotExistError(err) {
				return nil, ids, nil
			}
			return nil, nil, err
		}

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, err
			}
			wispSet[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}

	for _, id := range ids {
		if wispSet[id] {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}
	return wispIDs, permIDs, nil
}

// WispTableRouting returns the appropriate issue, label, event, and dependency
// table names based on whether the ID is an active wisp. Call IsActiveWispInTx
// first to determine isWisp.
func WispTableRouting(isWisp bool) (issueTable, labelTable, eventTable, depTable string) {
	if isWisp {
		return "wisps", "wisp_labels", "wisp_events", "wisp_dependencies"
	}
	return "issues", "labels", "events", "dependencies"
}
