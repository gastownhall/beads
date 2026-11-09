package versioncontrolops

import (
	"context"
	"fmt"
	"strconv"
)

// child_counters merge settlement (bd-28sb25, GH#4796).
//
// child_counters.last_child is a high-water mark: the largest child suffix a
// clone has allocated under parent_id. Two clones creating children under the
// same parent between syncs both advance it, so the row conflicts on every
// such pull, and before this the conflict aborted the pull for the operator
// even when no child id collided. The convergent answer is the maximum of the
// two sides — a counter only grows, and taking the larger one never lets a
// later allocation reuse a suffix either side handed out.
//
// Only that shape is merged: the row present on BOTH sides (modify/modify, or
// add/add when the parent's first children were minted on both clones) with a
// valid nonnegative last_child on each. A side with no row is a deletion
// (cascade from the parent's deletion, or a repair), never zero, and a value
// that does not parse is never guessed at: both decline to the operator.

// childCountersKeyColumn is the child_counters primary key.
const childCountersKeyColumn = "parent_id"

// childCountersValueColumn is the high-water-mark column merged to max.
const childCountersValueColumn = "last_child"

// childCounterMerge is the settlement of one conflicted child_counters row.
type childCounterMerge struct {
	parentID  any
	lastChild int64
	// write reports that the merged value differs from our working-set
	// value, so the row must be rewritten before the conflict is cleared.
	write bool
}

// parseCounterValue reads a last_child cell as a nonnegative integer.
func parseCounterValue(v any) (int64, bool) {
	s := formatConflictValue(v)
	if s == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(*s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// mergeChildCountersConflictRow computes the max-merge of one conflicted
// child_counters row; ok is false when the row must be left for the operator.
// It is pure, so every rule is unit-testable without a database.
func mergeChildCountersConflictRow(row rawConflictRow) (childCounterMerge, bool) {
	_, ourOK, theirOK := row.sidesPresent([]string{childCountersKeyColumn})
	if !ourOK || !theirOK {
		return childCounterMerge{}, false
	}
	ourKey, _ := row.value("our", childCountersKeyColumn)
	theirKey, _ := row.value("their", childCountersKeyColumn)
	if !conflictCellsEqual(ourKey, theirKey) {
		return childCounterMerge{}, false
	}
	// Any other data column is a shape this rule does not understand; it may
	// only ride along when both sides agree on it.
	for _, col := range row.dataColumns(childCountersKeyColumn, childCountersValueColumn) {
		ourVal, ourHas := row.value("our", col)
		theirVal, theirHas := row.value("their", col)
		if !ourHas || !theirHas || !conflictCellsEqual(ourVal, theirVal) {
			return childCounterMerge{}, false
		}
	}
	ourRaw, ourHas := row.value("our", childCountersValueColumn)
	theirRaw, theirHas := row.value("their", childCountersValueColumn)
	if !ourHas || !theirHas {
		return childCounterMerge{}, false
	}
	ours, ok := parseCounterValue(ourRaw)
	if !ok {
		return childCounterMerge{}, false
	}
	theirs, ok := parseCounterValue(theirRaw)
	if !ok {
		return childCounterMerge{}, false
	}
	return childCounterMerge{
		parentID:  ourKey,
		lastChild: max(ours, theirs),
		write:     theirs > ours,
	}, true
}

// childCountersConflictsAreMaxMergeable reports whether every live
// child_counters conflict can be settled by mergeChildCountersConflictRow,
// and returns the plan so the resolution pass does not recompute it.
func childCountersConflictsAreMaxMergeable(ctx context.Context, db DBConn) ([]childCounterMerge, bool, error) {
	rows, err := loadConflictRows(ctx, db, "child_counters")
	if err != nil {
		return nil, false, err
	}
	if declineDuplicateConflictRows("child_counters", []string{childCountersKeyColumn}, rows) {
		return nil, false, nil
	}
	plan := make([]childCounterMerge, 0, len(rows))
	for _, row := range rows {
		merged, ok := mergeChildCountersConflictRow(row)
		if !ok {
			return nil, false, nil
		}
		plan = append(plan, merged)
	}
	return plan, true, nil
}

// resolveChildCountersMax applies a plan from
// childCountersConflictsAreMaxMergeable through dolt's manual-resolution
// path, as resolveIssuesFieldMerge does: write the maximum over our row when
// theirs is larger, then DELETE the conflict row, which marks it settled.
func resolveChildCountersMax(ctx context.Context, db DBConn, plan []childCounterMerge) error {
	for _, m := range plan {
		if m.parentID == nil {
			return fmt.Errorf("unexpected child_counters conflict row with no parent_id (safety check bypassed)")
		}
		if m.write {
			res, err := db.ExecContext(ctx,
				"UPDATE child_counters SET last_child = ? WHERE parent_id = ?", m.lastChild, m.parentID)
			if err != nil {
				return fmt.Errorf("apply merged child counter for %v: %w", m.parentID, err)
			}
			if n, err := res.RowsAffected(); err != nil || n == 0 {
				present, err := conflictTargetStillPresent(ctx, db, "child_counters", childCountersKeyColumn, m.parentID)
				if err != nil {
					return fmt.Errorf("confirm child counter %v still exists after merge write: %w", m.parentID, err)
				}
				if !present {
					return fmt.Errorf("merged child counter for %v matched no row (was it deleted concurrently?); conflict left unresolved", m.parentID)
				}
			}
		}
		res, err := db.ExecContext(ctx,
			"DELETE FROM dolt_conflicts_child_counters WHERE our_parent_id = ?", m.parentID)
		if err != nil {
			return fmt.Errorf("clear child counter conflict for %v: %w", m.parentID, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("child counter conflict for %v was not cleared (no conflict row deleted)", m.parentID)
		}
	}
	return nil
}
