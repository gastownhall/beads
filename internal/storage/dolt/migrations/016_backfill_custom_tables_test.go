package migrations

import (
	"testing"
)

// --- Migration 015 tests ---

func TestMigrateCustomStatusTypeTables_EmptyConfig(t *testing.T) {
	db := openTestDoltBranch(t)

	// Create config table with no custom types/statuses config
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config table: %v", err)
	}

	// Run migration — should create tables but not populate them
	if err := MigrateCustomStatusTypeTables(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify tables exist
	for _, table := range []string{"custom_types", "custom_statuses"} {
		exists, err := TableExists(db, table)
		if err != nil {
			t.Fatalf("tableExists(%s): %v", table, err)
		}
		if !exists {
			t.Fatalf("expected %s table to exist after migration", table)
		}
	}

	// Verify tables are empty
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("count custom_types: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 custom_types, got %d", count)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&count); err != nil {
		t.Fatalf("count custom_statuses: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 custom_statuses, got %d", count)
	}
}

func TestMigrateCustomStatusTypeTables_WithConfig(t *testing.T) {
	db := openTestDoltBranch(t)

	// Create config table with custom types and statuses
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config table: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'gate,convoy')")
	if err != nil {
		t.Fatalf("insert types.custom: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('status.custom', 'reviewing:active,parked:frozen')")
	if err != nil {
		t.Fatalf("insert status.custom: %v", err)
	}

	// Run migration
	if err := MigrateCustomStatusTypeTables(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify types populated
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("count custom_types: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 custom_types, got %d", count)
	}

	// Verify statuses populated with correct categories
	var category string
	if err := db.QueryRow("SELECT category FROM custom_statuses WHERE name = 'reviewing'").Scan(&category); err != nil {
		t.Fatalf("query reviewing: %v", err)
	}
	if category != "active" {
		t.Fatalf("expected reviewing category 'active', got %q", category)
	}
	if err := db.QueryRow("SELECT category FROM custom_statuses WHERE name = 'parked'").Scan(&category); err != nil {
		t.Fatalf("query parked: %v", err)
	}
	if category != "frozen" {
		t.Fatalf("expected parked category 'frozen', got %q", category)
	}
}

func TestMigrateCustomStatusTypeTables_Idempotent(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config table: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'agent')")
	if err != nil {
		t.Fatalf("insert types.custom: %v", err)
	}

	// Run twice
	if err := MigrateCustomStatusTypeTables(db); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := MigrateCustomStatusTypeTables(db); err != nil {
		t.Fatalf("second run (idempotent) failed: %v", err)
	}

	// Still exactly 1 type
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 custom_types, got %d", count)
	}
}

func TestMigrateCustomStatusTypeTables_JSONTypes(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO config (` + "`key`" + `, value) VALUES ('types.custom', '["gate","convoy"]')`)
	if err != nil {
		t.Fatalf("insert types.custom: %v", err)
	}

	if err := MigrateCustomStatusTypeTables(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 custom_types from JSON, got %d", count)
	}
}

// --- Migration 016 (BackfillCustomTables) tests ---

func TestBackfillCustomTypes_EmptyTableWithConfig(t *testing.T) {
	db := openTestDoltBranch(t)

	// Create config + custom_types (empty)
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'agent,gate')")
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_types (name VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create custom_types: %v", err)
	}

	// Backfill should populate
	if err := backfillCustomTypes(db); err != nil {
		t.Fatalf("backfillCustomTypes: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 custom_types, got %d", count)
	}
}

func TestBackfillCustomTypes_AlreadyPopulated(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'agent,gate')")
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_types (name VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create custom_types: %v", err)
	}
	_, err = db.Exec("INSERT INTO custom_types (name) VALUES ('agent')")
	if err != nil {
		t.Fatalf("insert existing: %v", err)
	}

	// Backfill should skip (already populated)
	if err := backfillCustomTypes(db); err != nil {
		t.Fatalf("backfillCustomTypes: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 custom_types (no backfill), got %d", count)
	}
}

func TestBackfillCustomTypes_TableMissing(t *testing.T) {
	db := openTestDoltBranch(t)

	// No custom_types table — should be a no-op
	if err := backfillCustomTypes(db); err != nil {
		t.Fatalf("backfillCustomTypes on missing table should not error: %v", err)
	}
}

func TestBackfillCustomTypes_NoConfig(t *testing.T) {
	db := openTestDoltBranch(t)

	// custom_types table exists but no config
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_types (name VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create custom_types: %v", err)
	}

	if err := backfillCustomTypes(db); err != nil {
		t.Fatalf("backfillCustomTypes with no config: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 custom_types, got %d", count)
	}
}

func TestBackfillCustomStatuses_EmptyTableWithConfig(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('status.custom', 'reviewing:active,parked:frozen')")
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}

	if err := backfillCustomStatuses(db); err != nil {
		t.Fatalf("backfillCustomStatuses: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 custom_statuses, got %d", count)
	}

	// Verify category preservation
	var category string
	if err := db.QueryRow("SELECT category FROM custom_statuses WHERE name = 'reviewing'").Scan(&category); err != nil {
		t.Fatalf("query reviewing: %v", err)
	}
	if category != "active" {
		t.Fatalf("expected reviewing category 'active', got %q", category)
	}
	if err := db.QueryRow("SELECT category FROM custom_statuses WHERE name = 'parked'").Scan(&category); err != nil {
		t.Fatalf("query parked: %v", err)
	}
	if category != "frozen" {
		t.Fatalf("expected parked category 'frozen', got %q", category)
	}
}

func TestBackfillCustomStatuses_AlreadyPopulated(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('status.custom', 'reviewing:active,parked:frozen')")
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}
	_, err = db.Exec("INSERT INTO custom_statuses (name, category) VALUES ('reviewing', 'active')")
	if err != nil {
		t.Fatalf("insert existing: %v", err)
	}

	if err := backfillCustomStatuses(db); err != nil {
		t.Fatalf("backfillCustomStatuses: %v", err)
	}

	// Should remain at 1 (no backfill since already populated)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 custom_statuses (no backfill), got %d", count)
	}
}

func TestBackfillCustomStatuses_TableMissing(t *testing.T) {
	db := openTestDoltBranch(t)

	if err := backfillCustomStatuses(db); err != nil {
		t.Fatalf("backfillCustomStatuses on missing table should not error: %v", err)
	}
}

func TestBackfillCustomStatuses_NoConfig(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}

	if err := backfillCustomStatuses(db); err != nil {
		t.Fatalf("backfillCustomStatuses with no config: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 custom_statuses, got %d", count)
	}
}

func TestBackfillCustomStatuses_CategoryPreservation(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	// Test all category types
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('status.custom', 'rev:active,doing:wip,shipped:done,ice:frozen')")
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}

	if err := backfillCustomStatuses(db); err != nil {
		t.Fatalf("backfillCustomStatuses: %v", err)
	}

	expected := map[string]string{
		"rev":     "active",
		"doing":   "wip",
		"shipped": "done",
		"ice":     "frozen",
	}
	for name, wantCat := range expected {
		var got string
		if err := db.QueryRow("SELECT category FROM custom_statuses WHERE name = ?", name).Scan(&got); err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		if got != wantCat {
			t.Errorf("status %q: expected category %q, got %q", name, wantCat, got)
		}
	}
}

// --- BackfillCustomTables end-to-end tests ---

func TestBackfillCustomTables_BothEmpty(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'agent')")
	if err != nil {
		t.Fatalf("insert types config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('status.custom', 'reviewing:active')")
	if err != nil {
		t.Fatalf("insert status config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_types (name VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create custom_types: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}

	if err := BackfillCustomTables(db); err != nil {
		t.Fatalf("BackfillCustomTables: %v", err)
	}

	var typeCount, statusCount int
	db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&typeCount)
	db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&statusCount)
	if typeCount != 1 {
		t.Errorf("expected 1 custom_types, got %d", typeCount)
	}
	if statusCount != 1 {
		t.Errorf("expected 1 custom_statuses, got %d", statusCount)
	}
}

func TestBackfillCustomTables_OneEmptyOnePopulated(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'agent,gate')")
	if err != nil {
		t.Fatalf("insert types config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('status.custom', 'reviewing:active')")
	if err != nil {
		t.Fatalf("insert status config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_types (name VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create custom_types: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}

	// Pre-populate statuses (so only types should be backfilled)
	_, err = db.Exec("INSERT INTO custom_statuses (name, category) VALUES ('reviewing', 'active')")
	if err != nil {
		t.Fatalf("insert existing status: %v", err)
	}

	if err := BackfillCustomTables(db); err != nil {
		t.Fatalf("BackfillCustomTables: %v", err)
	}

	var typeCount, statusCount int
	db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&typeCount)
	db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&statusCount)
	if typeCount != 2 {
		t.Errorf("expected 2 custom_types (backfilled), got %d", typeCount)
	}
	if statusCount != 1 {
		t.Errorf("expected 1 custom_statuses (not backfilled), got %d", statusCount)
	}
}

func TestBackfillCustomTables_NeitherEmpty(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'agent,gate')")
	if err != nil {
		t.Fatalf("insert types config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('status.custom', 'reviewing:active')")
	if err != nil {
		t.Fatalf("insert status config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_types (name VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create custom_types: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}

	// Pre-populate both
	_, err = db.Exec("INSERT INTO custom_types (name) VALUES ('agent')")
	if err != nil {
		t.Fatalf("insert existing type: %v", err)
	}
	_, err = db.Exec("INSERT INTO custom_statuses (name, category) VALUES ('reviewing', 'active')")
	if err != nil {
		t.Fatalf("insert existing status: %v", err)
	}

	if err := BackfillCustomTables(db); err != nil {
		t.Fatalf("BackfillCustomTables: %v", err)
	}

	// Neither should change
	var typeCount, statusCount int
	db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&typeCount)
	db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&statusCount)
	if typeCount != 1 {
		t.Errorf("expected 1 custom_types (untouched), got %d", typeCount)
	}
	if statusCount != 1 {
		t.Errorf("expected 1 custom_statuses (untouched), got %d", statusCount)
	}
}

func TestBackfillCustomTables_Idempotent(t *testing.T) {
	db := openTestDoltBranch(t)

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS config (` + "`key`" + ` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	_, err = db.Exec("INSERT INTO config (`key`, value) VALUES ('types.custom', 'agent')")
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_types (name VARCHAR(64) PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create custom_types: %v", err)
	}
	_, err = db.Exec("CREATE TABLE custom_statuses (name VARCHAR(64) PRIMARY KEY, category VARCHAR(32) NOT NULL DEFAULT 'unspecified')")
	if err != nil {
		t.Fatalf("create custom_statuses: %v", err)
	}

	// Run twice
	if err := BackfillCustomTables(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := BackfillCustomTables(db); err != nil {
		t.Fatalf("second run (idempotent): %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 custom_types after idempotent run, got %d", count)
	}
}
