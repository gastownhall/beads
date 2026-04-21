package issueops

import (
	"context"
	"database/sql"
	"fmt"
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

// PartitionWispIDsInTx partitions a set of IDs into wisp vs non-wisp buckets
// using a single batched `SELECT id FROM wisps WHERE id IN (...)` query per
// queryBatchSize chunk, rather than one round-trip per ID. This is critical
// for remote backends (Dolt) where per-ID round-trips multiply WAN latency
// and can push bulk hydration past the context deadline (see GH#3414).
// IDs not present in the wisps table are treated as permanent issue IDs.
// Returned slices preserve the input ordering within each bucket.
func PartitionWispIDsInTx(ctx context.Context, tx *sql.Tx, ids []string) (wispIDs, permIDs []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}

	wispSet := make(map[string]struct{}, len(ids))
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		//nolint:gosec // G201: only ? placeholders in the IN clause.
		rows, qErr := tx.QueryContext(ctx,
			fmt.Sprintf("SELECT id FROM wisps WHERE id IN (%s)", strings.Join(placeholders, ",")),
			args...)
		if qErr != nil {
			// Wisps table may not exist yet on older schemas — treat as "no wisps".
			if isTableNotExistError(qErr) {
				return nil, append([]string(nil), ids...), nil
			}
			return nil, nil, fmt.Errorf("partition wisp ids: %w", qErr)
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				_ = rows.Close()
				return nil, nil, fmt.Errorf("partition wisp ids: scan: %w", scanErr)
			}
			wispSet[id] = struct{}{}
		}
		_ = rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return nil, nil, fmt.Errorf("partition wisp ids: rows: %w", rowsErr)
		}
	}

	for _, id := range ids {
		if _, ok := wispSet[id]; ok {
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
