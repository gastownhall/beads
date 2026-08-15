package issueops

import (
	"context"
	"testing"

	publicops "github.com/steveyegge/beads/issueops"
)

// TestExecuteLifecycleVerbsTakeDBTX pins the seam that lets both Lifecycle
// adapters reach one body. The store adapters pass *sql.Tx. The unit-of-work
// adapter passes domain/db.Runner. Both satisfy DBTX. A *sql.Tx-only
// signature is what forced the unit-of-work adapter to re-orchestrate create,
// update, close, and reopen.
func TestExecuteLifecycleVerbsTakeDBTX(t *testing.T) {
	t.Parallel()
	// Compile-time pin only. A *sql.Tx-only signature fails to assign.
	var (
		_ func(context.Context, DBTX, publicops.CreateRequest) (publicops.CreateResult, ChangedTables, error) = ExecuteCreate
		_ func(context.Context, DBTX, publicops.UpdateRequest) (publicops.UpdateResult, ChangedTables, error) = ExecuteUpdate
		_ func(context.Context, DBTX, publicops.CloseRequest) (publicops.CloseResult, ChangedTables, error)   = ExecuteClose
		_ func(context.Context, DBTX, publicops.ReopenRequest) (publicops.ReopenResult, ChangedTables, error) = ExecuteReopen
	)
}
