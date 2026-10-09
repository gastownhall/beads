package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestDetailBatchReaderContract checks the shared body through the leg accessor.
func TestDetailBatchReaderContract(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "dbr")
	kit := newUOWRoleFixtureKit(provider, "dbr")
	source, ok := provider.(DetailBatchReaderSource)
	if !ok {
		t.Fatalf("provider %T lacks DetailBatchReaderSource", provider)
	}
	batch, err := source.DetailBatchReader()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := provider.(IssueReaderSource).IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	fixture := conformance.DetailBatchReaderFixture{
		IssuePrefix: kit.IssuePrefix, DetailBatchReader: batch, Reader: reader,
		CreateIssue: kit.CreateIssue, CreateWisp: kit.CreateWisp,
		DirectServer: false, UOW: true,
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return "", fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return "seed detail batch fixture", nil
			})
		},
	}
	t.Run("Parity", func(t *testing.T) { conformance.RunDetailBatchReaderParity(t, ctx, fixture) })
	t.Run("ChunkBoundaries", func(t *testing.T) { conformance.RunDetailBatchReaderChunkBoundaries(t, ctx, fixture) })
}
