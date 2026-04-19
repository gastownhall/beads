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
//
// For multi-ID callers, prefer PartitionByWispInTx or ActiveWispIDsInTx
// below — this single-ID helper issues one round-trip per call and was
// the source of a 2,285-query N+1 on remote bd export before the bulk
// helpers were added.
func IsActiveWispInTx(ctx context.Context, tx *sql.Tx, id string) bool {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
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

// ActiveWispIDsInTx returns the subset of ids that exist in the active
// wisps table, batched at queryBatchSize. This is the primitive — most
// call sites want PartitionByWispInTx below, which preserves input
// ordering. Callers that range the returned map directly must be aware
// Go map iteration order is randomized.
//
// Returns an empty (non-nil) map if ids is empty.
func ActiveWispIDsInTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

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

		//nolint:gosec // G201: placeholders are only "?" literals
		q := fmt.Sprintf("SELECT id FROM wisps WHERE id IN (%s)", strings.Join(placeholders, ","))
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("ActiveWispIDsInTx: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("ActiveWispIDsInTx scan: %w", err)
			}
			result[id] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("ActiveWispIDsInTx rows: %w", err)
		}
		_ = rows.Close()
	}

	return result, nil
}

// PartitionByWispInTx splits ids into (wispIDs, permIDs) by running one
// batched membership probe against the wisps table. The returned slices
// preserve input order within each partition, so JSON export ordering
// (bd export, bd list --json) stays deterministic across refactors.
//
// Callers that need a set primitive should use ActiveWispIDsInTx directly.
func PartitionByWispInTx(ctx context.Context, tx *sql.Tx, ids []string) (wispIDs, permIDs []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	wispSet, err := ActiveWispIDsInTx(ctx, tx, ids)
	if err != nil {
		return nil, nil, err
	}
	wispIDs = make([]string, 0, len(wispSet))
	permIDs = make([]string, 0, len(ids)-len(wispSet))
	for _, id := range ids {
		if wispSet[id] {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}
	return wispIDs, permIDs, nil
}
