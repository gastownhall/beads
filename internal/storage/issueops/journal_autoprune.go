package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// The substrate half of bounded-by-default retention: resolving the automatic
// prune target, and deleting it a capped batch at a time.
//
// The POLICY is not here and is not new. Auto-prune's target is "everything the
// active retention floors do not protect", which is ComputeEventsPruneWhere
// with before = head+1 — an unbounded request that the floors narrow down. A
// second floor implementation is exactly the drift the centralization in
// journal_prune.go exists to prevent, so this file computes nothing about
// retention itself; it supplies the head, hands the same floor readers to the
// same resolver, and executes the bound it gets back.
//
// The throttle that decides WHEN to run, and the configuration that decides
// whether to run at all, live in internal/eventsjournal.

const (
	// EventsAutoPruneBatchRows is how many rows one auto-prune transaction
	// deletes. Big enough that a normal backlog clears in one batch (a busy
	// workspace commits a few thousand mutations a day, so 10k is more than the
	// daily churn the 7-day floor leaves behind), small enough that the delete
	// is a short transaction rather than one that holds the journal table for
	// the length of a full compaction.
	EventsAutoPruneBatchRows = 10000

	// EventsAutoPruneMaxBatches caps one auto-prune invocation. Three batches
	// bound the work a single user command can be made to pay for at 30k rows;
	// a larger backlog — a journal that was left enabled and unconsumed for
	// months, or one whose floors were just lowered — drains over the next few
	// invocations instead of stalling this one. This is SQLite's WAL
	// auto-checkpoint bargain: the writer pays, but never without a ceiling.
	EventsAutoPruneMaxBatches = 3
)

// ComputeEventsAutoPruneBoundInTx resolves the automatic retention target: the
// exclusive seq bound below which nothing is protected by the active floors. It
// reports skip=true when there is nothing to delete, which includes the case
// that matters most — BOTH floors disabled, meaning the operator asked for an
// unbounded ledger and maintenance must not touch it.
//
// before = head+1 is what makes this "everything the floors do not protect":
// head is the highest seq ever assigned, so head+1 is an exclusive bound above
// every existing row, and every narrowing from there is a floor doing its job.
// With no floor to narrow it, ComputeEventsPruneWhere returns that bound
// unchanged — which is why the both-floors-disabled case is refused HERE rather
// than delegated: to the resolver, "delete everything" is a legitimate answer to
// an explicit `bd events prune --before <head+1>`, and only the caller knows
// this request came from maintenance nobody asked for.
func ComputeEventsAutoPruneBoundInTx(ctx context.Context, tx DBTX, retainDays, retainRows int, now time.Time) (bound int64, skip bool, err error) {
	if retainDays <= 0 && retainRows <= 0 {
		return 0, true, nil
	}
	head, err := readEventsHeadInTx(ctx, tx)
	if err != nil {
		return 0, false, err
	}
	if head <= 0 {
		return 0, true, nil
	}
	readRowsCeil, readDaysFloor := eventsPruneFloorReadersInTx(ctx, tx, retainRows)
	_, args, skip, err := ComputeEventsPruneWhere(head+1, retainDays, retainRows, now, readRowsCeil, readDaysFloor)
	if err != nil || skip {
		return 0, skip, err
	}
	// The resolver renders its answer as a predicate plus binds because that is
	// what its callers execute; the single bind IS the resolved bound (see
	// BuildEventsPruneWhere, the one renderer). Reading it back beats computing
	// the same number a second way.
	resolved, ok := args[0].(int64)
	if !ok {
		return 0, false, fmt.Errorf("journal: auto-prune bound has unexpected type %T", args[0])
	}
	return resolved, false, nil
}

// PruneEventsBatchInTx deletes at most limit rows below bound and returns how
// many it deleted. It runs inside the caller's transaction; a driver calls it
// repeatedly, each call in a FRESH transaction, until it deletes fewer than
// limit or the batch cap is reached.
//
// ORDER BY seq keeps every batch a strict PREFIX delete, which is the invariant
// the whole feature rests on: a consumer can be told "records below F are gone"
// and nothing else. Without the ordering the engine is free to delete an
// arbitrary limit-sized subset of the matching rows, punching holes in the
// middle of the retained window that only the read-side interior-gap sweep
// would ever notice. See ComputeEventsTruncation.
//
// limit is a compile-time constant rather than a bind because the read path
// renders its LIMIT the same way (readEventsRowsInTx) and because a bound
// parameter in a LIMIT clause is the one placeholder position engines disagree
// about.
func PruneEventsBatchInTx(ctx context.Context, tx DBTX, bound int64, limit int) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("journal: auto-prune batch limit must be positive, got %d", limit)
	}
	where, args := BuildEventsPruneWhere(bound)
	q := "DELETE FROM bd_events_journal WHERE " + where + " ORDER BY seq ASC LIMIT " + strconv.Itoa(limit)
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("journal: auto-prune batch below %d: %w", bound, err)
	}
	return res.RowsAffected()
}

// ReadEventsAutoPruneStateInTx reads both halves of the auto-prune throttle in
// ONE round trip: the persisted watermark from the clone-local metadata slot,
// and the journal's current head from the seq counter.
//
// One query is the point. This read runs after every journaled mutation in a
// workspace with the journal on, and the overwhelmingly common answer is
// "nothing is due" — so the not-due path must cost a single indexed lookup, not
// a conversation. The counter rides along as a scalar subquery because the
// volume half of the throttle ("has the journal advanced far enough to be worth
// a pass?") cannot be answered without it.
//
// A missing slot row reads as ("", head): never pruned here, which the throttle
// treats as due. A missing counter row reads as head 0: nothing has ever been
// journaled, so there is nothing to prune either way.
func ReadEventsAutoPruneStateInTx(ctx context.Context, tx DBTX, slotKey string) (watermark string, head int64, err error) {
	const q = "SELECT (SELECT value FROM local_metadata WHERE `key` = ?), " +
		"(SELECT next_seq FROM bd_events_seq WHERE id = 0)"
	var (
		slot     sql.NullString
		headNull sql.NullInt64
	)
	scanErr := tx.QueryRowContext(ctx, q, slotKey).Scan(&slot, &headNull)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return "", 0, nil
	}
	if scanErr != nil {
		return "", 0, fmt.Errorf("journal: read auto-prune state: %w", scanErr)
	}
	return slot.String, headNull.Int64, nil
}

// SetEventsAutoPruneStateInTx persists the throttle watermark.
func SetEventsAutoPruneStateInTx(ctx context.Context, tx DBTX, slotKey, watermark string) error {
	return SetLocalMetadataInTx(ctx, tx, slotKey, watermark)
}

// EventsMaintenanceRunner runs one unit of journal maintenance inside a FRESH
// transaction that commits when fn returns nil. Every write plumbing that can
// journal implements it: the embedded store, the server-mode store, and the
// unit-of-work provider.
//
// One transaction per call, owned by the plumbing, is the contract. Auto-prune
// must never join the mutation transaction it follows — maintenance that could
// roll back a user's write, or extend its lock window, is worse than no
// maintenance — and its own deletes must land in bounded pieces rather than one
// long transaction. Both properties come from this shape rather than from any
// discipline at the call site.
//
// Implementations must commit into the working set WITHOUT minting a version
// control commit: the journal tables are dolt_ignored engine state, and a
// maintenance commit in a workspace's history would be noise at best.
type EventsMaintenanceRunner interface {
	RunEventsMaintenanceTx(ctx context.Context, fn func(context.Context, DBTX) error) error
}
