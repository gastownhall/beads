package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// CheckExpectedFieldsInTx reads the current assignee, status and ownership
// fence for id and returns ErrAssigneeMismatch/ErrStatusMismatch/
// ErrFenceMismatch (wrapped with actual vs expected) when a non-nil guard
// differs — the semantic-field compare-and-swap behind `bd update
// --if-assignee/--if-status/--if-fence` (bd-wsqvw). A non-nil pointer to "" is
// a real guard meaning "expected unassigned", and a non-nil pointer to fence 0
// is a real guard meaning "expected never claimed"; nil disables that check.
// Routes to the issues or wisps table. Returns ErrNotFound when the row is
// absent.
//
// The CAS has the same two limbs as CheckVersionInTx: this read-side check
// refuses a writer that committed before the caller's transaction began, and a
// writer that commits DURING the transaction collides at commit time on the
// row_lock cell (every mutating path rewrites row_lock), which the caller's
// retry loop replays — the replayed attempt re-reads here and refuses.
// Together they close the read-then-write window.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func CheckExpectedFieldsInTx(ctx context.Context, tx DBTX, id string, expectedAssignee, expectedStatus *string, expectedFence *int64) error {
	if expectedAssignee == nil && expectedStatus == nil && expectedFence == nil {
		return nil
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, _, _ := WispTableRouting(isWisp)

	var assignee sql.NullString
	var status string
	var fence int64
	err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT assignee, status, claim_fence FROM %s WHERE id = ?", issueTable), id,
	).Scan(&assignee, &status, &fence)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("failed to read assignee/status for %s: %w", id, err)
	}
	if expectedAssignee != nil && assignee.String != *expectedAssignee {
		return fmt.Errorf("%w: %s is held by %q, expected %q", storage.ErrAssigneeMismatch, id, assignee.String, *expectedAssignee)
	}
	if expectedStatus != nil && status != *expectedStatus {
		return fmt.Errorf("%w: %s has status %q, expected %q", storage.ErrStatusMismatch, id, status, *expectedStatus)
	}
	if expectedFence != nil && fence != *expectedFence {
		return fmt.Errorf("%w: %s has fence %d, expected %d", storage.ErrFenceMismatch, id, fence, *expectedFence)
	}
	return nil
}
