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
	{"blocked_issues_cache", migrations.MigrateBlockedIssuesCache},
	{"orphan_detection", migrations.MigrateOrphanDetection},
	{"close_reason_column", migrations.MigrateCloseReasonColumn},
	{"tombstone_columns", migrations.MigrateTombstoneColumns},
	{"messaging_fields", migrations.MigrateMessagingFields},
	{"edge_consolidation", migrations.MigrateEdgeConsolidation},
	{"migrate_edge_fields", migrations.MigrateEdgeFields},
	{"drop_edge_columns", migrations.MigrateDropEdgeColumns},
	{"pinned_column", migrations.MigratePinnedColumn},
	{"is_template_column", migrations.MigrateIsTemplateColumn},
	{"remove_depends_on_fk", migrations.MigrateRemoveDependsOnFK},
	{"additional_indexes", migrations.MigrateAdditionalIndexes},
	{"gate_columns", migrations.MigrateGateColumns},
	{"tombstone_closed_at", migrations.MigrateTombstoneClosedAt},
	{"created_by_column", migrations.MigrateCreatedByColumn},
	{"agent_fields", migrations.MigrateAgentFields},
	{"mol_type_column", migrations.MigrateMolTypeColumn},
	{"hooked_status_migration", migrations.MigrateHookedStatus},
	{"event_fields", migrations.MigrateEventFields},
	{"closed_by_session_column", migrations.MigrateClosedBySessionColumn},
	{"due_defer_columns", migrations.MigrateDueDeferColumns},
	{"owner_column", migrations.MigrateOwnerColumn},
	{"crystallizes_column", migrations.MigrateCrystallizesColumn},
	{"work_type_column", migrations.MigrateWorkTypeColumn},
	{"source_system_column", migrations.MigrateSourceSystemColumn},
	{"quality_score_column", migrations.MigrateQualityScoreColumn},
	{"metadata_column", migrations.MigrateMetadataColumn},
	{"wisp_type_column", migrations.MigrateWispTypeColumn},
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
		"blocked_issues_cache":         "Adds blocked_issues_cache table for GetReadyWork performance optimization",
		"orphan_detection":             "Detects orphaned child issues and logs them for user action",
		"close_reason_column":          "Adds close_reason column to issues table for storing closure explanations",
		"tombstone_columns":            "Adds tombstone columns (deleted_at, deleted_by, delete_reason, original_type) for inline soft-delete",
		"messaging_fields":             "Adds messaging fields (sender, ephemeral, replies_to, relates_to, duplicate_of, superseded_by) for inter-agent communication",
		"edge_consolidation":           "Adds metadata and thread_id columns to dependencies table for edge schema consolidation (Decision 004)",
		"migrate_edge_fields":          "Migrates existing issue fields (replies_to, relates_to, duplicate_of, superseded_by) to dependency edges (Decision 004 Phase 3)",
		"drop_edge_columns":            "Drops deprecated edge columns (replies_to, relates_to, duplicate_of, superseded_by) from issues table (Decision 004 Phase 4)",
		"pinned_column":                "Adds pinned column for persistent context markers",
		"is_template_column":           "Adds is_template column for template molecules",
		"remove_depends_on_fk":         "Removes FK constraint on depends_on_id to allow external references",
		"additional_indexes":           "Adds performance optimization indexes for common query patterns",
		"gate_columns":                 "Adds gate columns (await_type, await_id, timeout_ns, waiters) for async coordination",
		"tombstone_closed_at":          "Preserves closed_at timestamp when issues become tombstones",
		"created_by_column":            "Adds created_by column to track issue creator",
		"agent_fields":                 "Adds agent identity fields (hook_bead, role_bead, agent_state, etc.) for agent-as-bead pattern",
		"mol_type_column":              "Adds mol_type column for molecule type classification (swarm/patrol/work)",
		"hooked_status_migration":      "Migrates blocked hooked issues to in_progress status",
		"event_fields":                 "Adds event fields (event_kind, actor, target, payload) for operational state change beads",
		"closed_by_session_column":     "Adds closed_by_session column for tracking which Claude Code session closed an issue",
		"due_defer_columns":            "Adds due_at and defer_until columns for time-based task scheduling (GH#820)",
		"owner_column":                 "Adds owner column for human attribution in HOP CV chains (Decision 008)",
		"crystallizes_column":          "Adds crystallizes column for work economics (compounds vs evaporates) per Decision 006",
		"work_type_column":             "Adds work_type column for work assignment model (mutex vs open_competition per Decision 006)",
		"source_system_column":         "Adds source_system column for federation adapter tracking",
		"quality_score_column":         "Adds quality_score column for aggregate quality (0.0-1.0) set by Refineries",
		"metadata_column":              "Adds metadata column for arbitrary JSON data (tool annotations, file lists) per GH#1406",
		"wisp_type_column":             "Adds wisp_type column for TTL-based compaction classification (gt-9br)",
	}

	if desc, ok := descriptions[name]; ok {
		return desc
	}
	return "Unknown migration"
}

// RunMigrations executes all registered migrations in order with invariant checking.
// Uses EXCLUSIVE transaction to prevent race conditions when multiple processes
// open the database simultaneously (GH#720).
func RunMigrations(db *sql.DB) error {
	// Disable foreign keys BEFORE starting the transaction.
	// PRAGMA foreign_keys must be called when no transaction is active (SQLite limitation).
	// Some migrations (022, 025) drop/recreate tables and need foreign keys off
	// to prevent ON DELETE CASCADE from deleting related data.
	_, err := db.Exec("PRAGMA foreign_keys = OFF")
	if err != nil {
		return fmt.Errorf("failed to disable foreign keys for migrations: %w", err)
	}
	defer func() { _, _ = db.Exec("PRAGMA foreign_keys = ON") }()

	// Acquire EXCLUSIVE lock to serialize migrations across processes.
	// Without this, parallel processes can race on check-then-modify operations
	// (e.g., checking if a column exists then adding it), causing "duplicate column" errors.
	_, err = db.Exec("BEGIN EXCLUSIVE")
	if err != nil {
		return fmt.Errorf("failed to acquire exclusive lock for migrations: %w", err)
	}

	// Ensure we release the lock on any exit path
	committed := false
	defer func() {
		if !committed {
			_, _ = db.Exec("ROLLBACK")
		}
	}()

	// Pre-migration cleanup: remove orphaned refs that would fail invariant checks.
	// This prevents the chicken-and-egg problem where the database can't open
	// due to orphans left behind by tombstone deletion (see bd-eko4).
	if _, _, err := CleanOrphanedRefs(db); err != nil {
		return fmt.Errorf("pre-migration orphan cleanup failed: %w", err)
	}

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

	// Commit the transaction
	if _, err := db.Exec("COMMIT"); err != nil {
		return fmt.Errorf("failed to commit migrations: %w", err)
	}
	committed = true

	return nil
}
