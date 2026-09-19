package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// IsActiveWispInTx checks whether the given ID exists in the wisps table
// within an existing transaction. Returns true if the ID is found.
// This handles both auto-generated wisp IDs (containing "-wisp-") and
// ephemeral issues created with explicit IDs that were routed to wisps.
//
// For hot-path callers that partition a batch of IDs by wisp status,
// prefer WispIDSetInTx + partitionByWispSet to amortize the per-ID
// query cost into a single scoped query over the batch.
func IsActiveWispInTx(ctx context.Context, tx DBTX, id string) bool {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
}

func wispsTableEmptyOrMissingInTx(ctx context.Context, tx DBTX) (bool, error) {
	var probe int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM wisps LIMIT 1").Scan(&probe)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case isTableNotExistError(err):
		return true, nil
	default:
		return false, err
	}
}

//nolint:gosec // table is selected by callers from fixed optional wisp tables.
func optionalTableExistsInTx(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var probe int
	err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s LIMIT 1", table)).Scan(&probe)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case isTableNotExistError(err):
		return false, nil
	default:
		return false, err
	}
}

// WispIDSetInTx returns the subset of ids that are currently-active wisps
// within the tx. The set is consistent for the tx's lifetime (Dolt MVCC).
// Intended for hot-path partitioning where a batch of IDs must be split
// into wisps vs permanents; one scoped query amortized over the batch
// replaces N per-ID IsActiveWispInTx calls without paying for a full
// wisps-table scan when callers have a small batch against a large
// wisps table.
//
// Returns an empty set when ids is empty; never issues a query.
//
//nolint:gosec // G201: query uses placeholder-only interpolation
func WispIDSetInTx(ctx context.Context, tx DBTX, ids []string) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	if len(ids) == 0 {
		return set, nil
	}
	if empty, err := wispsTableEmptyOrMissingInTx(ctx, tx); err != nil {
		return nil, fmt.Errorf("wisp id set: probe: %w", err)
	} else if empty {
		return set, nil
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
		q := fmt.Sprintf("SELECT id FROM wisps WHERE id IN (%s)", strings.Join(placeholders, ","))
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("wisp id set: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("wisp id set: scan: %w", err)
			}
			set[id] = struct{}{}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("wisp id set: rows: %w", err)
		}
	}
	return set, nil
}

// partitionByWispSet splits ids into (wispIDs, permIDs) using the provided
// wisp-id set. If wispSet is nil the caller must populate it first via
// WispIDSetInTx; this helper does no I/O.
func partitionByWispSet(ids []string, wispSet map[string]struct{}) (wispIDs, permIDs []string) {
	for _, id := range ids {
		if _, isWisp := wispSet[id]; isWisp {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}
	return wispIDs, permIDs
}

// PartitionWispIDsInTx partitions a set of IDs into wisp vs non-wisp buckets
// using a single batched `SELECT id FROM wisps WHERE id IN (...)` query per
// queryBatchSize chunk, rather than one round-trip per ID. This is critical
// for remote backends (Dolt) where per-ID round-trips multiply WAN latency
// and can push bulk hydration past the context deadline (see GH#3414).
// IDs not present in the wisps table are treated as permanent issue IDs.
// Returned slices preserve the input ordering within each bucket.
func PartitionWispIDsInTx(ctx context.Context, tx DBTX, ids []string) (wispIDs, permIDs []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}

	if empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx); probeErr != nil {
		return nil, nil, fmt.Errorf("partition wisp ids: probe: %w", probeErr)
	} else if empty {
		return nil, append([]string(nil), ids...), nil
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

// DeleteCascadeTables returns every table a DELETE of one issue (or wisp) row
// removes rows from — FK-cascaded or explicit — including the deleted row's
// own table.
//
// For issues the delete only issues `DELETE FROM issues WHERE id = ?` and
// everything else goes through ON DELETE CASCADE foreign keys declared in the
// migrations. A wisp delete additionally makes one EXPLICIT cross-plane
// delete: deleteIssueRowInTx removes the sync-plane dependencies rows whose
// depends_on_wisp_id points at the wisp (there is no FK from dependencies to
// wisps — that is why the delete is explicit). Callers that track dirty tables
// for Dolt staging cannot see either kind — the caller's SQL never names the
// tables — so they have to be enumerated here, or the rows are removed in the
// working set but left out of the version commit (#3807: dolt
// transaction.DeleteIssue marked one table where embeddedTransaction marked
// five, and neither covered the snapshot or counter tables).
//
// Dolt-ignored members (events, and the wisp_* set) are included rather than
// filtered: DirtyTableTracker.MarkDirty already drops wisps/wisp_*, and
// DOLT_ADD on an ignored table is a no-op returning status 0, so listing them
// keeps this an honest description of the delete instead of a description of
// what happens to be stageable today.
func DeleteCascadeTables(isWisp bool) []string {
	if isWisp {
		return []string{
			"wisps",
			"wisp_labels",
			"wisp_events",
			"wisp_comments",
			"wisp_child_counters",
			"wisp_dependencies",
			// Explicit, not a cascade: the sync-plane edges pointing at this
			// wisp (DeleteWispFromDependenciesInTx). Dropping this from the
			// staging set would leave a real versioned deletion uncommitted,
			// to be resurrected by the next hard reset as a dangling edge.
			"dependencies",
		}
	}
	return []string{
		"issues",
		"dependencies",
		"labels",
		"comments",
		"events",
		"provenance_events",
		"child_counters",
		"issue_snapshots",
		"compaction_snapshots",
		// Cross-plane: wisp_dependencies.depends_on_issue_id points at
		// issues(id) ON DELETE CASCADE, so deleting an ordinary issue also
		// drops the wisp edges that depended on it.
		"wisp_dependencies",
	}
}
