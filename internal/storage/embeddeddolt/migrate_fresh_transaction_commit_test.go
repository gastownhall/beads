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

// TestEmbeddedFreshMigrateUpUnderTransactionCommit reproduces GH#4470: a
// fresh `bd init` against a @@dolt_transaction_commit=1 server must migrate
// a brand-new database to latest instead of failing a migration statement
// with "nothing to commit". The setting makes every statement commit at its
// own boundary, so the frozen in-body CALL DOLT_COMMIT calls in migration
// 0040 find an empty staged set and abort schema initialization; MigrateUp
// suspends the setting for the duration of the pass (see
// internal/storage/schema/transaction_commit.go).
func TestEmbeddedFreshMigrateUpUnderTransactionCommit(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS testdb"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	_ = bootCleanup()

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := conn.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("fresh MigrateUp under dolt_transaction_commit=1: %v", err)
	}
	// AC-1: both migration cursors reach their latest versions.
	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	var ignoredVersion int
	if err := conn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&ignoredVersion); err != nil {
		t.Fatalf("read ignored schema version: %v", err)
	}
	if ignoredVersion != schema.LatestIgnoredVersion() {
		t.Fatalf("ignored schema version = %d, want %d", ignoredVersion, schema.LatestIgnoredVersion())
	}
	// AC-1: dolt_status is empty (the pass's commits all landed, nothing
	// stranded in the working set).
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set dirty after fresh migrate: %v, want clean", dirty)
	}
	// 0040's in-body commit happened at its own explicit CALL boundary: the
	// changes were staged for the CALL instead of auto-committing at each
	// statement.
	var wispsCommits int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message = 'create nonlocal table wisps'").Scan(&wispsCommits); err != nil {
		t.Fatalf("count 0040 wisps commits in dolt_log: %v", err)
	}
	if wispsCommits != 1 {
		t.Fatalf("dolt_log commits named 'create nonlocal table wisps' = %d, want 1", wispsCommits)
	}
	// AC-2 (success half): the pass restored the session setting it changed —
	// the same usable connection reads 1 again.
	if got := sessionTransactionCommit(t, conn); got != 1 {
		t.Fatalf("session dolt_transaction_commit after MigrateUp = %d, want 1 (restored)", got)
	}
}

// sessionTransactionCommit reads the pinned connection's commit-semantics
// setting on a fresh context, independent of any canceled pass context.
func sessionTransactionCommit(t *testing.T, conn *sql.Conn) int64 {
	t.Helper()
	var v int64
	if err := conn.QueryRowContext(context.Background(),
		"SELECT @@SESSION.dolt_transaction_commit").Scan(&v); err != nil {
		t.Fatalf("read session dolt_transaction_commit: %v", err)
	}
	return v
}
