package schema

import (
	"context"
	"fmt"
)

// sessionTransactionCommitVar is the Dolt system variable that makes Dolt
// commit every SQL transaction at its own boundary, instead of leaving the
// session's working set to be committed explicitly. On a session (or server)
// with it enabled, the unguarded in-body CALL DOLT_COMMIT calls that shipped
// migrations 0040/0041 still contain fail with "nothing to commit" — the
// statements before each call auto-committed their own changes, so the call's
// staged set is empty and the migration aborts schema initialization
// (gastownhall/beads#4470; the reporter's 0028 instance of the same shape was
// fixed in v1.0.5 by removing 0028's in-body commit).
//
// The migration pass needs default commit semantics while it runs: its runner
// owns every commit boundary (per-step commitMigrationStep commits, the
// end-of-pass commit, and the frozen bodies' explicit CALLs). Migration bodies
// are frozen (their hashes are pinned), so the pass temporarily suspends the
// variable on the pinned migration connection instead of editing SQL.
const (
	sessionTransactionCommitVar   = "dolt_transaction_commit"
	readSessionTransactionCommit  = "SELECT @@SESSION." + sessionTransactionCommitVar
	clearSessionTransactionCommit = "SET @@SESSION." + sessionTransactionCommitVar + " = 0"
)

// suspendMigrationTransactionCommit reads the pinned connection's
// dolt_transaction_commit session setting and, when automatic transaction
// commits are enabled, sets it to 0 for the duration of the migration pass.
// The returned restore function puts the original value back; it must be
// called (typically deferred) on every path that acquired the suspension.
//
// A disabled setting yields a no-op restore that issues no statement, so the
// default server pays for one SELECT only. A read or SET failure fails the
// pass: continuing with unknown commit semantics would let the pass's commits
// land at boundaries the runner does not own.
func suspendMigrationTransactionCommit(ctx context.Context, db DBConn) (func(context.Context) error, error) {
	var orig int64
	if err := db.QueryRowContext(ctx, readSessionTransactionCommit).Scan(&orig); err != nil {
		return nil, fmt.Errorf("reading session %s: %w", sessionTransactionCommitVar, err)
	}
	if orig == 0 {
		return func(context.Context) error { return nil }, nil
	}
	if _, err := db.ExecContext(ctx, clearSessionTransactionCommit); err != nil {
		return nil, fmt.Errorf("disabling session %s for the migration pass: %w", sessionTransactionCommitVar, err)
	}
	restore := func(ctx context.Context) error {
		if _, err := db.ExecContext(ctx,
			fmt.Sprintf("SET @@SESSION.%s = %d", sessionTransactionCommitVar, orig)); err != nil {
			return fmt.Errorf("restoring session %s to %d: %w", sessionTransactionCommitVar, orig, err)
		}
		return nil
	}
	return restore, nil
}
