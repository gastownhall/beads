package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// suspendTransactionCommit temporarily disables session-level Dolt transaction
// commit on the migration pass's pinned session and returns the function that
// restores the prior setting.
//
// Why: with @@SESSION.dolt_transaction_commit enabled (SET PERSIST
// dolt_transaction_commit=1 on the server, gastownhall/beads#4137), every
// statement inside a migration body commits itself immediately. The shipped
// SQL predates that mode: migrations 0040 and 0041 carry their own
// CALL DOLT_COMMIT('-Am', ...) after inline DML, and the runner commits each
// step itself (commitMigrationStep). A body whose writes were already
// auto-committed dies at its inline commit with "nothing to commit", so the
// schema never reaches LatestVersion and the next open retries forever.
// Suspending the mode for one pass restores the commit boundaries the frozen
// bodies were written against, without editing their checksummed SQL or
// tolerating a batch-level error that would skip the rest of a body.
//
// A session with the setting already disabled (the default) pays one read and
// no SET, and the returned restoration is a no-op; a NULL read likewise means
// no mode is in play for this session. Any other read or SET error aborts
// before the pass: proceeding blindly could re-create exactly the wedge this
// exists to prevent.
//
// The restoration runs on its own bounded context, not the caller's, so a
// canceled caller still restores it (the ReleaseMigrationLock pattern). A
// restoration failure discards an unrestorable *sql.Conn so a session left in
// the suspended state cannot return to the pool.
func suspendTransactionCommit(ctx context.Context, db DBConn) (func() error, error) {
	var setting sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&setting); err != nil {
		return nil, fmt.Errorf("reading @@SESSION.dolt_transaction_commit: %w", err)
	}
	if !setting.Valid {
		return func() error { return nil }, nil
	}
	switch strings.ToLower(strings.TrimSpace(setting.String)) {
	case "0", "false", "off":
		return func() error { return nil }, nil
	case "1", "true", "on":
	default:
		return nil, fmt.Errorf("reading @@SESSION.dolt_transaction_commit: unsupported value %q", setting.String)
	}
	if _, err := db.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 0"); err != nil {
		return nil, fmt.Errorf("suspending @@SESSION.dolt_transaction_commit: %w", err)
	}
	return func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationLockCleanupTimeout)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx, "SET @@SESSION.dolt_transaction_commit = 1"); err != nil {
			if conn, ok := db.(*sql.Conn); ok {
				discardConn(conn)
			}
			return fmt.Errorf("restoring @@SESSION.dolt_transaction_commit: %w", err)
		}
		return nil
	}, nil
}
