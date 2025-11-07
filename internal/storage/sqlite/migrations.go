// Package sqlite - database migrations
package sqlite

import (
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/sqlite/migrations"
)

// Migration represents a single database migration
type Migration struct {
	Name string
	Func func(*sql.DB) error
}

// migrations is the ordered list of all migrations to run
// Migrations are run in order during database initialization
var migrationsList = []Migration{
	{"dirty_issues_table", migrations.MigrateDirtyIssuesTable},
	{"external_ref_column", migrations.MigrateExternalRefColumn},
	{"composite_indexes", migrations.MigrateCompositeIndexes},
	{"closed_at_constraint", migrations.MigrateClosedAtConstraint},
	{"compaction_columns", migrations.MigrateCompactionColumns},
	{"snapshots_table", migrations.MigrateSnapshotsTable},
	{"compaction_config", migrations.MigrateCompactionConfig},
	{"compacted_at_commit_column", migrations.MigrateCompactedAtCommitColumn},
	{"export_hashes_table", migrations.MigrateExportHashesTable},
	{"content_hash_column", migrations.MigrateContentHashColumn},
	{"external_ref_unique", migrations.MigrateExternalRefUnique},
	{"source_repo_column", migrations.MigrateSourceRepoColumn},
	{"repo_mtimes_table", migrations.MigrateRepoMtimesTable},
	{"child_counters_table", migrations.MigrateChildCountersTable},
}

// MigrationInfo contains metadata about a migration for inspection
type MigrationInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListMigrations returns list of all registered migrations with descriptions
// Note: This returns ALL registered migrations, not just pending ones (all are idempotent)
func ListMigrations() []MigrationInfo {
	result := make([]MigrationInfo, len(migrationsList))
	for i, m := range migrationsList {
		result[i] = MigrationInfo{
			Name:        m.Name,
			Description: getMigrationDescription(m.Name),
		}
	}
	return result
}

// getMigrationDescription returns a human-readable description for a migration
func getMigrationDescription(name string) string {
	descriptions := map[string]string{
		"dirty_issues_table":           "Adds dirty_issues table for auto-export tracking",
		"external_ref_column":          "Adds external_ref column to issues table",
		"composite_indexes":            "Adds composite indexes for better query performance",
		"closed_at_constraint":         "Adds constraint ensuring closed issues have closed_at timestamp",
		"compaction_columns":           "Adds compaction tracking columns (compacted_at, compacted_at_commit)",
		"snapshots_table":              "Adds snapshots table for issue history",
		"compaction_config":            "Adds config entries for compaction",
		"compacted_at_commit_column":   "Adds compacted_at_commit to snapshots table",
		"export_hashes_table":          "Adds export_hashes table for idempotent exports",
		"content_hash_column":          "Adds content_hash column for collision resolution",
		"external_ref_unique":          "Adds UNIQUE constraint on external_ref column",
		"source_repo_column":           "Adds source_repo column for multi-repo support",
		"repo_mtimes_table":            "Adds repo_mtimes table for multi-repo hydration caching",
		"child_counters_table":         "Adds child_counters table for hierarchical ID generation with ON DELETE CASCADE",
	}
	
	if desc, ok := descriptions[name]; ok {
		return desc
	}
	return "Unknown migration"
}

// RunMigrations executes all registered migrations in order with invariant checking
func RunMigrations(db *sql.DB) error {
	snapshot, err := captureSnapshot(db)
	if err != nil {
		return fmt.Errorf("failed to capture pre-migration snapshot: %w", err)
	}

	for _, migration := range migrationsList {
		if err := migration.Func(db); err != nil {
			return fmt.Errorf("migration %s failed: %w", migration.Name, err)
		}
	}

	if err := verifyInvariants(db, snapshot); err != nil {
		return fmt.Errorf("post-migration validation failed: %w", err)
	}

	return nil
}
