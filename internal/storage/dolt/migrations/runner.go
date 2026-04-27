package migrations

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// CompatMigration represents a backward-compat migration for databases that
// predate the embedded migration system.
type CompatMigration struct {
	Name string
	Func func(*sql.DB) error
}

// compatMigrationsList is the ordered list of backward-compat migrations
// for databases that predate the embedded migration system. Each migration
// must be idempotent — safe to run multiple times.
var compatMigrationsList = []CompatMigration{
	{"wisp_type_column", MigrateWispTypeColumn},
	{"spec_id_column", MigrateSpecIDColumn},
	{"orphan_detection", DetectOrphanedChildren},
	{"wisps_table", MigrateWispsTable},
	{"wisp_auxiliary_tables", MigrateWispAuxiliaryTables},
	{"issue_counter_table", MigrateIssueCounterTable},
	{"infra_to_wisps", MigrateInfraToWisps},
	{"wisp_dep_type_index", MigrateWispDepTypeIndex},
	{"cleanup_autopush_metadata", MigrateCleanupAutopushMetadata},
	{"uuid_primary_keys", MigrateUUIDPrimaryKeys},
	{"add_no_history_column", MigrateAddNoHistoryColumn},
	{"add_started_at_column", MigrateAddStartedAtColumn},
	{"drop_hop_columns", MigrateDropHOPColumns},
	{"drop_child_counters_fk", MigrateDropChildCountersFK},
	{"wisp_events_created_at_index", MigrateWispEventsCreatedAtIndex},
	{"custom_status_type_tables", MigrateCustomStatusTypeTables},
	{"backfill_custom_tables", BackfillCustomTables},
}

// RunCompatMigrations executes pending backward-compat migrations. These handle
// historical data transforms for databases that predate the embedded
// migration system (ALTER TABLE ADD COLUMN, data moves, FK drops, etc.).
//
// A compat_migrations tracking table records which migrations have already
// been applied. Once a migration is recorded, it is skipped on subsequent
// store opens. Without this gate every bd invocation paid for ~30 SQL
// roundtrips of idempotent no-op checks (be-9s8).
func RunCompatMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS compat_migrations (
		name VARCHAR(64) PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("creating compat_migrations table: %w", err)
	}

	applied := make(map[string]bool, len(compatMigrationsList))
	rows, err := db.Query("SELECT name FROM compat_migrations")
	if err != nil {
		return fmt.Errorf("reading compat_migrations: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scanning compat_migrations: %w", err)
		}
		applied[name] = true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating compat_migrations: %w", err)
	}

	for _, m := range compatMigrationsList {
		if applied[m.Name] {
			continue
		}
		if err := m.Func(db); err != nil {
			return fmt.Errorf("compat migration %q failed: %w", m.Name, err)
		}
		// INSERT IGNORE so concurrent processes racing on a fresh DB don't
		// fail on duplicate-key — same pattern as schema_migrations.
		if _, err := db.Exec("INSERT IGNORE INTO compat_migrations (name) VALUES (?)", m.Name); err != nil {
			return fmt.Errorf("recording compat migration %s: %w", m.Name, err)
		}
	}

	// Only stage and commit when compat migrations actually produced changes.
	// Previously, DOLT_COMMIT was called unconditionally, causing a
	// "nothing to commit" WARNING on the server for every bd invocation
	// (94% of server log lines in one reported case). GH#3366.
	var dirtyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM dolt_status").Scan(&dirtyCount); err != nil {
		// dolt_status might not be available (e.g. older servers); fall through
		// to the original behavior as a safe fallback.
		dirtyCount = 1
	}
	if dirtyCount == 0 {
		return nil
	}

	// GH#2455: Stage only schema tables (not config) to avoid sweeping up
	// stale issue_prefix changes from concurrent operations.
	migrationTables := []string{
		"issues", "wisps", "events", "wisp_events", "dependencies",
		"wisp_dependencies", "labels", "wisp_labels", "comments",
		"wisp_comments", "metadata", "child_counters", "issue_counter",
		"issue_snapshots", "compaction_snapshots", "federation_peers",
		"custom_statuses", "custom_types",
		"dolt_ignore",
		"compat_migrations",
	}
	for _, table := range migrationTables {
		_, _ = db.Exec("CALL DOLT_ADD(?)", table)
	}
	_, err = db.Exec("CALL DOLT_COMMIT('-m', 'schema: auto-migrate')")
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			log.Printf("dolt compat migration commit warning: %v", err)
		}
	}

	return nil
}

// ListCompatMigrations returns the names of all registered compat migrations.
func ListCompatMigrations() []string {
	names := make([]string, len(compatMigrationsList))
	for i, m := range compatMigrationsList {
		names[i] = m.Name
	}
	return names
}
