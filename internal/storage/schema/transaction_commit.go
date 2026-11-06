package schema

import (
	"context"
	"database/sql"
	"fmt"
)

// suspendTransactionCommit turns off the session's @@dolt_transaction_commit
// for a migration pass (GH#4470) and returns the function that restores the
// original value. Frozen migrations 0040 and 0041 end their statements with
// in-body CALL DOLT_COMMIT; with automatic transaction commits on, every
// preceding statement has already been committed, so the in-body commit fails
// with "nothing to commit" and a fresh init dies mid-pass. A session already
// at 0 is left alone and its restore is a no-op.
//
// The setting is per session, so the caller must run the whole pass on one
// pinned connection (MigrateUpWithLock already requires a *sql.Conn).
func suspendTransactionCommit(ctx context.Context, db DBConn) (restore func() error, err error) {
	if keepTransactionCommitForTest {
		return func() error { return nil }, nil
	}
	var enabled int64
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&enabled); err != nil {
		return nil, fmt.Errorf("reading @@dolt_transaction_commit: %w", err)
	}
	if enabled == 0 {
		return func() error { return nil }, nil
	}
	if _, err := db.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 0"); err != nil {
		return nil, fmt.Errorf("suspending @@dolt_transaction_commit for schema migration: %w", err)
	}
	return func() error {
		// Like ReleaseMigrationLock: the pass's ctx may already be canceled,
		// and the session must not be handed back with the setting altered.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationLockCleanupTimeout)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, fmt.Sprintf("SET @@SESSION.dolt_transaction_commit = %d", enabled)); err != nil {
			// An unrestored session must not go back to the pool for
			// unrelated work to inherit.
			if conn, ok := db.(*sql.Conn); ok {
				discardConn(conn)
			}
			return fmt.Errorf("restoring @@dolt_transaction_commit after schema migration: %w", err)
		}
		return nil
	}, nil
}

// joinRestoreError adds a failed restore to the pass's own result without
// replacing it. Like MigrateUpWithLock's lock release, both go on one line,
// the pass's error first: errors.Join's separator newline would let a
// last-line-only capture drop the migration diagnostic.
func joinRestoreError(err, restoreErr error) error {
	switch {
	case restoreErr == nil:
		return err
	case err == nil:
		return restoreErr
	default:
		// Two %w verbs keep errors.Is/As working for both errors.
		return fmt.Errorf("%w (transaction-commit restore also failed: %w)", err, restoreErr)
	}
}

// keepTransactionCommitForTest is a test-only seam. When true,
// suspendTransactionCommit leaves the session's setting alone, so a test can
// run a pass with automatic transaction commits on, the way a binary without
// the suspension still runs it. Production leaves it false.
var keepTransactionCommitForTest bool

// SetKeepTransactionCommitForTest sets keepTransactionCommitForTest and
// returns a function that restores the previous value. Test-only.
func SetKeepTransactionCommitForTest(keep bool) func() {
	prev := keepTransactionCommitForTest
	keepTransactionCommitForTest = keep
	return func() { keepTransactionCommitForTest = prev }
}
