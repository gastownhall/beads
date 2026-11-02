package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// suspendTransactionCommit turns @@dolt_transaction_commit off on db's
// session for the migration pass (gastownhall/beads#4137).
//
// With it on, Dolt commits every SQL transaction as it ends, so each
// autocommitted statement of a migration body lands in HEAD on its own. The
// frozen 0040/0041 bodies then reach their inline DOLT_COMMIT('-Am', ...)
// with nothing left to commit and abort with "nothing to commit", leaving a
// fresh database stuck below LatestVersion; the runner's own per-step and
// terminal commit boundaries are bypassed the same way. The bodies are
// checksummed, so they cannot be edited to pass --skip-empty.
//
// The setting is session-scoped: the pass must run on one pinned session
// (see MigrateUp). A session that already has it off issues no SET.
//
// The returned restore puts the session's prior value back and must run on
// every return from the pass; it hands passErr back unchanged when the
// restore succeeds. It runs on its own bounded context so a canceled caller
// still gets its session back, and a pinned *sql.Conn it cannot restore is
// discarded rather than returned to the pool with the setting off.
func suspendTransactionCommit(ctx context.Context, db DBConn) (restore func(passErr error) error, err error) {
	var enabled int64
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&enabled); err != nil {
		return nil, fmt.Errorf("reading session dolt_transaction_commit: %w", err)
	}
	if enabled == 0 {
		return func(passErr error) error { return passErr }, nil
	}
	if _, err := db.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 0"); err != nil {
		return nil, fmt.Errorf("suspending session dolt_transaction_commit: %w", err)
	}
	return func(passErr error) error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationLockCleanupTimeout)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, fmt.Sprintf("SET @@SESSION.dolt_transaction_commit = %d", enabled)); err != nil {
			if conn, ok := db.(*sql.Conn); ok {
				discardConn(conn)
			}
			return errors.Join(passErr, fmt.Errorf("restoring session dolt_transaction_commit: %w", err))
		}
		return passErr
	}, nil
}
