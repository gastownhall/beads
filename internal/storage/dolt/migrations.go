package dolt

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt/migrations"
)

// Migration represents a single schema migration for Dolt.
type Migration struct {
	Name string
	Func func(*sql.DB) error
}

// migrationsList is the ordered list of all Dolt schema migrations.
// Each migration must be idempotent - safe to run multiple times.
// New migrations should be appended to the end of this list.
var migrationsList = []Migration{
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
	{"issues_type_status_index", migrations.MigrateIssuesTypeStatusIndex},
	{"sync_wisps_schema", migrations.MigrateSyncWispsSchema},
}

// RunMigrations executes all registered Dolt migrations in order.
// Each migration is idempotent and checks whether its changes have
// already been applied before making modifications.
func RunMigrations(db *sql.DB) error {
	for _, m := range migrationsList {
		if err := m.Func(db); err != nil {
			return fmt.Errorf("dolt migration %q failed: %w", m.Name, err)
		}
	}

	// GH#2455: Stage only schema tables (not config) to avoid sweeping up
	// stale issue_prefix changes from concurrent operations. The old '-Am'
	// approach staged ALL dirty tables including config.
	migrationTables := []string{
		"issues", "wisps", "events", "wisp_events", "dependencies",
		"wisp_dependencies", "labels", "wisp_labels", "comments",
		"wisp_comments", "metadata", "child_counters", "issue_counter",
		"issue_snapshots", "compaction_snapshots", "federation_peers",
		"dolt_ignore",
	}
	for _, table := range migrationTables {
		_, _ = db.Exec("CALL DOLT_ADD(?)", table)
	}
	_, err := db.Exec("CALL DOLT_COMMIT('-m', 'schema: auto-migrate')")
	if err != nil {
		// "nothing to commit" is expected when migrations were already applied
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			log.Printf("dolt migration commit warning: %v", err)
		}
	}

	return nil
}

// CreateIgnoredTables re-creates dolt_ignore'd tables (wisps, wisp_*)
// on the current branch. These tables only exist in the working set and
// are not inherited when branching. Safe to call repeatedly (idempotent).
// Exported for use by test helpers in other packages.
func CreateIgnoredTables(db *sql.DB) error {
	return createIgnoredTables(db)
}

// createIgnoredTables is the internal implementation.
func createIgnoredTables(db *sql.DB) error {
	if err := migrations.MigrateWispsTable(db); err != nil {
		return fmt.Errorf("wisps table: %w", err)
	}
	if err := migrations.MigrateWispAuxiliaryTables(db); err != nil {
		return fmt.Errorf("wisp auxiliary tables: %w", err)
	}
	return nil
}

// ListMigrations returns the names of all registered migrations.
func ListMigrations() []string {
	names := make([]string, len(migrationsList))
	for i, m := range migrationsList {
		names[i] = m.Name
	}
	return names
}
