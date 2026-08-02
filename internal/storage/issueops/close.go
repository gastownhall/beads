package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

const closeCheckedSavepointPrefix = "issueops_close_checked_"

var closeCheckedSavepointCounter atomic.Uint64

// CloseResult holds the result of a CloseIssueInTx call.
type CloseResult struct {
	IsWisp        bool
	AlreadyClosed bool
	OpenChildren  int
	// IssueRowsChanged reports whether this close changed a durable issues row,
	// either directly or while recomputing a dependent's blocked state.
	IssueRowsChanged bool
}

// CloseIssueInTx closes an issue within a transaction, setting status to closed
// and recording the close event. Routes to the correct table (issues/wisps)
// automatically. The caller is responsible for Dolt versioning if needed.
func CloseIssueInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string) (*CloseResult, error) {
	return closeIssueInTx(ctx, tx, id, reason, actor, session, true)
}

func CloseIssueWithoutEventInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string) (*CloseResult, error) {
	return closeIssueInTx(ctx, tx, id, reason, actor, session, false)
}

// CloseIssueCheckedInTx closes an issue within a transaction, refusing with
// storage.ErrCloseOpenChildren when it has open parent-child dependents and
// storage.ErrCloseBlocked when it has a LIVE direct blocker unless force is set.
// The guard (IsBlockedInTx) and the close (CloseIssueInTx) share the SAME
// transaction, so no blocker can clear between the check and the close.
//
// The refuse predicate is the exact historical `bd close` guard:
// blocked && len(blockers) > 0 — refuse only when the denormalized is_blocked
// column is set AND there is at least one live, open direct blocker
// (blocks/waits-for/conditional-blocks). A bare is_blocked=1 with no live direct
// blocker is deliberately NOT refused: it is either a purely transitive block
// (a parent-child child of a blocked parent — historically closable) or a stale
// is_blocked column whose direct blockers have since closed. Reading the live
// blocker list self-heals against a stale column instead of acting on it.
//
// When expectedVersion is non-nil it adds an ORTHOGONAL optimistic-concurrency
// precondition: the row's current RowVersion (row_lock) must still equal
// *expectedVersion or the close refuses with storage.ErrVersionMismatch. This
// runs first — before the is_blocked guard and before force short-circuits it —
// so force bypasses the child and blocker policies, never the version check. Because
// the read shares this transaction, a mismatch returns before any write and the
// transaction rolls back with the issue unchanged (a true compare-and-swap).
// row_lock only tracks lifecycle/ownership writes (status, assignee, started_at),
// so this guards against a concurrent lifecycle change — not against concurrent
// label, dependency, rename, or is_blocked writes that leave row_lock untouched
// (see the freshRowLock invariant in lease.go).
func CloseIssueCheckedInTx(ctx context.Context, tx DBTX, id, reason, actor, session string, force bool, expectedVersion *int64) (*CloseResult, error) {
	if expectedVersion != nil {
		if err := CheckVersionInTx(ctx, tx, id, *expectedVersion); err != nil {
			return nil, err
		}
	}
	// Read the target before policy checks. A closed target can still have open
	// children, which refuses without Force just like an open target.
	closed, targetColumn, found, err := isClosedInTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	// domain/db also supplies a *sql.DB Runner, whose independently pooled
	// statements cannot retain a savepoint. The shared UOW uses a pinned
	// *sql.Conn after START TRANSACTION, while embedded callers use *sql.Tx.
	if !closeCheckedSavepointEligible(tx) {
		return closeIssueCheckedAfterSavepoint(ctx, tx, id, reason, actor, session, force, closed, targetColumn)
	}
	savepoint, err := createCloseCheckedSavepoint(ctx, tx)
	if err != nil {
		return nil, err
	}
	result, scopedErr := closeIssueCheckedAfterSavepoint(ctx, tx, id, reason, actor, session, force, closed, targetColumn)
	if scopedErr != nil {
		if cleanupErr := rollbackAndReleaseCloseCheckedSavepoint(ctx, tx, savepoint); cleanupErr != nil {
			return nil, fmt.Errorf("discard checked close savepoint after %v: %w", scopedErr, cleanupErr)
		}
		return nil, scopedErr
	}
	if err := releaseCloseCheckedSavepoint(ctx, tx, savepoint); err != nil {
		if rollbackErr := rollbackToCloseCheckedSavepoint(ctx, tx, savepoint); rollbackErr != nil {
			return nil, fmt.Errorf("release checked close savepoint: %v; rollback to savepoint: %w", err, rollbackErr)
		}
		return nil, err
	}
	return result, nil
}

func closeCheckedSavepointEligible(tx DBTX) bool {
	switch tx.(type) {
	case *sql.Tx, *sql.Conn:
		return true
	default:
		return false
	}
}

func closeIssueCheckedAfterSavepoint(ctx context.Context, tx DBTX, id, reason, actor, session string, force, closed bool, targetColumn string) (*CloseResult, error) {
	openChildren, err := enforceClosePolicyForTargetInTx(ctx, tx, id, targetColumn, force, closed)
	if err != nil {
		return nil, err
	}
	result, err := CloseIssueInTx(ctx, tx, id, reason, actor, session)
	if err != nil {
		return nil, err
	}
	result.OpenChildren = openChildren
	return result, nil
}

// enforceClosePolicyForTargetInTx applies the close policy — the open-children
// refusal and the live-direct-blocker refusal — to an already-routed target,
// and reports the open-child count it observed. It is the whole policy half of
// a checked close, split out so a status change that reaches done through
// another route can run the identical gate rather than a second copy of it.
//
// closed is the target's pre-write closed state and targetColumn its dependency
// routing, both read by the caller from the same transaction.
func enforceClosePolicyForTargetInTx(ctx context.Context, tx DBTX, id, targetColumn string, force, closed bool) (int, error) {
	// This write precedes every child-count policy branch, including Force and
	// idempotent closes. A concurrent new parent-child edge writes its own tier
	// cell, while a close writes both tiers, so Dolt cannot merge a stale count
	// with a new edge.
	if err := touchDependencyCoordinationInTx(ctx, tx, id); err != nil {
		return 0, err
	}
	openChildren, err := countOpenChildrenForTargetInTx(ctx, tx, id, targetColumn)
	if err != nil {
		return 0, err
	}
	if !force && openChildren > 0 {
		return 0, &storage.CloseOpenChildrenError{IssueID: id, OpenChildren: openChildren}
	}
	if !force && !closed {
		blocked, blockers, err := IsBlockedInTx(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		if blocked && len(blockers) > 0 {
			return 0, fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, id, blockers)
		}
	}
	return openChildren, nil
}

// EnforceClosePolicyInTx applies the close policy to id without closing it, for
// a caller that reaches a done status by some other write. It routes the target
// exactly as a checked close does — probing issues then wisps by ID, preferring
// the durable row when both hold it — rather than deriving the table from an
// is-wisp flag, so a dual-resident ID counts children against the same row the
// close guard would.
//
// It returns the observed open-child count. force bypasses both refusals and
// nothing else; the coordination-cell touch happens either way.
func EnforceClosePolicyInTx(ctx context.Context, tx DBTX, id string, force bool) (int, error) {
	closed, targetColumn, found, err := isClosedInTx(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	return enforceClosePolicyForTargetInTx(ctx, tx, id, targetColumn, force, closed)
}

func countOpenChildrenInTx(ctx context.Context, tx DBTX, id string) (int, error) {
	targetColumn, err := dependencyTargetColumnForIDInTx(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	return countOpenChildrenForTargetInTx(ctx, tx, id, targetColumn)
}

func countOpenChildrenForTargetInTx(ctx context.Context, tx DBTX, id, targetColumn string) (int, error) {
	if targetColumn != "depends_on_issue_id" && targetColumn != "depends_on_wisp_id" {
		return 0, fmt.Errorf("count open children: unsupported target column %q", targetColumn)
	}
	var durableCount int
	//nolint:gosec // G201: targetColumn is validated above against two hardcoded identifiers.
	durableQuery := fmt.Sprintf(`
		SELECT COUNT(DISTINCT dependency.issue_id)
		FROM dependencies AS dependency
		JOIN issues AS child ON child.id = dependency.issue_id
		WHERE dependency.%s = ?
		  AND dependency.type = 'parent-child'
		  AND child.status != 'closed'
	`, targetColumn)
	if err := tx.QueryRowContext(ctx, durableQuery, id).Scan(&durableCount); err != nil {
		return 0, fmt.Errorf("count open durable children for %s: %w", id, err)
	}

	var wispCount int
	//nolint:gosec // G201: targetColumn is validated above against two hardcoded identifiers.
	wispQuery := fmt.Sprintf(`
		SELECT COUNT(DISTINCT dependency.issue_id)
		FROM wisp_dependencies AS dependency
		JOIN wisps AS child ON child.id = dependency.issue_id
		WHERE dependency.%s = ?
		  AND dependency.type = 'parent-child'
		  AND child.status != 'closed'
		  AND NOT EXISTS (
			SELECT 1 FROM dependencies AS durable WHERE durable.id = dependency.id
		  )
	`, targetColumn)
	if err := tx.QueryRowContext(ctx, wispQuery, id).Scan(&wispCount); err != nil {
		if optionalBlockedTable("wisp_dependencies") && isTableNotExistError(err) {
			return durableCount, nil
		}
		return 0, fmt.Errorf("count open wisp children for %s: %w", id, err)
	}
	return durableCount + wispCount, nil
}

func createCloseCheckedSavepoint(ctx context.Context, tx DBTX) (string, error) {
	name := closeCheckedSavepointPrefix + strconv.FormatUint(closeCheckedSavepointCounter.Add(1), 10)
	//nolint:gosec // G201: name is a fixed identifier-safe prefix plus an atomic decimal counter.
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return "", fmt.Errorf("create checked close savepoint: %w", err)
	}
	return name, nil
}

func rollbackAndReleaseCloseCheckedSavepoint(ctx context.Context, tx DBTX, name string) error {
	rollbackErr := rollbackToCloseCheckedSavepoint(ctx, tx, name)
	releaseErr := releaseCloseCheckedSavepoint(ctx, tx, name)
	return errors.Join(rollbackErr, releaseErr)
}

func rollbackToCloseCheckedSavepoint(ctx context.Context, tx DBTX, name string) error {
	//nolint:gosec // G201: name is generated by createCloseCheckedSavepoint.
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name); err != nil {
		return fmt.Errorf("rollback checked close savepoint: %w", err)
	}
	return nil
}

func releaseCloseCheckedSavepoint(ctx context.Context, tx DBTX, name string) error {
	//nolint:gosec // G201: name is generated by createCloseCheckedSavepoint.
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); err != nil {
		return fmt.Errorf("release checked close savepoint: %w", err)
	}
	return nil
}

// dependencyTargetColumnForIDInTx returns the typed target column for id,
// preferring a durable row when an id is present in both tables.
func dependencyTargetColumnForIDInTx(ctx context.Context, tx DBTX, id string) (string, error) {
	var found int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM issues WHERE id = ?", id).Scan(&found)
	if err == nil {
		return "depends_on_issue_id", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("classify child-count target in issues: %w", err)
	}
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ?", id).Scan(&found)
	if err == nil {
		return "depends_on_wisp_id", nil
	}
	if optionalBlockedTable("wisps") && isTableNotExistError(err) {
		return "depends_on_issue_id", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("classify child-count target in wisps: %w", err)
	}
	return "depends_on_issue_id", nil
}

// isClosedInTx reports whether the issue (or wisp) identified by id exists and
// is already closed. It probes the issues table then the optional wisps table,
// mirroring IsBlockedInTx's table order.
//
//nolint:gosec // G201: table is a hardcoded "issues" or "wisps".
func isClosedInTx(ctx context.Context, tx DBTX, id string) (closed bool, targetColumn string, found bool, err error) {
	for _, target := range []struct {
		table  string
		column string
	}{
		{table: "issues", column: "depends_on_issue_id"},
		{table: "wisps", column: "depends_on_wisp_id"},
	} {
		var status string
		err := tx.QueryRowContext(ctx, "SELECT status FROM "+target.table+" WHERE id = ?", id).Scan(&status)
		if err == nil {
			return types.Status(status) == types.StatusClosed, target.column, true, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if optionalBlockedTable(target.table) && isTableNotExistError(err) {
			continue
		}
		return false, "", false, fmt.Errorf("read status from %s: %w", target.table, err)
	}
	return false, "", false, nil
}

//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func closeIssueInTx(ctx context.Context, tx DBTX, id string, reason, actor, session string, recordEvent bool) (*CloseResult, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	var affectedIssues, affectedWisps []string
	var aerr error
	if isWisp {
		affectedIssues, affectedWisps, aerr = AffectedByStatusChangeForWispInTx(ctx, tx, id)
	} else {
		affectedIssues, affectedWisps, aerr = AffectedByStatusChangeInTx(ctx, tx, id)
	}
	if aerr != nil {
		return nil, fmt.Errorf("affected by close for %s: %w", id, aerr)
	}

	now := time.Now().UTC()

	// row_lock is rewritten on close so a concurrent reclaim (which also rewrites
	// row_lock) collides on this cell and is forced to conflict-and-retry rather
	// than silently cell-merging a revert-to-ready over a completed close (see
	// lease.go). The lease row is deleted below: a closed issue holds no lease.
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?,
			row_lock = ?
		WHERE id = ? AND status != ?
	`, issueTable), types.StatusClosed, now, now, reason, session, freshRowLock(), id, types.StatusClosed)
	if err != nil {
		return nil, fmt.Errorf("failed to close issue: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		var status string
		qerr := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT status FROM %s WHERE id = ?`, issueTable), id,
		).Scan(&status)
		if qerr == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
		}
		if qerr != nil {
			return nil, fmt.Errorf("failed to check issue existence: %w", qerr)
		}
		if types.Status(status) == types.StatusClosed {
			return &CloseResult{IsWisp: isWisp, AlreadyClosed: true}, nil
		}
		return nil, fmt.Errorf("failed to close issue: %s", id)
	}

	// A closed issue holds no lease (no-op for wisps, which are never leased).
	if err := DeleteLeaseInTx(ctx, tx, id); err != nil {
		return nil, err
	}

	if recordEvent {
		if err := RecordEventInTable(ctx, tx, eventTable, id, types.EventClosed, actor, reason); err != nil {
			return nil, fmt.Errorf("failed to record event: %w", err)
		}
	}

	recompute, err := RecomputeIsBlockedInTxWithResult(ctx, tx, affectedIssues, affectedWisps)
	if err != nil {
		return nil, fmt.Errorf("recompute is_blocked after close for %s: %w", id, err)
	}

	return &CloseResult{IsWisp: isWisp, IssueRowsChanged: !isWisp || recompute.IssueRowsChanged}, nil
}
