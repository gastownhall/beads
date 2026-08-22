package issueops

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

// DeferWakeActor is the actor recorded on the status_changed event when the
// wake sweep returns an expired dated defer to open. A constant rather than
// the invoking session's actor: the wake is the system honoring the defer
// date, not something the reader who happened to trigger it did.
const DeferWakeActor = "bd-defer-wake"

// WakeDefersResult reports what one sweep woke, per table.
type WakeDefersResult struct {
	// Issues are the permanent-table issue ids returned to open; only these
	// affect Dolt-versioned tables, so only these decide whether the caller
	// mints a dolt commit.
	Issues []string
	// Wisps are the woken wisp ids. Wisp tables are dolt_ignored, so a
	// wisp-only wake never needs a version commit.
	Wisps []string
}

// WakeDefersCommitMessage names a sweep's dolt commit. n is the number of
// permanent issues woken (len(result.Issues)); callers with n == 0 should not
// commit at all.
func WakeDefersCommitMessage(n int) string {
	return fmt.Sprintf("bd: wake %d expired defer(s)", n)
}

// WakeExpiredDefersInTx returns every DATED defer whose date has passed to
// open: status='deferred' AND defer_until <= now flips to status='open',
// defer_until=NULL — byte-identical to what `bd undefer` writes, so a later
// dateless `bd defer` cannot inherit a stale past date and instantly re-wake.
// A DATELESS defer (defer_until IS NULL) is the indefinite icebox and is
// deliberately never touched: `bd undefer` stays its only exit.
//
// This is the lazy half of the defer contract. `bd defer --until` and
// `bd update --defer` promise "hidden until <date>", but nothing ever flipped
// the status back, so an expired defer stayed invisible to the ready front
// forever. Callers run this sweep at the top of ready-work reads and claims;
// the snapshot-then-recheck shape mirrors ReclaimExpiredLeasesInTx, so a bead
// re-deferred or claimed between the snapshot and its UPDATE is simply skipped.
//
// The caller owns Dolt versioning (commit iff len(result.Issues) > 0) and must
// treat sweep failure as advisory — a ready listing never fails because the
// wake could not run.
func WakeExpiredDefersInTx(ctx context.Context, tx DBTX) (WakeDefersResult, error) {
	var result WakeDefersResult
	issues, err := wakeExpiredDefersInTable(ctx, tx, "issues", "events")
	if err != nil {
		return result, err
	}
	result.Issues = issues
	// Wisps carry the same status/defer_until columns and `bd defer` reaches
	// them through the same UpdateIssue routing. The table is tolerated absent
	// for pre-wisp databases, like every other wisp probe.
	wisps, err := wakeExpiredDefersInTable(ctx, tx, "wisps", "wisp_events")
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return result, nil
		}
		return result, err
	}
	result.Wisps = wisps
	return result, nil
}

func wakeExpiredDefersInTable(ctx context.Context, tx DBTX, table, eventsTable string) ([]string, error) {
	// Snapshot first so each genuinely-woken row gets its own event. The
	// UPDATE below repeats the whole predicate, so a row rescued between the
	// SELECT and its UPDATE (re-deferred further out, claimed, closed) matches
	// nothing and is skipped rather than clobbered.
	//nolint:gosec // G201: table is a hardcoded constant from the caller above.
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id FROM %s
		WHERE status = 'deferred' AND defer_until IS NOT NULL
		  AND defer_until <= UTC_TIMESTAMP()
	`, table))
	if err != nil {
		return nil, fmt.Errorf("wake expired defers: scan %s: %w", table, err)
	}
	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("wake expired defers: scan %s row: %w", table, err)
		}
		expired = append(expired, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("wake expired defers: iterate %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("wake expired defers: close %s rows: %w", table, err)
	}
	if len(expired) == 0 {
		return nil, nil
	}

	var woken []string
	now := time.Now().UTC()
	for _, id := range expired {
		// row_lock is rewritten so a concurrent claim/update conflicts at
		// commit time instead of cell-merging with this write — the same
		// invariant the lease scheme depends on.
		//nolint:gosec // G201: table is a hardcoded constant from the caller above.
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET status = 'open', defer_until = NULL, updated_at = ?, row_lock = ?
			WHERE id = ? AND status = 'deferred' AND defer_until IS NOT NULL
			  AND defer_until <= UTC_TIMESTAMP()
		`, table), now, freshRowLock(), id)
		if err != nil {
			return woken, fmt.Errorf("wake expired defer %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return woken, fmt.Errorf("wake expired defer %s rows affected: %w", id, err)
		}
		if n == 0 {
			continue // rescued concurrently — leave it be
		}
		if err := RecordFullEventInTable(ctx, tx, eventsTable, id, types.EventStatusChanged,
			DeferWakeActor, string(types.StatusDeferred), string(types.StatusOpen)); err != nil {
			return woken, fmt.Errorf("record wake event for %s: %w", id, err)
		}
		// A wake is a status change, so it journals as an update. Emitted past
		// the rows-affected re-check, so a concurrently-rescued bead records
		// nothing.
		if err := RecordEventInTx(ctx, tx, EventUpdate, id); err != nil {
			return woken, err
		}
		woken = append(woken, id)
	}
	return woken, nil
}
