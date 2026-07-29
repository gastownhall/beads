package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// CheckVersionInTx reads the current row_lock (the RowVersion token) for id and
// returns ErrVersionMismatch (wrapped with both values) when it differs from
// expected. Routes to the issues or wisps table. Returns ErrNotFound when the
// row is absent. The read shares the caller's transaction, so pairing it with a
// mutation in the same tx yields a true compare-and-swap.
//
// The CAS has two limbs, and the read-side check here is the first: it refuses
// any writer that committed a row_lock change BEFORE the close's transaction
// began (the version it reads is already stale). The second limb lives on the
// retry-wrapped permanent close path — a writer that commits DURING the close's
// transaction collides on this same row_lock cell at commit time, which
// withRetryTx replays; the replayed attempt then re-reads the new version here
// and refuses. Together they close the read-then-write window that a bare
// read-then-write would leave open.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func CheckVersionInTx(ctx context.Context, tx DBTX, id string, expected int64) error {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, _, _ := WispTableRouting(isWisp)

	// row_lock is NOT NULL DEFAULT 0, but scan defensively so a NULL maps to 0
	// rather than erroring (mirrors scan.go's RowVersion handling).
	var current sql.NullInt64
	err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT row_lock FROM %s WHERE id = ?", issueTable), id,
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("failed to read row version for %s: %w", id, err)
	}
	if current.Int64 != expected {
		return fmt.Errorf("%w: expected %d, got %d", storage.ErrVersionMismatch, expected, current.Int64)
	}
	return nil
}
