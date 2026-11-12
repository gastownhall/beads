package issueops

import (
	"context"
	"database/sql"
	"fmt"
)

// childCounterBucket is one parent's child-suffix reservation: the highest
// child suffix a pass saw for it, and whether its storage plane is already
// known. known=false means the plane must be resolved from the database by
// applyChildCounterBuckets.
type childCounterBucket struct {
	maxChild int
	isWisp   bool
	known    bool
}

// applyChildCounterBuckets routes every bucketed parent to its storage plane
// and raises its counter to the bucket's maxChild, returning which counter
// tables changed. It is the apply half ReconcileChildCounters has always run,
// extracted so reservation entries (ReserveChildSuffixesInTx) share the exact
// same plane routing (the PARENT's plane, via WispIDSetInTx), parent-existence
// check and GREATEST upsert.
func applyChildCounterBuckets(ctx context.Context, tx DBTX, parents map[string]*childCounterBucket) (map[string]bool, error) {
	unknownParentIDs := make([]string, 0, len(parents))
	for parentID, b := range parents {
		if b.maxChild > 0 && !b.known {
			unknownParentIDs = append(unknownParentIDs, parentID)
		}
	}
	wispParents, err := WispIDSetInTx(ctx, tx, unknownParentIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to route child counter parents: %w", err)
	}
	for _, parentID := range unknownParentIDs {
		_, parents[parentID].isWisp = wispParents[parentID]
	}

	var changed map[string]bool
	for parentID, b := range parents {
		if b.maxChild == 0 {
			continue
		}
		table := "child_counters"
		parentTable := "issues"
		if b.isWisp {
			table = "wisp_child_counters"
			parentTable = "wisps"
		}
		var parentExists int
		// Orphaned hierarchical IDs are valid import input when the parent was
		// deleted before export. Their auxiliary counter has no owner and must
		// not be inserted: both counter tables enforce a parent foreign key.
		//nolint:gosec // G201: parentTable is one of two hardcoded constants.
		err := tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT 1 FROM %s WHERE id = ?
		`, parentTable), parentID).Scan(&parentExists)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to check child counter parent %s: %w", parentID, err)
		}
		var current int
		//nolint:gosec // G201: table is one of two hardcoded constants.
		err = tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT last_child FROM %s WHERE parent_id = ?
		`, table), parentID).Scan(&current)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("failed to read child counter for %s: %w", parentID, err)
		}
		if err == nil && current >= b.maxChild {
			continue
		}
		// Qualify the existing-row column with the table name so the canonical
		// MySQL form and SQLite's translated ON CONFLICT form both unambiguously
		// refer to the target row rather than the incoming value.
		//nolint:gosec // G201: table is one of two hardcoded constants.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %[1]s (parent_id, last_child) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE last_child = GREATEST(%[1]s.last_child, ?)
		`, table), parentID, b.maxChild, b.maxChild); err != nil {
			return nil, fmt.Errorf("failed to reconcile child counter for %s: %w", parentID, err)
		}
		if changed == nil {
			changed = map[string]bool{}
		}
		changed[table] = true
	}
	return changed, nil
}

// ReserveChildSuffixesInTx raises the child counter of every immediate parent
// of ids to cover the highest child suffix those ids name, so a suffix an
// operation removes or renames away is never minted again while the parent
// survives (GH#4750: child_counters must be a high-water mark, not a hint).
// A grandchild id (P.2.1) reserves on its IMMEDIATE parent (P.2) — the same
// split ParseHierarchicalID gives the mint path. departing names parents this
// operation is deleting: their buckets are skipped rather than written, so a
// delete never mints a counter row for a parent that is going away.
// It is a no-op for ids without a hierarchical suffix.
func ReserveChildSuffixesInTx(ctx context.Context, tx DBTX, ids []string, departing map[string]bool) (map[string]bool, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	parents := make(map[string]*childCounterBucket, len(ids))
	for _, id := range ids {
		parentID, childNum, ok := ParseHierarchicalID(id)
		if !ok || childNum <= 0 {
			continue
		}
		if departing != nil && departing[parentID] {
			continue
		}
		b, exists := parents[parentID]
		if !exists {
			b = &childCounterBucket{}
			parents[parentID] = b
		}
		if childNum > b.maxChild {
			b.maxChild = childNum
		}
	}
	if len(parents) == 0 {
		return nil, nil
	}
	return applyChildCounterBuckets(ctx, tx, parents)
}
