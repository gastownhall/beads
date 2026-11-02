package schema

import (
	"context"
	"fmt"
)

// suspendTransactionCommit turns off the session's @@dolt_transaction_commit
// for a migration pass (GH#4470). Frozen migrations 0040 and 0041 end their
// statements with in-body CALL DOLT_COMMIT; with automatic transaction commits
// on, every preceding statement has already been committed, so the in-body
// commit fails with "nothing to commit" and a fresh init dies mid-pass. A
// session already at 0 is left alone.
func suspendTransactionCommit(ctx context.Context, db DBConn) error {
	var enabled int64
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&enabled); err != nil {
		return fmt.Errorf("reading @@dolt_transaction_commit: %w", err)
	}
	if enabled == 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 0"); err != nil {
		return fmt.Errorf("suspending @@dolt_transaction_commit for schema migration: %w", err)
	}
	return nil
}
