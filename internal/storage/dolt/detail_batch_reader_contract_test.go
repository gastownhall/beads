package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestDetailBatchReaderContract checks the shared body through the leg accessor.
func TestDetailBatchReaderContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	kit := newDoltRoleFixtureKit(store, "dbr")
	batch, err := store.DetailBatchReader()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	fixture := conformance.DetailBatchReaderFixture{
		IssuePrefix: kit.IssuePrefix, DetailBatchReader: batch, Reader: reader,
		CreateIssue: kit.CreateIssue, CreateWisp: kit.CreateWisp,
		DirectServer: true, UOW: false,
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			conn, err := store.db.Conn(ctx)
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
