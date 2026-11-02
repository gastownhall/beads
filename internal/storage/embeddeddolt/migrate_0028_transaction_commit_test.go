//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigration0028CompletesUnderDoltTransactionCommit reproduces
// gastownhall/beads#4137: a store at schema 27 on a server running
// @@dolt_transaction_commit=1 must apply migration 0028 and record its cursor
// row, not die with "nothing to commit". 0028's own inline commit is gone,
// but the surviving failure moved to the frozen 0040/0041 bodies, whose inline
// DOLT_COMMIT('-Am', ...) find nothing left to commit once each statement has
// auto-committed; the pass must still reach LatestVersion.
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
