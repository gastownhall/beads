//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestDetailBatchReaderContract checks the shared body through the leg accessor.
func TestDetailBatchReaderContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "dbr")
	ctx := t.Context()
	kit := newEmbeddedRoleFixtureKit(te, "dbr")
	batch, err := te.store.DetailBatchReader()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	fixture := conformance.DetailBatchReaderFixture{
		IssuePrefix: kit.IssuePrefix, DetailBatchReader: batch, Reader: reader,
		CreateIssue: kit.CreateIssue, CreateWisp: kit.CreateWisp,
		DirectServer: false, UOW: false,
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return err
			}
			defer func() { _ = cleanup() }()
			conn, err := db.Conn(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			for _, stmt := range statements {
				if _, err := conn.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
					return fmt.Errorf("%s: %w", stmt.Query, err)
				}
			}
			return nil
		},
	}
	t.Run("Parity", func(t *testing.T) { conformance.RunDetailBatchReaderParity(t, ctx, fixture) })
	t.Run("ChunkBoundaries", func(t *testing.T) { conformance.RunDetailBatchReaderChunkBoundaries(t, ctx, fixture) })
}
