package issueops

import (
	"context"
	"fmt"
	"time"
)

// The durable events table (claimed, status_changed, updated, ... rows with
// their old_value/new_value) has no retention of its own: the journal
// (bd_events_journal) is bounded by its floors, but events rows accrue for as
// long as the issue lives. `bd gc` prunes them past a cutoff through these two
// statements; both the direct store and the proxied-server route run exactly
// this text so the phase means the same thing on every backend.
//
// Pruning a Dolt-versioned table frees rows, not disk: the deleted rows stay
// reachable from the commits that held them until those commits are squashed
// (bd compact / bd flatten) and GC'd, which is why the phase runs ahead of
// compact and GC in `bd gc`.
const (
	// CountEventsBeforeSQL counts the events rows a prune at this cutoff would
	// delete. The one placeholder is the cutoff, a UTC DATETIME.
	CountEventsBeforeSQL = "SELECT COUNT(*) FROM events WHERE created_at < ?"
	// PruneEventsBeforeSQL deletes them. Same placeholder.
	PruneEventsBeforeSQL = "DELETE FROM events WHERE created_at < ?"
)

// CountEventsBeforeInTx reports how many events rows were created before the
// cutoff.
func CountEventsBeforeInTx(ctx context.Context, tx DBTX, before time.Time) (int64, error) {
	var n int64
	if err := tx.QueryRowContext(ctx, CountEventsBeforeSQL, before.UTC()).Scan(&n); err != nil {
		return 0, fmt.Errorf("count events before %s: %w", before.UTC().Format(time.RFC3339), err)
	}
	return n, nil
}

// PruneEventsBeforeInTx deletes the events rows created before the cutoff and
// returns how many went. It does not commit: the caller owns the transaction
// and the Dolt commit that publishes the delete.
func PruneEventsBeforeInTx(ctx context.Context, tx DBTX, before time.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, PruneEventsBeforeSQL, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune events before %s: %w", before.UTC().Format(time.RFC3339), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune events before %s: rows affected: %w", before.UTC().Format(time.RFC3339), err)
	}
	return n, nil
}
