package dolt

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestSchemaMigrationsPopulatedAfterInit verifies that initSchemaOnDB populates
// the schema_migrations table after successful initialization.
func TestSchemaMigrationsPopulatedAfterInit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	var maxVersion int
	err := store.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion)
	if err != nil {
		t.Fatalf("schema_migrations query failed: %v", err)
	}
	if maxVersion != schema.LatestVersion() {
		t.Errorf("max migration version = %d, want %d", maxVersion, schema.LatestVersion())
	}
}

// TestSchemaSkipsReinit verifies that initSchemaOnDB returns early
// when all migrations are already applied, skipping all DDL.
func TestSchemaSkipsReinit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Drop a table to detect whether init actually runs DDL
	_, err := store.db.ExecContext(ctx, "DROP TABLE IF EXISTS export_hashes")
	if err != nil {
		t.Fatalf("failed to drop export_hashes: %v", err)
	}

	// Run initSchemaOnDB again — should skip because migrations are current
	if err := initSchemaOnDB(ctx, store.db); err != nil {
		t.Fatalf("initSchemaOnDB failed: %v", err)
	}

	// export_hashes should still be missing (init was skipped)
	var count int
	err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'export_hashes' AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		t.Fatalf("failed to check for export_hashes: %v", err)
	}
	if count != 0 {
		t.Error("export_hashes was recreated — initSchemaOnDB should have skipped when migrations are current")
	}
}

// TestSchemaRunsInitWhenStale verifies that initSchemaOnDB runs
// migrations when the schema_migrations table is behind.
func TestSchemaRunsInitWhenStale(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Remove the latest migration record to simulate a stale schema
	_, err := store.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion())
	if err != nil {
		t.Fatalf("failed to delete latest migration: %v", err)
	}

	// Run initSchemaOnDB — should detect stale and re-apply
	if err := initSchemaOnDB(ctx, store.db); err != nil {
		t.Fatalf("initSchemaOnDB failed: %v", err)
	}

	// Latest version should be back
	var maxVersion int
	err = store.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion)
	if err != nil {
		t.Fatalf("reading max version: %v", err)
	}
	if maxVersion != schema.LatestVersion() {
		t.Errorf("max migration version = %d after re-init, want %d", maxVersion, schema.LatestVersion())
	}
}

// TestSchemaRunsInitWhenMissing verifies that initSchemaOnDB runs
// full initialization when schema_migrations doesn't exist (fresh db).
func TestSchemaRunsInitWhenMissing(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbName := uniqueTestDBName(t)
	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	var maxVersion int
	err = store.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&maxVersion)
	if err != nil {
		t.Fatalf("schema_migrations query failed: %v", err)
	}
	if maxVersion != schema.LatestVersion() {
		t.Errorf("max migration version = %d, want %d", maxVersion, schema.LatestVersion())
	}

	dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
	defer dropCancel()
	_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	store.Close()
}
