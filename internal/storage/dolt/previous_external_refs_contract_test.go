package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/storagecontract"
)

// TestPreviousExternalRefsContract runs the batch external_ref history
// contract against the server-backed store. It shares its body with the
// embedded leg (issueops.PreviousExternalRefsInTx) and differs in the read
// connection: the long-timeout reader History uses, since the same
// dolt_history_issues scan runs under it.
func TestPreviousExternalRefsContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	storagecontract.RunPreviousExternalRefsContract(t, ctx, storagecontract.PreviousExternalRefsFixture{
		Prefix: "per",
		Refs:   store,
		Exec: func(ctx context.Context, stmt string, args ...any) error {
			_, err := store.db.ExecContext(ctx, stmt, args...)
			return err
		},
		ReadTx: func(ctx context.Context, fn func(*sql.Tx) error) error {
			tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			return fn(tx)
		},
	})
}
