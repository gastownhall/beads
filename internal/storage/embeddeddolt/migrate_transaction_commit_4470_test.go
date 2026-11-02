//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests cover GH#4470's surviving case: frozen migrations 0040 and 0041
// carry in-body CALL DOLT_COMMIT statements, and on a session with
// @@dolt_transaction_commit = 1 every preceding statement has already been
// committed, so the in-body commit fails with "nothing to commit" and a fresh
// `bd init` dies mid-pass. MigrateUp suspends the session setting for the
// numbered-migration pass and restores it on return.

// openFreshTransactionCommitConn creates a truly empty database (no schema at
// all) and returns a pinned connection to it with the session's
// @@dolt_transaction_commit set to 1.
func openFreshTransactionCommitConn(t *testing.T, ctx context.Context) *sql.Conn {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		_ = bootCleanup()
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	t.Cleanup(closeConn)
	if _, err := conn.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}
	return conn
}

func sessionTransactionCommit(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	return scalarInt(t, ctx, conn, "SELECT @@SESSION.dolt_transaction_commit")
}

// TestEmbeddedFreshMigrateUnderTransactionCommit_4470 is AC-1: a fresh
// database migrated on a dolt_transaction_commit=1 session reaches the latest
// main and ignored versions, through 0040's and 0041's in-body commits, with a
// clean working set.
func TestEmbeddedFreshMigrateUnderTransactionCommit_4470(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := openFreshTransactionCommitConn(t, ctx)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("fresh MigrateUp under dolt_transaction_commit=1: %v", err)
	}
	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("main schema version = %d, want %d", got, want)
	}
	if got, want := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"), schema.LatestIgnoredVersion(); got != want {
		t.Fatalf("ignored schema version = %d, want %d", got, want)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set after fresh MigrateUp is dirty: %v, want clean", dirty)
	}
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = 'create nonlocal table wisps'"); n != 1 {
		t.Fatalf("0040's 'create nonlocal table wisps' commit count = %d, want 1", n)
	}
}
