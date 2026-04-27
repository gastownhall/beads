package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt/migrations"
)

// TestRunCompatMigrationsSkipsWhenUpToDate verifies that RunCompatMigrations
// returns early once all migrations have been recorded as applied.
// Regression test for be-9s8: previously, every store open ran all 17
// compat migrations, adding ~30 SQL queries per bd invocation.
func TestRunCompatMigrationsSkipsWhenUpToDate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// setupTestStore already ran RunCompatMigrations during init.
	// Drop a table that compat migration 015 (custom_status_type_tables)
	// would recreate, as a marker for "did the migration run again?".
	if _, err := store.db.ExecContext(ctx, "DROP TABLE IF EXISTS custom_statuses"); err != nil {
		t.Fatalf("failed to drop custom_statuses: %v", err)
	}

	if err := migrations.RunCompatMigrations(store.db); err != nil {
		t.Fatalf("RunCompatMigrations failed: %v", err)
	}

	var count int
	err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'custom_statuses' AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		t.Fatalf("checking custom_statuses: %v", err)
	}
	if count != 0 {
		t.Error("custom_statuses was recreated — RunCompatMigrations should skip when migrations are tracked as applied")
	}
}

// TestRunCompatMigrationsRunsPendingMigrations verifies the slow path:
// when compat_migrations is empty (e.g. pre-fix DB upgrading), pending
// migrations actually run.
func TestRunCompatMigrationsRunsPendingMigrations(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := store.db.ExecContext(ctx, "DELETE FROM compat_migrations"); err != nil {
		t.Fatalf("clearing compat_migrations: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DROP TABLE IF EXISTS custom_statuses"); err != nil {
		t.Fatalf("dropping custom_statuses: %v", err)
	}

	if err := migrations.RunCompatMigrations(store.db); err != nil {
		t.Fatalf("RunCompatMigrations failed: %v", err)
	}

	var count int
	err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'custom_statuses' AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		t.Fatalf("checking custom_statuses: %v", err)
	}
	if count == 0 {
		t.Error("custom_statuses was not recreated — slow path should re-run migration 015 when compat_migrations is empty")
	}

	var rows int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM compat_migrations").Scan(&rows); err != nil {
		t.Fatalf("checking compat_migrations: %v", err)
	}
	if rows != len(migrations.ListCompatMigrations()) {
		t.Errorf("compat_migrations has %d rows, want %d", rows, len(migrations.ListCompatMigrations()))
	}
}
