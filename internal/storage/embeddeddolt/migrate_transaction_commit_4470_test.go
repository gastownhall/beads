//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
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

// TestEmbeddedFreshMigrateRestoresTransactionCommit_4470 is AC-2's success
// leg: the migration pass hands the pinned connection back with the session's
// original @@dolt_transaction_commit.
func TestEmbeddedFreshMigrateRestoresTransactionCommit_4470(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := openFreshTransactionCommitConn(t, ctx)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("fresh MigrateUp under dolt_transaction_commit=1: %v", err)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != 1 {
		t.Fatalf("@@dolt_transaction_commit after MigrateUp = %d, want the original 1", got)
	}
}

// TestEmbeddedMigrateFaultRestoresTransactionCommit_4470 is AC-2's failure
// leg: a pass that fails mid-flight (injected after migration 0040) still
// restores the session setting, and the caller sees the original error.
func TestEmbeddedMigrateFaultRestoresTransactionCommit_4470(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := openFreshTransactionCommitConn(t, ctx)

	errInjected := errors.New("injected fault after migration 40")
	inPass := -1
	restoreHook := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != 40 {
			return nil
		}
		var v int
		if err := db.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&v); err != nil {
			return err
		}
		inPass = v
		return errInjected
	})
	t.Cleanup(restoreHook)

	_, err := schema.MigrateUp(ctx, conn)
	if !errors.Is(err, errInjected) {
		t.Fatalf("MigrateUp error = %v, want the injected fault", err)
	}
	if inPass != 0 {
		t.Fatalf("@@dolt_transaction_commit inside the pass = %d, want 0 (suspended)", inPass)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != 1 {
		t.Fatalf("@@dolt_transaction_commit after the failed pass = %d, want the original 1", got)
	}
}

// TestEmbeddedMigrateCanceledFaultRestoresTransactionCommit_4470 is AC-2's
// failure leg under a canceled caller: the fault hook cancels the pass's
// context before injecting its error, so the restoration must run on its own
// cleanup context. The same connection stays usable and reads 1 again.
func TestEmbeddedMigrateCanceledFaultRestoresTransactionCommit_4470(t *testing.T) {
	requireEmbedded(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn := openFreshTransactionCommitConn(t, ctx)

	errInjected := errors.New("injected fault after migration 40")
	inPass := -1
	restoreHook := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != 40 {
			return nil
		}
		var v int
		if err := db.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&v); err != nil {
			return err
		}
		inPass = v
		cancel()
		return errInjected
	})
	t.Cleanup(restoreHook)

	if _, err := schema.MigrateUp(ctx, conn); !errors.Is(err, errInjected) {
		t.Fatalf("MigrateUp error = %v, want the injected fault", err)
	}
	if inPass != 0 {
		t.Fatalf("@@dolt_transaction_commit inside the pass = %d, want 0 (suspended)", inPass)
	}
	if got := sessionTransactionCommit(t, t.Context(), conn); got != 1 {
		t.Fatalf("@@dolt_transaction_commit after the canceled pass = %d, want the original 1", got)
	}
	if err := conn.PingContext(t.Context()); err != nil {
		t.Fatalf("connection unusable after the canceled pass: %v", err)
	}
}

// TestEmbeddedMigrateRetryAfterFaultConverges_4470: after a pass aborted at
// migration 0040, a plain re-run of MigrateUp on the same connection, setting
// still enabled, converges to latest with a clean working set.
func TestEmbeddedMigrateRetryAfterFaultConverges_4470(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := openFreshTransactionCommitConn(t, ctx)

	errInjected := errors.New("injected fault after migration 40")
	restoreHook := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == 40 {
			return errInjected
		}
		return nil
	})
	_, err := schema.MigrateUp(ctx, conn)
	restoreHook()
	if !errors.Is(err, errInjected) {
		t.Fatalf("first MigrateUp error = %v, want the injected fault", err)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("retry MigrateUp: %v", err)
	}
	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("main schema version after retry = %d, want %d", got, want)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set after retry is dirty: %v, want clean", dirty)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != 1 {
		t.Fatalf("@@dolt_transaction_commit after retry = %d, want the original 1", got)
	}
}

// openSchema27TransactionCommitConn returns a pinned connection, with the
// session's @@dolt_transaction_commit set to 1, to a store seeded at main
// schema 27: GH#4137's starting point, a database created before 0028 that a
// later bd upgrades on a transaction-commit server.
func openSchema27TransactionCommitConn(t *testing.T, ctx context.Context) *sql.Conn {
	t.Helper()
	conn, closeConn := openPinnedConn(t, ctx, seedMainSchemaAt(t, ctx, 27))
	t.Cleanup(closeConn)
	if _, err := conn.ExecContext(ctx, "SET @@SESSION.dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}
	return conn
}

// TestEmbeddedUpgradeFromSchema27UnderTransactionCommit_4137: the upgrade
// counterpart of the fresh-database case. A store at schema 27 migrated on a
// dolt_transaction_commit=1 session records 0028 once, reaches the latest
// main and ignored versions through 0040's and 0041's in-body commits, leaves
// a clean working set, and gets the session's original setting back.
func TestEmbeddedUpgradeFromSchema27UnderTransactionCommit_4137(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := openSchema27TransactionCommitConn(t, ctx)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp from schema 27 under dolt_transaction_commit=1: %v", err)
	}
	if n := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM schema_migrations WHERE version = 28"); n != 1 {
		t.Fatalf("schema_migrations version 28 rows = %d, want 1", n)
	}
	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("main schema version = %d, want %d", got, want)
	}
	if got, want := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"), schema.LatestIgnoredVersion(); got != want {
		t.Fatalf("ignored schema version = %d, want %d", got, want)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set after the upgrade is dirty: %v, want clean", dirty)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != 1 {
		t.Fatalf("@@dolt_transaction_commit after the upgrade = %d, want the original 1", got)
	}
}

// TestEmbeddedUpgradeFromSchema27FaultRestoresTransactionCommit_4137: an
// upgrade pass from schema 27 that fails after migration 0040 ran with the
// setting suspended, surfaces the original error, and still restores the
// session setting.
func TestEmbeddedUpgradeFromSchema27FaultRestoresTransactionCommit_4137(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn := openSchema27TransactionCommitConn(t, ctx)

	errInjected := errors.New("injected fault after migration 40")
	inPass := -1
	restoreHook := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, db schema.DBConn, version int) error {
		if version != 40 {
			return nil
		}
		var v int
		if err := db.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&v); err != nil {
			return err
		}
		inPass = v
		return errInjected
	})
	t.Cleanup(restoreHook)

	_, err := schema.MigrateUp(ctx, conn)
	if !errors.Is(err, errInjected) {
		t.Fatalf("MigrateUp error = %v, want the injected fault", err)
	}
	if inPass != 0 {
		t.Fatalf("@@dolt_transaction_commit inside the pass = %d, want 0 (suspended)", inPass)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != 1 {
		t.Fatalf("@@dolt_transaction_commit after the failed upgrade pass = %d, want the original 1", got)
	}
}
