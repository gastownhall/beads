//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigration0028CompletesUnderDoltTransactionCommit reproduces
// gastownhall/beads#4137: a store at schema 27 on a server running
// @@dolt_transaction_commit=1 must apply migration 0028 and record its cursor
// row, not die with "nothing to commit".
//
// 0028's own body no longer commits inline at this main; with the session
// variable enabled the pass instead died at 0040/0041, whose frozen bodies
// still carry CALL DOLT_COMMIT after inline DML that autocommit had already
// landed (operator-confirmed on dolt sql-server 2.6.0 with
// SET PERSIST dolt_transaction_commit=1). The fix suspends the session's
// transaction-commit mode for the duration of the pass, so this regression
// asserts the store converges to LatestVersion, not merely past one file.
func TestEmbeddedMigration0028CompletesUnderDoltTransactionCommit(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dir := seedMainSchemaAt(t, ctx, 27)
	conn, closeConn := openPinnedConn(t, ctx, dir)
	defer closeConn()

	if _, err := conn.ExecContext(ctx, "SET @@dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp from 27 under dolt_transaction_commit=1: %v", err)
	}

	var has28 int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 28").Scan(&has28); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if has28 != 1 {
		t.Fatalf("schema_migrations version 28 rows = %d, want 1", has28)
	}
	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
}
