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

// RunCompatMigrations executes all backward-compat migrations. These handle
// historical data transforms for databases that predate the embedded
// migration system (ALTER TABLE ADD COLUMN, data moves, FK drops, etc.).
// Each migration is idempotent and checks whether its changes have already
// been applied.
func RunCompatMigrations(db *sql.DB) error {
	for _, m := range compatMigrationsList {
		if err := m.Func(db); err != nil {
			return fmt.Errorf("compat migration %q failed: %w", m.Name, err)
		}
	}

	// Only stage and commit when compat migrations actually produced committable
	// changes. Exclude dolt-ignored tables (wisps, local_metadata, etc.) that
	// appear in dolt_status but can never be staged — attempting to commit when
	// only ignored tables are dirty produces a "nothing to commit" WARNING on
	// the Dolt server. GH#3366, GH#3529.
	var dirtyCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`).Scan(&dirtyCount); err != nil {
		// dolt_status/dolt_ignore might not be available (e.g. older servers);
		// fall through to the original behavior as a safe fallback.
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
	}
	for _, table := range migrationTables {
		_, _ = db.Exec("CALL DOLT_ADD(?)", table)
	}
	_, err := db.Exec("CALL DOLT_COMMIT('-m', 'schema: auto-migrate')")
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
