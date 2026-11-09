package versioncontrolops

import (
	"context"
	"fmt"
	"strconv"
)

// Child-counter conflict settlement (GH#4796, bd-28sb25): child_counters is a
// high-water mark, not an editable field, so a conflict where both sides
// present a counter for the SAME parent is settled at max(ours, theirs) — the
// only value a counter converges to no matter which clone's children came
// first. Two offline clones each mint children under the same parent and each
// advance the same row's last_child, and before this settlement that
// both-changed cell aborted the pull for the operator even after the
// colliding children themselves were renumbered away.
//
// Declined (the operator's) shapes, in the pre-screen below: a row missing on
// one side (deletion is not zero), parent keys that differ, and a last_child
// that is not a nonnegative integer. The decline leaves dolt_conflicts intact
// so the caller builds the MergeConflictsError that names child_counters.

// childCounterKeyColumns are child_counters' primary-key columns, used both
// for the duplicate-key decline and to identify each conflict row.
var childCounterKeyColumns = []string{"parent_id"}

// childCounterMax is one pre-screened counter conflict's settlement: the
// parent whose counter row it is, and the max of the two sides' last_child.
type childCounterMax struct {
	parentID  string
	lastChild int64
}

// mergeChildCountersConflictRow decides one child_counters conflict row.
// ok is false when the row is not a both-present same-parent counter
// conflict: a side without a row (deletion is not zero), parent keys that
// disagree, or a last_child that is not a nonnegative integer.
//
// It is pure, so every rule is unit-testable without a database.
func mergeChildCountersConflictRow(row rawConflictRow) (childCounterMax, bool) {
	ourParent, ourOK := row.value("our", "parent_id")
	theirParent, theirOK := row.value("their", "parent_id")
	if !ourOK || !theirOK || ourParent == nil || theirParent == nil {
		// One side deleted the row: an add/delete conflict, not a counter.
		return childCounterMax{}, false
	}
	ourParentStr := formatConflictValue(ourParent)
	theirParentStr := formatConflictValue(theirParent)
	if ourParentStr == nil || theirParentStr == nil || *ourParentStr != *theirParentStr {
		// Not the same counter row on both sides.
		return childCounterMax{}, false
	}
	ourLast, ok := parseChildCounterValue(row, "our", "last_child")
	if !ok {
		return childCounterMax{}, false
	}
	theirLast, ok := parseChildCounterValue(row, "their", "last_child")
	if !ok {
		return childCounterMax{}, false
	}
	// The base value is irrelevant: modify/modify and add/add alike settle to
	// the max, which is the counter both sides' allocations already reached.
	max := ourLast
	if theirLast > max {
		max = theirLast
	}
	return childCounterMax{parentID: *ourParentStr, lastChild: max}, true
}

// parseChildCounterValue reads one side's last_child cell as a nonnegative
// integer. ok is false for NULL (the side deleted the row or the column is
// missing), for any value that does not parse as an integer, and for a
// negative one: a negative high-water mark is a corrupted or synthetic value,
// not a counter bd's write paths can produce, and "max" with one is as likely
// to lower a healthy counter as to raise a broken one.
func parseChildCounterValue(row rawConflictRow, side, col string) (int64, bool) {
	v, has := row.value(side, col)
	if !has || v == nil {
		return 0, false
	}
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

// childCountersConflictsAreMaxMergeable reports whether every live
// child_counters conflict can be settled at max, and returns the settlement
// plan so the resolution pass does not recompute it. The pre-screen reads
// every row before any is resolved, so one declined row declines the whole
// table — a partially settled merge must never be committed.
func childCountersConflictsAreMaxMergeable(ctx context.Context, db DBConn) ([]childCounterMax, bool, error) {
	rows, err := loadConflictRows(ctx, db, "child_counters")
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	if declineDuplicateConflictRows("child_counters", childCounterKeyColumns, rows) {
		return nil, false, nil
	}
	plan := make([]childCounterMax, 0, len(rows))
	for _, row := range rows {
		settled, ok := mergeChildCountersConflictRow(row)
		if !ok {
			return nil, false, nil
		}
		plan = append(plan, settled)
	}
	return plan, true, nil
}

// resolveChildCountersMaxConflicts applies a plan from
// childCountersConflictsAreMaxMergeable: write each max over OUR working-set
// row, then delete the conflict row — the delete is what tells dolt the row
// is settled (the same manual-resolution idiom as resolveIssuesFieldMerge).
func resolveChildCountersMaxConflicts(ctx context.Context, db DBConn, plan []childCounterMax) error {
	for _, m := range plan {
		if _, err := db.ExecContext(ctx,
			"UPDATE child_counters SET last_child = ? WHERE parent_id = ?", m.lastChild, m.parentID); err != nil {
			return fmt.Errorf("settle child counter for %s to %d: %w", m.parentID, m.lastChild, err)
		}
		res, err := db.ExecContext(ctx,
			"DELETE FROM dolt_conflicts_child_counters WHERE our_parent_id = ?", m.parentID)
		if err != nil {
			return fmt.Errorf("clear child_counters conflict for %s: %w", m.parentID, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("child_counters conflict for %s was not cleared (no conflict row deleted)", m.parentID)
		}
	}
	// child_counters is staged by the caller's shared DOLT_ADD loop.
	return nil
}
