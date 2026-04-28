package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt/migrations"
)

// TestRunCompatMigrationsSkipsWhenUpToDate verifies that backfill-tier
// migrations are gated by compat_migrations once recorded as applied.
// Drift-tier migrations (the schema-shape repairs) intentionally run
// every time — their idempotency check IS the drift detector, per
// GH#3412 / be-bjxf — so this test exercises only the backfill path.
//
// Regression test for be-9s8 (the fast-path savings) and be-zjv6 (the
// fix that re-enables drift-tier self-heal by gating only backfill-tier).
func TestRunCompatMigrationsSkipsWhenUpToDate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// setupTestStore already ran RunCompatMigrations, so compat_migrations
	// has a row for backfill_custom_tables and custom_types is populated
	// (migration 016 backfilled the legacy default rows).
	// Clear custom_types to set up the gating assertion: if the backfill
	// re-runs, it will repopulate the table; if it's gated, the table
	// stays empty.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM custom_types"); err != nil {
		t.Fatalf("failed to clear custom_types: %v", err)
	}

	if err := migrations.RunCompatMigrations(store.db); err != nil {
		t.Fatalf("RunCompatMigrations failed: %v", err)
	}

	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("checking custom_types: %v", err)
	}
	if count != 0 {
		t.Errorf("custom_types has %d rows, want 0 — backfill_custom_tables should be gated by compat_migrations once applied", count)
	}
}

// TestRunCompatMigrationsRunsPendingMigrations verifies the slow path:
// when compat_migrations is empty (e.g. pre-fix DB upgrading), every
// migration runs — both drift-tier (which always runs anyway) and
// backfill-tier (whose tracking-table row was cleared).
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
