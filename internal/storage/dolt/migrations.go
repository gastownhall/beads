package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt/migrations"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// CompatMigration represents a DoltStore-specific backward-compat migration.
type CompatMigration struct {
	Name string
	Func func(*sql.DB) error
}

// compatMigrationsList is the ordered list of DoltStore-specific migrations
// for databases that predate the embedded migration system. Each migration
// must be idempotent — safe to run multiple times.
var compatMigrationsList = []CompatMigration{
	{"wisp_type_column", migrations.MigrateWispTypeColumn},
	{"spec_id_column", migrations.MigrateSpecIDColumn},
	{"orphan_detection", migrations.DetectOrphanedChildren},
	{"wisps_table", migrations.MigrateWispsTable},
	{"wisp_auxiliary_tables", migrations.MigrateWispAuxiliaryTables},
	{"issue_counter_table", migrations.MigrateIssueCounterTable},
	{"infra_to_wisps", migrations.MigrateInfraToWisps},
	{"wisp_dep_type_index", migrations.MigrateWispDepTypeIndex},
	{"cleanup_autopush_metadata", migrations.MigrateCleanupAutopushMetadata},
	{"uuid_primary_keys", migrations.MigrateUUIDPrimaryKeys},
	{"add_no_history_column", migrations.MigrateAddNoHistoryColumn},
	{"drop_hop_columns", migrations.MigrateDropHOPColumns},
	{"drop_child_counters_fk", migrations.MigrateDropChildCountersFK},
	{"wisp_events_created_at_index", migrations.MigrateWispEventsCreatedAtIndex},
	{"custom_status_type_tables", migrations.MigrateCustomStatusTypeTables},
	{"backfill_custom_tables", migrations.BackfillCustomTables},
}

// RunCompatMigrations executes all DoltStore-specific backward-compat migrations.
// These handle historical data transforms for databases that predate the embedded
// migration system (ALTER TABLE ADD COLUMN, data moves, FK drops, etc.).
// Each migration is idempotent and checks whether its changes have already been applied.
func RunCompatMigrations(db *sql.DB) error {
	for _, m := range compatMigrationsList {
		if err := m.Func(db); err != nil {
			return fmt.Errorf("compat migration %q failed: %w", m.Name, err)
		}
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
		// "nothing to commit" is expected when migrations were already applied
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			log.Printf("dolt compat migration commit warning: %v", err)
		}
	}

	return nil
}

// CreateIgnoredTables re-creates dolt_ignore'd tables (wisps, wisp_*)
// on the current branch. These tables only exist in the working set and
// are not inherited when branching. Safe to call repeatedly (idempotent).
// Exported for use by test helpers in other packages.
func CreateIgnoredTables(db *sql.DB) error {
	return schema.CreateIgnoredTables(context.Background(), db)
}

// ListCompatMigrations returns the names of all registered compat migrations.
func ListCompatMigrations() []string {
	names := make([]string, len(compatMigrationsList))
	for i, m := range compatMigrationsList {
		names[i] = m.Name
	}
	return names
}
