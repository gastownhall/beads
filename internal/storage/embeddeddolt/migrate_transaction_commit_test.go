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

// TestEmbeddedMigrateFreshDatabaseUnderDoltTransactionCommit is the
// gastownhall/beads#4137 fresh-init shape: an empty database migrated on a
// session with @@dolt_transaction_commit=1 must reach both latest cursors with
// a clean working set, still run 0040's inline commits, and hand the session
// back with the setting it came in with.
func TestEmbeddedMigrateFreshDatabaseUnderDoltTransactionCommit(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := openFreshTransactionCommitConn(t, ctx)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp on a fresh database under dolt_transaction_commit=1: %v", err)
	}

	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("main schema version = %d, want %d", got, want)
	}
	var ignored int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&ignored); err != nil {
		t.Fatalf("read ignored_schema_migrations: %v", err)
	}
	if want := schema.LatestIgnoredVersion(); ignored != want {
		t.Fatalf("ignored schema version = %d, want %d", ignored, want)
	}
	dirty, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if len(dirty) != 0 {
		t.Fatalf("dolt_status after MigrateUp = %v, want clean", dirty)
	}
	var inlineCommits int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = 'create nonlocal table wisps'").Scan(&inlineCommits); err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	if inlineCommits != 1 {
		t.Fatalf("0040 'create nonlocal table wisps' commits = %d, want 1", inlineCommits)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != 1 {
		t.Fatalf("session dolt_transaction_commit after MigrateUp = %d, want 1 (restored)", got)
	}
}

// openFreshTransactionCommitConn creates an empty testdb (no migration run,
// not even MigrateUpTo, whose 0 means latest) and returns a pinned session on
// it with @@dolt_transaction_commit=1.
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
	if _, err := conn.ExecContext(ctx, "SET @@dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}
	return conn
}

func sessionTransactionCommit(t *testing.T, ctx context.Context, conn *sql.Conn) int {
	t.Helper()
	var v int
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&v); err != nil {
		t.Fatalf("read session dolt_transaction_commit: %v", err)
	}
	return v
}
