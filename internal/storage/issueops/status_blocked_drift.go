package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// openBlockersIDsSQL selects, uncorrelated with any outer row, every depTable
// issue_id that currently has at least one open (non-closed, non-pinned)
// 'blocks'-type dependency target. This is the same has-a-blocker test
// GetNewlyUnblockedByCloseInTx already applies when it decides an issue
// counts as "newly unblocked" — reused here to detect the be-ntbxt drift
// instead: status='blocked' rows for which this set does NOT contain the id.
//
//nolint:gosec // G201: depTable is a hardcoded constant from the two callers below.
func openBlockersIDsSQL(depTable string) string {
	return fmt.Sprintf(`
		SELECT d.issue_id FROM %[1]s d
		JOIN issues t ON t.id = d.depends_on_issue_id
		WHERE d.issue_id IS NOT NULL
		  AND d.type = 'blocks'
		  AND t.status <> 'closed' AND t.status <> 'pinned'
		UNION
		SELECT d.issue_id FROM %[1]s d
		JOIN wisps t ON t.id = d.depends_on_wisp_id
		WHERE d.issue_id IS NOT NULL
		  AND d.type = 'blocks'
		  AND t.status <> 'closed' AND t.status <> 'pinned'
	`, depTable)
}

// countStatusBlockedDriftSQL counts rows in table left at status='blocked'
// despite having no id in openBlockersIDsSQL(depTable) — the manual status
// enum (internal/types/types.go:511) disagreeing with the dependency graph
// because the blocker closed, or because none was ever recorded at all.
//
//nolint:gosec // G201: table and depTable are hardcoded constants from the two callers below.
func countStatusBlockedDriftSQL(table, depTable string) string {
	return fmt.Sprintf(`
		SELECT COUNT(*) FROM %[1]s
		WHERE status = 'blocked'
		  AND id NOT IN (%[2]s)
	`, table, openBlockersIDsSQL(depTable))
}

// selectStatusBlockedDriftSQL is the id-listing analog of
// countStatusBlockedDriftSQL: same membership test, naming the drifted rows
// instead of counting them, so the fix can correct and journal them one at a
// time (see fixStatusBlockedDriftInTable).
//
//nolint:gosec // G201: table and depTable are hardcoded constants from the two callers below.
func selectStatusBlockedDriftSQL(table, depTable string) string {
	return fmt.Sprintf(`
		SELECT id FROM %[1]s
		WHERE status = 'blocked'
		  AND id NOT IN (%[2]s)
	`, table, openBlockersIDsSQL(depTable))
}

// fixOneStatusBlockedDriftSQL is fixStatusBlockedDriftInTable's per-row
// UPDATE. It repeats the full membership test (not just id = ?) so a row
// rescued between the SELECT and this UPDATE — re-blocked on a fresh
// dependency in the same transaction's view — matches nothing and is skipped
// rather than clobbered. This never touches is_blocked — that column is
// separately derived and repaired by RecomputeAllIsBlockedInTx.
//
//nolint:gosec // G201: table and depTable are hardcoded constants from the two callers below.
func fixOneStatusBlockedDriftSQL(table, depTable string) string {
	return fmt.Sprintf(`
		UPDATE %[1]s
		SET status = 'open'
		WHERE id = ? AND status = 'blocked'
		  AND id NOT IN (%[2]s)
	`, table, openBlockersIDsSQL(depTable))
}

// fixStatusBlockedDriftActor is recorded as the actor on both the audit event
// and the journal entry for each row this repair returns to status='open'.
// A constant, not the invoking session's actor — mirroring DeferWakeActor —
// because the transition happens because the manual status disagreed with
// the dependency graph, not because a person edited it (be-ntbxt).
const fixStatusBlockedDriftActor = "bd-status-blocked-drift-fix"

// CountStatusBlockedDriftInTx is the read-only detection behind the bd doctor
// "Status Blocked Drift" check and the 'bd recompute-blocked --status' flag
// (be-ntbxt): issues/wisps whose manually-set status='blocked' disagrees with
// the dependency graph, either because every 'blocks' target has since closed
// or because none was ever recorded. The repair is FixStatusBlockedDriftInTx.
func CountStatusBlockedDriftInTx(ctx context.Context, tx DBTX) (int64, error) {
	var total int64

	n, err := countRows(ctx, tx, countStatusBlockedDriftSQL("issues", "dependencies"))
	if err != nil {
		return 0, fmt.Errorf("count status=blocked drift: issues: %w", err)
	}
	total += n

	n, err = countRows(ctx, tx, countStatusBlockedDriftSQL("wisps", "wisp_dependencies"))
	if err != nil {
		if isTableNotExistError(err) {
			return total, nil
		}
		return 0, fmt.Errorf("count status=blocked drift: wisps: %w", err)
	}
	total += n

	return total, nil
}

// FixStatusBlockedDriftInTx returns every drifted status='blocked' row (see
// CountStatusBlockedDriftInTx) to status='open' and reports how many rows it
// corrected. A legitimately blocked row — status='blocked' with a still-open
// 'blocks' target — is never touched: the membership test is identical to
// the count, so a converged database (count == 0) leaves this a no-op.
//
// This does not decide whether 'bd close' should perform this transition
// automatically; that is out of scope for be-ntbxt (a manual status may mean
// "blocked on something not modeled as a dependency"). This is the detector
// and repair the fix spec calls for regardless of how that question is
// eventually settled.
func FixStatusBlockedDriftInTx(ctx context.Context, tx DBTX) (int64, error) {
	var total int64

	n, err := fixStatusBlockedDriftInTable(ctx, tx, "issues", "dependencies", "events")
	if err != nil {
		return total, fmt.Errorf("fix status=blocked drift: issues: %w", err)
	}
	total += n

	n, err = fixStatusBlockedDriftInTable(ctx, tx, "wisps", "wisp_dependencies", "wisp_events")
	if err != nil {
		return total, fmt.Errorf("fix status=blocked drift: wisps: %w", err)
	}
	total += n

	return total, nil
}

// fixStatusBlockedDriftInTable corrects one table's drifted rows (see
// FixStatusBlockedDriftInTx) and journals each one it actually changes. It
// snapshots the drifted ids first, then re-verifies and updates them one at a
// time via fixOneStatusBlockedDriftSQL — the same select-then-recheck shape
// as wakeExpiredDefersInTable — so only rows still genuinely drifted at
// UPDATE time are counted, journaled, and returned to status='open'.
func fixStatusBlockedDriftInTable(ctx context.Context, tx DBTX, table, depTable, eventsTable string) (int64, error) {
	rows, err := tx.QueryContext(ctx, selectStatusBlockedDriftSQL(table, depTable))
	if err != nil {
		if isTableNotExistError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("select drifted rows: %w", err)
	}
	var drifted []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan drifted row: %w", err)
		}
		drifted = append(drifted, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate drifted rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close drifted rows: %w", err)
	}

	var fixed int64
	for _, id := range drifted {
		res, err := tx.ExecContext(ctx, fixOneStatusBlockedDriftSQL(table, depTable), id)
		if err != nil {
			return fixed, fmt.Errorf("update %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fixed, fmt.Errorf("rows affected for %s: %w", id, err)
		}
		if n == 0 {
			continue // no longer drifted by the time we reached it — leave it be
		}
		if err := RecordFullEventInTable(ctx, tx, eventsTable, id, types.EventStatusChanged,
			fixStatusBlockedDriftActor, string(types.StatusBlocked), string(types.StatusOpen)); err != nil {
			return fixed, fmt.Errorf("record status=blocked drift fix event for %s: %w", id, err)
		}
		if err := RecordEventInTx(ctx, tx, EventUpdate, id, fixStatusBlockedDriftActor); err != nil {
			return fixed, err
		}
		fixed++
	}
	return fixed, nil
}
