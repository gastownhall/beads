package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// The three templates below all decide membership through
// shouldBeBlockedIDsUnionSQL (blocked_consistency.go) — deliberately the very
// same builder is_blocked is derived from, not a second predicate that happens
// to agree today. "Blocked" is defined in exactly one place: an open
// blocks/conditional-blocks target (issue or wisp), a parent-child parent that
// is itself blocked, or a held waits-for gate.
//
// This detector originally carried its own narrower 'blocks'-only union, on the
// reasoning that it was the has-a-blocker test GetNewlyUnblockedByCloseInTx
// already applies. That was the wrong set to borrow: that function only ever
// looks at issues the just-closed bead was a 'blocks' target of, so it never
// had to define blockedness in general. Against the whole table the narrow
// predicate reported every OTHER kind of still-blocked row as drift, and
// --fix then force-opened rows the graph holds blocked while is_blocked stayed
// 1 (gastownhall/beads#6565 review). Do not re-narrow this: the regression is
// invisible until a conditional-blocks, parent-child or waits-for bead is
// force-opened in someone's database.

// countStatusBlockedDriftSQL counts rows in table left at status='blocked'
// despite having no id in shouldBeBlockedIDsUnionSQL(depTable) — the manual
// status enum (internal/types/types.go:511) disagreeing with the dependency
// graph because every blocker closed, or because none was ever recorded.
//
//nolint:gosec // G201: table and depTable are hardcoded constants from the two callers below.
func countStatusBlockedDriftSQL(table, depTable string) string {
	return fmt.Sprintf(`
		SELECT COUNT(*) FROM %[1]s
		WHERE status = 'blocked'
		  AND id NOT IN (%[2]s)
	`, table, shouldBeBlockedIDsUnionSQL(depTable))
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
	`, table, shouldBeBlockedIDsUnionSQL(depTable))
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
	`, table, shouldBeBlockedIDsUnionSQL(depTable))
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
// the dependency graph, either because every blocker has since closed or
// because none was ever recorded. "Blocker" here means whatever
// shouldBeBlockedIDsUnionSQL means by it, so a row the graph still holds
// blocked through conditional-blocks, an inherited parent-child block or a
// waits-for gate is never drift. The repair is FixStatusBlockedDriftInTx.
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
// corrected. A legitimately blocked row — one the dependency graph still holds
// blocked by any of the reasons shouldBeBlockedIDsUnionSQL enumerates — is
// never touched: the membership test is identical to the count, so a converged
// database (count == 0) leaves this a no-op.
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
