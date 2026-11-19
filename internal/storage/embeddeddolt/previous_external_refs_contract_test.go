//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/storagecontract"
)

// TestPreviousExternalRefsContract runs the batch external_ref history
// contract against the embedded store, the backend GH#6605 measured: one
// tracker pull reopened the engine and scanned history once per linked issue.
// Exec and the oracle open their own engine, so a change they make is one
// completed outside the store's read path.
func TestPreviousExternalRefsContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "per")
	ctx := t.Context()

	storagecontract.RunPreviousExternalRefsContract(t, ctx, storagecontract.PreviousExternalRefsFixture{
		Prefix: "per",
		Refs:   te.store,
		Exec: func(ctx context.Context, stmt string, args ...any) error {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return err
			}
			defer cleanup()
			_, err = db.ExecContext(ctx, stmt, args...)
			return err
		},
		ReadTx: func(ctx context.Context, fn func(*sql.Tx) error) error {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return err
			}
			defer cleanup()
			tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			return fn(tx)
		},
	})
}
