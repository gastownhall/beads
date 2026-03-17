package dolt

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// TestSchemaVersionSetAfterInit verifies that initSchemaOnDB sets
// schema_version in the config table after successful initialization.
func TestSchemaVersionSetAfterInit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	var version int
	err := store.db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'schema_version'").Scan(&version)
	if err != nil {
		t.Fatalf("schema_version not found in config after init: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, currentSchemaVersion)
	}

	var stagedRows int
	err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'").Scan(&stagedRows)
	if err != nil {
		t.Fatalf("query dolt_status for config: %v", err)
	}
	if stagedRows != 0 {
		t.Fatalf("config left staged in dolt_status after init: %d row(s)", stagedRows)
	}
}

// TestSchemaVersionSkipsReinit verifies that initSchemaOnDB returns early
// when the stored version matches currentSchemaVersion, skipping all DDL.
func TestSchemaVersionSkipsReinit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Drop a table to detect whether init actually runs DDL
	_, err := store.db.ExecContext(ctx, "DROP TABLE IF EXISTS export_hashes")
	if err != nil {
		t.Fatalf("failed to drop export_hashes: %v", err)
	}

	// Run initSchemaOnDB again — should skip because version matches
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
		t.Error("export_hashes was recreated — initSchemaOnDB should have skipped when version matches")
	}
}

// TestSchemaVersionRunsInitWhenStale verifies that initSchemaOnDB runs
// full initialization when the stored version is lower than currentSchemaVersion.
func TestSchemaVersionRunsInitWhenStale(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Set version to an old value
	_, err := store.db.ExecContext(ctx,
		"UPDATE config SET `value` = '1' WHERE `key` = 'schema_version'")
	if err != nil {
		t.Fatalf("failed to set old schema_version: %v", err)
	}

	// Drop a table so we can detect re-creation
	_, err = store.db.ExecContext(ctx, "DROP TABLE IF EXISTS interactions")
	if err != nil {
		t.Fatalf("failed to drop interactions: %v", err)
	}

	// Run initSchemaOnDB — should run full init because version is stale
	if err := initSchemaOnDB(ctx, store.db); err != nil {
		t.Fatalf("initSchemaOnDB failed: %v", err)
	}

	// interactions should be recreated
	var count int
	err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'interactions' AND table_schema = DATABASE()").Scan(&count)
	if err != nil {
		t.Fatalf("failed to check for interactions: %v", err)
	}
	if count != 1 {
		t.Error("interactions was not recreated — initSchemaOnDB should have run full init for stale version")
	}

	// Version should be updated to current
	var version int
	err = store.db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'schema_version'").Scan(&version)
	if err != nil {
		t.Fatalf("schema_version not found after re-init: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d after re-init, want %d", version, currentSchemaVersion)
	}
}

// TestSchemaVersionRunsLatestMigrationsWhenOneVersionBehind verifies that a
// database marked one schema version behind re-enters initSchemaOnDB and picks
// up the latest migration-backed columns.
func TestSchemaVersionRunsLatestMigrationsWhenOneVersionBehind(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for _, stmt := range []string{
		"ALTER TABLE issues DROP COLUMN no_history",
		"ALTER TABLE wisps DROP COLUMN no_history",
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	_, err := store.db.ExecContext(ctx,
		"UPDATE config SET `value` = ? WHERE `key` = 'schema_version'",
		currentSchemaVersion-1,
	)
	if err != nil {
		t.Fatalf("failed to set prior schema_version: %v", err)
	}

	if err := initSchemaOnDB(ctx, store.db); err != nil {
		t.Fatalf("initSchemaOnDB failed: %v", err)
	}

	for _, table := range []string{"issues", "wisps"} {
		var count int
		err := store.db.QueryRowContext(
			ctx,
			"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'no_history'",
			table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("check %s.no_history: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s.no_history missing after initSchemaOnDB", table)
		}
	}

	// Verify that config is NOT left dirty in dolt_status (GH#2634).
	// The schema_version write must be committed as part of the migration.
	var stagedRows int
	err = store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'").Scan(&stagedRows)
	if err != nil {
		t.Fatalf("query dolt_status for config: %v", err)
	}
	if stagedRows != 0 {
		t.Fatalf("config left staged in dolt_status after upgrade from one version behind: %d row(s)", stagedRows)
	}
}

// TestSchemaVersionRunsInitWhenMissing verifies that initSchemaOnDB runs
// full initialization when the schema_version key doesn't exist (fresh db
// or pre-versioning upgrade).
func TestSchemaVersionRunsInitWhenMissing(t *testing.T) {
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
		CreateIfMissing: true, // test creates a fresh database
	}

	// First open — creates schema and sets version
	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Delete the schema_version key to simulate a pre-versioning database
	_, err = store.db.ExecContext(ctx, "DELETE FROM config WHERE `key` = 'schema_version'")
	if err != nil {
		t.Fatalf("failed to delete schema_version: %v", err)
	}

	// Run initSchemaOnDB — should run full init (SELECT fails, no version found)
	if err := initSchemaOnDB(ctx, store.db); err != nil {
		t.Fatalf("initSchemaOnDB failed on missing version: %v", err)
	}

	// Version should now be set
	var version int
	err = store.db.QueryRowContext(ctx, "SELECT `value` FROM config WHERE `key` = 'schema_version'").Scan(&version)
	if err != nil {
		t.Fatalf("schema_version not set after init with missing key: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, currentSchemaVersion)
	}

	dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
	defer dropCancel()
	_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	store.Close()
}
