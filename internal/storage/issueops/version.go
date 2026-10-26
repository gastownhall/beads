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
func CheckVersionInTx(ctx context.Context, tx DBTX, id string, expected int64) error {
	current, err := readRowVersionInTx(ctx, tx, id, IsActiveWispInTx(ctx, tx, id))
	if err != nil {
		return err
	}
	if current != expected {
		return &storage.VersionMismatchError{Expected: expected, Current: current}
	}
	return nil
}

// readRowVersionInTx reads id's row_lock from the plane isWisp names.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func readRowVersionInTx(ctx context.Context, tx DBTX, id string, isWisp bool) (int64, error) {
	issueTable, _, _, _ := WispTableRouting(isWisp)

	// row_lock is NOT NULL DEFAULT 0, but scan defensively so a NULL maps to 0
	// rather than erroring (mirrors scan.go's RowVersion handling).
	var current sql.NullInt64
	err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT row_lock FROM %s WHERE id = ?", issueTable), id,
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read row version for %s: %w", id, err)
	}
	return current.Int64, nil
}
