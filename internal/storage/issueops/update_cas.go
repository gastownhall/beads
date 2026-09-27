package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// CheckExpectedFieldsInTx reads the current assignee, status and updated_at
// for id and returns ErrAssigneeMismatch/ErrStatusMismatch/ErrUpdatedAtMismatch
// (wrapped with actual vs expected) when a non-nil guard differs — the
// semantic-field compare-and-swap behind
// `bd update --if-assignee/--if-status/--if-updated-at` (bd-wsqvw). A non-nil
// pointer to "" is a real guard meaning "expected unassigned"; nil disables
// that check. Routes to the issues or wisps table. Returns ErrNotFound when
// the row is absent.
//
// The CAS has the same two limbs as CheckVersionInTx: this read-side check
// refuses a writer that committed before the caller's transaction began, and a
// writer that commits DURING the transaction collides at commit time on the
// row_lock cell (every mutating path rewrites row_lock), which the caller's
// retry loop replays — the replayed attempt re-reads here and refuses.
// Together they close the read-then-write window.
//
// The assignee guard compares under actorMatches, not verbatim ==, so two
// spellings of the same identity (ga-wzl83) don't false-mismatch — the same
// fix already shipped for UnclaimIssueInTx's SQL-CAS predicate and
// AuthorizeAssigneeTransferWithPools; this was the third, previously-split
// verbatim-comparison surface (ga-5ksp5, gate review on #5439).
//
// The updated_at guard is a TRANSACTIONAL READ-COMPARE, not a SQL predicate:
// DATETIME(0) truncation happens on store, so an `AND updated_at = ?` clause
// against a caller's stamp could miss on spelling alone. Reading the stamp in
// the same transaction and comparing Go-side (UpdatedAtStampsEqual) keeps the
// refusal atomic with the update and reports the CURRENT stamp in the error.
// It fences the issues row only — label mutations bypass updated_at by design
// (upstream #5442).
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func CheckExpectedFieldsInTx(ctx context.Context, tx DBTX, id string, expectedAssignee, expectedStatus *string, expectedUpdatedAt *time.Time) error {
	if expectedAssignee == nil && expectedStatus == nil && expectedUpdatedAt == nil {
		return nil
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, _, _ := WispTableRouting(isWisp)

	var assignee sql.NullString
	var status string
	var updatedAtStr sql.NullString
	err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT assignee, status, updated_at FROM %s WHERE id = ?", issueTable), id,
	).Scan(&assignee, &status, &updatedAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("failed to read assignee/status for %s: %w", id, err)
	}
	if expectedAssignee != nil && !actorMatches(assignee.String, *expectedAssignee) {
		return fmt.Errorf("%w: %s is held by %q, expected %q", storage.ErrAssigneeMismatch, id, assignee.String, *expectedAssignee)
	}
	if expectedStatus != nil && status != *expectedStatus {
		return fmt.Errorf("%w: %s has status %q, expected %q", storage.ErrStatusMismatch, id, status, *expectedStatus)
	}
	if expectedUpdatedAt != nil {
		var current time.Time
		if updatedAtStr.Valid {
			current = ParseTimeString(updatedAtStr.String)
		}
		if !publicops.UpdatedAtStampsEqual(current, *expectedUpdatedAt) {
			return publicops.UpdatedAtMismatchError(id, current, *expectedUpdatedAt)
		}
	}
	return nil
}
