package schema

import (
	"context"
	"fmt"
)

// Pre-migration repairs run immediately before a specific pending migration
// file is applied. Shipped migration files are frozen (see
// scripts/check-migration-hygiene.sh): editing one forks fresh clones from
// upgraded clones via the recorded content hash, and a bug that makes a
// migration FAIL on drifted databases cannot be fixed forward with a new
// migration either — the failing file aborts the pass before any later
// version runs. Repairing the drift in code, keyed to the pending version,
// is the only path that heals affected databases without touching shipped
// SQL. Precedent: ensureContentHashColumn, the aux row-id backfill.

// preMigrationRepair dispatches any repair registered for (source, version).
func (m migrationSource) preMigrationRepair(ctx context.Context, db DBConn, version int) error {
	if m.cursorTable != "schema_migrations" {
		return nil
	}
	switch version {
	case 47:
		return ensureWispTablesForMixedBlockedRecompute(ctx, db)
	case 53:
		if err := ensureIssuesRigColumns(ctx, db); err != nil {
			return err
		}
		return ensureWispDependenciesSplitTargets(ctx, db)
	}
	return nil
}

// ensureWispTablesForMixedBlockedRecompute repairs #4695 (and the identical
// #4176 clone-skew shape): migration 0047's final recompute block joins
// wisps/wisp_dependencies unconditionally in a WITH RECURSIVE UPDATE. Those
// tables are dolt_ignore'd (0019_wisps_dolt_ignore,
// 0040_ignored_tables_also_nonlocal_tables), so they never sync to a clone,
// and MigrateUp runs the main sequence (which reaches 0047) before the
// ignored sequence that (re)creates them locally. Any clone whose
// schema_migrations cursor arrives below the binary's latest -- the normal
// window right after a schema bump, before every producer has re-pushed, and
// also how a pre-1.0 database's stale cursor numbering can treat the main
// 0020/0021 wisp-creating migrations as already applied -- reaches 0047 with
// the wisp tables altogether missing: "Error 1146: table not found: wisps".
//
// 0047's own SQL is frozen (already shipped, content-hashed); create the
// tables here if they are entirely missing. An empty wisps/wisp_dependencies
// pair makes 0047's recompute a correct no-op (there are no wisps yet to
// consider), and the ignored sequence that runs after all main migrations
// complete simply finds the tables already present. If wisp_dependencies
// already exists (e.g. a local ignored-migration cursor that has not yet
// caught up) but lacks the split target columns 0047 also reads, reuse
// ensureWispDependenciesSplitTargets -- the same repair migration 0053
// already relies on.
//
// The created shape must be forward-canonical, not the original 0020/0021
// content: this repair fires at v47, but the SAME empty tables then ride
// through every later main migration in the same pass, including 0053 --
// which INSERTs wisps.no_history/started_at into issues (added by 0023/0027,
// after 0020) and, on a database whose local wisp_dependencies predates
// 0022, expects idx_wisp_dep_type to already exist. Creating the 0020/0021-era
// shape here is exactly the #4695 bug shape one migration later: a real
// production repro hit "table not found: wisps" at 0047 and "unknown column
// no_history" at 0053 back to back (see the failed-workaround log in #4695).
// wispsTableDDLForMigration0047 / wispDependenciesTableDDLForMigration0047
// below are therefore the full shape as of immediately before 0053 runs --
// every column/index any main migration <=52 adds to these tables -- verified
// by building a bounded fresh chain (migrations 1..52 only) and comparing
// against SHOW CREATE TABLE, not by manual inspection. Nothing from 54/55
// (lease columns added then removed again) or 56 belongs here.
func ensureWispTablesForMixedBlockedRecompute(ctx context.Context, db DBConn) error {
	hasWisps, err := schemaTableExists(ctx, db, "wisps")
	if err != nil {
		return fmt.Errorf("checking wisps table: %w", err)
	}
	if !hasWisps {
		if _, err := db.ExecContext(ctx, wispsTableDDLForMigration0047); err != nil {
			return fmt.Errorf("creating wisps for migration 0047: %w", err)
		}
	}

	hasWispDeps, err := schemaTableExists(ctx, db, "wisp_dependencies")
	if err != nil {
		return fmt.Errorf("checking wisp_dependencies table: %w", err)
	}
	if !hasWispDeps {
		if _, err := db.ExecContext(ctx, wispDependenciesTableDDLForMigration0047); err != nil {
			return fmt.Errorf("creating wisp_dependencies for migration 0047: %w", err)
		}
		return nil
	}

	return ensureWispDependenciesSplitTargets(ctx, db)
}

// wispsTableDDLForMigration0047 is 0020_create_wisps.up.sql's shape plus every
// column a main migration <=52 subsequently adds to wisps: no_history (0023)
// and started_at (0027). wisps is dolt_ignore'd (never replicated), so
// creating it here when it is missing cannot fork any synced clone: the
// table itself is always clone-local by design, and the ignored sequence's
// own guarded create (ignored/0001: CREATE a __temp__wisps, then RENAME it
// to wisps only if wisps does not already exist, else DROP the temp table)
// simply takes the DROP branch once this has run.
const wispsTableDDLForMigration0047 = `CREATE TABLE IF NOT EXISTS wisps (
    id VARCHAR(255) PRIMARY KEY,
    content_hash VARCHAR(64),
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    design TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    priority INT NOT NULL DEFAULT 2,
    issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
    assignee VARCHAR(255),
    estimated_minutes INT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    owner VARCHAR(255) DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    closed_at DATETIME,
    closed_by_session VARCHAR(255) DEFAULT '',
    external_ref VARCHAR(255),
    spec_id VARCHAR(1024),
    compaction_level INT DEFAULT 0,
    compacted_at DATETIME,
    compacted_at_commit VARCHAR(64),
    original_size INT,
    sender VARCHAR(255) DEFAULT '',
    ephemeral TINYINT(1) DEFAULT 0,
    wisp_type VARCHAR(32) DEFAULT '',
    pinned TINYINT(1) DEFAULT 0,
    is_template TINYINT(1) DEFAULT 0,
    mol_type VARCHAR(32) DEFAULT '',
    work_type VARCHAR(32) DEFAULT 'mutex',
    source_system VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    source_repo VARCHAR(512) DEFAULT '',
    close_reason TEXT DEFAULT '',
    event_kind VARCHAR(32) DEFAULT '',
    actor VARCHAR(255) DEFAULT '',
    target VARCHAR(255) DEFAULT '',
    payload TEXT DEFAULT '',
    await_type VARCHAR(32) DEFAULT '',
    await_id VARCHAR(255) DEFAULT '',
    timeout_ns BIGINT DEFAULT 0,
    waiters TEXT DEFAULT '',
    hook_bead VARCHAR(255) DEFAULT '',
    role_bead VARCHAR(255) DEFAULT '',
    agent_state VARCHAR(32) DEFAULT '',
    last_activity DATETIME,
    role_type VARCHAR(32) DEFAULT '',
    rig VARCHAR(255) DEFAULT '',
    due_at DATETIME,
    defer_until DATETIME,
    no_history TINYINT(1) DEFAULT 0,
    started_at DATETIME,
    INDEX idx_wisps_status (status),
    INDEX idx_wisps_priority (priority),
    INDEX idx_wisps_issue_type (issue_type),
    INDEX idx_wisps_assignee (assignee),
    INDEX idx_wisps_created_at (created_at),
    INDEX idx_wisps_spec_id (spec_id),
    INDEX idx_wisps_external_ref (external_ref)
);`

// wispDependenciesTableDDLForMigration0047 is the wisp_dependencies portion of
// 0021_create_wisp_auxiliary.up.sql's shape (the split-target shape, matching
// what 0047's own SQL reads: depends_on_issue_id / depends_on_wisp_id /
// depends_on_external) plus idx_wisp_dep_type (0022), the one index a main
// migration <=52 subsequently adds. Like wisps, wisp_dependencies is
// dolt_ignore'd and always clone-local.
const wispDependenciesTableDDLForMigration0047 = `CREATE TABLE IF NOT EXISTS wisp_dependencies (
    id CHAR(36) NOT NULL DEFAULT (UUID()) PRIMARY KEY,
    issue_id VARCHAR(255) NOT NULL,
    depends_on_issue_id VARCHAR(255) NULL,
    depends_on_wisp_id VARCHAR(255) NULL,
    depends_on_external VARCHAR(255) NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    UNIQUE KEY uk_wisp_dep_issue_target (issue_id, depends_on_issue_id),
    UNIQUE KEY uk_wisp_dep_wisp_target (issue_id, depends_on_wisp_id),
    UNIQUE KEY uk_wisp_dep_external_target (issue_id, depends_on_external),
    INDEX idx_wisp_dep_type (type),
    INDEX idx_wisp_dep_type_issue (type, depends_on_issue_id),
    INDEX idx_wisp_dep_type_wisp (type, depends_on_wisp_id),
    INDEX idx_wisp_dep_type_external (type, depends_on_external),
    CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT ck_wisp_dep_one_target CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1)
);`

// ensureIssuesRigColumns repairs #4502: the rig/agent columns were only ever
// added to the squashed bootstrap 0001_create_issues, so a database
// bootstrapped before they existed reaches schema v52 without them, and
// migration 0053 — which copies exactly these columns from wisps into
// issues — fails with "Unknown column" even with zero rig wisps to repair.
// Databases in the wild may have some but not all six, so each is checked
// individually. Definitions mirror the current bootstrap schema.
func ensureIssuesRigColumns(ctx context.Context, db DBConn) error {
	columns := []struct{ name, definition string }{
		{"hook_bead", "VARCHAR(255) DEFAULT ''"},
		{"role_bead", "VARCHAR(255) DEFAULT ''"},
		{"agent_state", "VARCHAR(32) DEFAULT ''"},
		{"last_activity", "DATETIME"},
		{"role_type", "VARCHAR(32) DEFAULT ''"},
		{"rig", "VARCHAR(255) DEFAULT ''"},
	}
	for _, col := range columns {
		present, err := schemaColumnExists(ctx, db, "issues", col.name)
		if err != nil {
			return fmt.Errorf("checking issues.%s: %w", col.name, err)
		}
		if present {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE issues ADD COLUMN "+col.name+" "+col.definition); err != nil {
			return fmt.Errorf("adding issues.%s for migration 0053: %w", col.name, err)
		}
	}
	return nil
}

// ensureWispDependenciesSplitTargets repairs #4555: a mixed-vintage local
// wisp_dependencies table can have the post-0005 id column while still lacking
// one or more split target columns. Migration 0053 reads those columns when it
// repairs rig wisps, so add the missing columns and backfill them from the
// legacy depends_on_id column when that source column is still available.
func ensureWispDependenciesSplitTargets(ctx context.Context, db DBConn) error {
	table, err := schemaTableExists(ctx, db, "wisp_dependencies")
	if err != nil {
		return fmt.Errorf("checking wisp_dependencies table: %w", err)
	}
	if !table {
		return nil
	}

	columns := wispDependenciesSplitTargetColumns()
	missing := make([]struct{ name, definition string }, 0, len(columns))
	for _, col := range columns {
		present, err := schemaColumnExists(ctx, db, "wisp_dependencies", col.name)
		if err != nil {
			return fmt.Errorf("checking wisp_dependencies.%s: %w", col.name, err)
		}
		if !present {
			missing = append(missing, col)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	for _, col := range missing {
		if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies ADD COLUMN "+col.name+" "+col.definition); err != nil {
			return fmt.Errorf("adding wisp_dependencies.%s for migration 0053: %w", col.name, err)
		}
	}

	legacyTarget, err := schemaColumnExists(ctx, db, "wisp_dependencies", "depends_on_id")
	if err != nil {
		return fmt.Errorf("checking wisp_dependencies.depends_on_id: %w", err)
	}
	if !legacyTarget {
		return nil
	}

	for _, repair := range wispDependenciesSplitTargetBackfillSQL() {
		if _, err := db.ExecContext(ctx, repair); err != nil {
			return fmt.Errorf("backfilling wisp_dependencies split targets for migration 0053: %w", err)
		}
	}
	return nil
}

func wispDependenciesSplitTargetColumns() []struct{ name, definition string } {
	return []struct{ name, definition string }{
		{"depends_on_issue_id", "VARCHAR(255) NULL"},
		{"depends_on_wisp_id", "VARCHAR(255) NULL"},
		{"depends_on_external", "VARCHAR(255) NULL"},
	}
}

func wispDependenciesSplitTargetBackfillSQL() []string {
	return []string{
		"UPDATE wisp_dependencies SET depends_on_external = depends_on_id WHERE depends_on_external IS NULL AND depends_on_id LIKE 'external:%'",
		"UPDATE wisp_dependencies wd JOIN wisps w ON w.id = wd.depends_on_id SET wd.depends_on_wisp_id = wd.depends_on_id WHERE wd.depends_on_wisp_id IS NULL AND wd.depends_on_external IS NULL",
		"UPDATE wisp_dependencies wd JOIN issues i ON i.id = wd.depends_on_id SET wd.depends_on_issue_id = wd.depends_on_id WHERE wd.depends_on_issue_id IS NULL AND wd.depends_on_external IS NULL AND wd.depends_on_wisp_id IS NULL",
		"UPDATE wisp_dependencies SET depends_on_external = depends_on_id WHERE depends_on_external IS NULL AND depends_on_wisp_id IS NULL AND depends_on_issue_id IS NULL",
	}
}

func schemaTableExists(ctx context.Context, db DBConn, table string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
	`, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func schemaColumnExists(ctx context.Context, db DBConn, table, column string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?
	`, table, column).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
