package schema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/depid"
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
		if err := ensureWispDependenciesSplitTargets(ctx, db); err != nil {
			return err
		}
		return ensureDependenciesIDColumn(ctx, db)
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

// ensureDependenciesIDColumn repairs #4690: on a database where
// dependencies.id was never added (a different historical migration path
// than this repo's 0043_drop_dependencies_generated_column, which adds it --
// the same class of cross-clone content drift #4259's audit epic targets),
// migration 0053's rig-wisp repair blocks reference "dependencies(id, ...)"
// and "d.id" unconditionally and fail with "Unknown column 'id'" even with
// zero rig wisps to migrate.
//
// Restore the column and its key to 0043's exact canonical end state --
// "id CHAR(36) NOT NULL ... PRIMARY KEY" -- not just a plain NOT NULL column.
// 0053's own "REPLACE INTO dependencies (id, ...)" matches rows on the
// uk_dep_* natural-identity unique keys; an unkeyed id lets a REPLACE that
// hits a row whose old depends_on_wisp_id is NULL (so uk_dep_wisp_target
// doesn't match) fall through to INSERT, duplicating the edge under a new id
// while the stale row survives. Restoring id as the PRIMARY KEY is what makes
// REPLACE's own conflict detection do its job.
//
// This is deliberately re-entrant rather than a single "column present ->
// nil" gate: preMigrationRepair's mutations to a synced table like
// dependencies land in the same atomic per-step commit as migration 0053
// (see runMigrations' dirty-table-snapshot ordering), but a process killed
// mid-repair -- after ADD COLUMN, before the backfill or the key finishes --
// still needs the NEXT open's repair call to finish the job rather than
// short-circuit on "column exists". Every step below re-verifies its own
// target state instead of trusting an earlier step ran to completion.
func ensureDependenciesIDColumn(ctx context.Context, db DBConn) error {
	hasID, err := schemaColumnExists(ctx, db, "dependencies", "id")
	if err != nil {
		return fmt.Errorf("checking dependencies.id: %w", err)
	}
	if !hasID {
		if _, err := db.ExecContext(ctx, "ALTER TABLE dependencies ADD COLUMN id CHAR(36) NULL"); err != nil {
			return fmt.Errorf("adding dependencies.id for migration 0053: %w", err)
		}
	}

	if err := backfillDependenciesID(ctx, db); err != nil {
		return err
	}
	return ensureDependenciesIDPrimaryKey(ctx, db)
}

// backfillDependenciesID fills in any dependencies.id still NULL with
// depid.New(issue_id, target) -- the same deterministic id every insert path
// and the post-migration rekeyDependencyIDs pass use (dep_id_backfill.go) --
// so rows with real edges get a real, cross-clone-stable id rather than a
// throwaway placeholder, and rekeyDependencyIDs finds nothing left to correct
// afterwards. The `WHERE id IS NULL` scope (rather than every row) is what
// makes re-entry after a partial prior run cheap and idempotent: a row this
// function already backfilled, or one that already had an id, is untouched.
func backfillDependenciesID(ctx context.Context, db DBConn) error {
	rows, err := db.QueryContext(ctx, `
		SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external
		FROM dependencies
		WHERE id IS NULL
	`)
	if err != nil {
		return fmt.Errorf("reading dependencies rows for migration 0053 id backfill: %w", err)
	}
	type edge struct {
		issueID                                              string
		dependsOnIssueID, dependsOnWispID, dependsOnExternal sql.NullString
	}
	var edges []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.issueID, &e.dependsOnIssueID, &e.dependsOnWispID, &e.dependsOnExternal); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scanning dependencies row for migration 0053 id backfill: %w", err)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating dependencies rows for migration 0053 id backfill: %w", err)
	}
	_ = rows.Close()

	for _, e := range edges {
		target := firstNonNullString(e.dependsOnIssueID, e.dependsOnWispID, e.dependsOnExternal)
		if target == "" {
			// ck_dep_one_target (0041) should make a targetless row
			// unreachable; if one exists anyway, leave its id NULL here --
			// ensureDependenciesIDPrimaryKey below checks for exactly this
			// and fails loudly with an actionable count instead of letting a
			// blind MODIFY ... NOT NULL hard-fail on it, or silently keying
			// the table while pretending the row doesn't exist.
			continue
		}
		id := depid.New(e.issueID, target)
		if _, err := db.ExecContext(ctx, `
			UPDATE dependencies SET id = ?
			WHERE issue_id = ?
			  AND depends_on_issue_id <=> ?
			  AND depends_on_wisp_id <=> ?
			  AND depends_on_external <=> ?
		`, id, e.issueID, e.dependsOnIssueID, e.dependsOnWispID, e.dependsOnExternal); err != nil {
			return fmt.Errorf("backfilling dependencies.id for migration 0053: %w", err)
		}
	}
	return nil
}

// ensureDependenciesIDPrimaryKey finishes restoring dependencies.id to 0043's
// canonical shape: NOT NULL and the table's PRIMARY KEY. It re-verifies both
// independently of whether this pass just backfilled anything, so a re-entry
// after a crash between the backfill and the key (or between MODIFY NOT NULL
// and ADD PRIMARY KEY) finishes the remaining step(s) instead of re-running
// ones already done -- MODIFY COLUMN restating an identical definition and
// re-adding an already-present PRIMARY KEY are otherwise either redundant or
// outright rejected as a duplicate key.
func ensureDependenciesIDPrimaryKey(ctx context.Context, db DBConn) error {
	var remainingNull int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies WHERE id IS NULL").Scan(&remainingNull); err != nil {
		return fmt.Errorf("counting unbackfilled dependencies.id rows for migration 0053: %w", err)
	}
	if remainingNull > 0 {
		// Fail with an actionable count now rather than let a subsequent
		// MODIFY COLUMN ... NOT NULL below abort with a generic "column
		// cannot be null" error, or silently key the table while leaving
		// NULL-id rows behind it.
		return fmt.Errorf("migration 0053: %d dependencies row(s) have no depends_on_issue_id/depends_on_wisp_id/depends_on_external target and cannot be assigned an id (ck_dep_one_target should prevent this); repair manually before retrying", remainingNull)
	}

	idIsPrimaryKey, err := schemaColumnInPrimaryKey(ctx, db, "dependencies", "id")
	if err != nil {
		return fmt.Errorf("checking dependencies.id primary key: %w", err)
	}
	if idIsPrimaryKey {
		return nil
	}

	if _, err := db.ExecContext(ctx, "ALTER TABLE dependencies MODIFY COLUMN id CHAR(36) NOT NULL"); err != nil {
		return fmt.Errorf("finalizing dependencies.id for migration 0053: %w", err)
	}

	hasAnyPrimaryKey, err := schemaHasPrimaryKey(ctx, db, "dependencies")
	if err != nil {
		return fmt.Errorf("checking dependencies for an existing primary key: %w", err)
	}
	if hasAnyPrimaryKey {
		// The #4690 drifted shape has dependencies keyed some other way (or
		// keyless): a table can carry only one PRIMARY KEY, so whatever is
		// there must go before id can become it. The uk_dep_* natural-identity
		// unique keys (0043) enforce the real uniqueness independently of
		// whatever this was, so dropping it is safe.
		if _, err := db.ExecContext(ctx, "ALTER TABLE dependencies DROP PRIMARY KEY"); err != nil {
			return fmt.Errorf("dropping dependencies' existing primary key for migration 0053: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE dependencies ADD PRIMARY KEY (id)"); err != nil {
		return fmt.Errorf("keying dependencies.id for migration 0053: %w", err)
	}
	return nil
}

// firstNonNullString returns the first valid (non-NULL) value among cols, or
// "" if all are NULL.
func firstNonNullString(cols ...sql.NullString) string {
	for _, c := range cols {
		if c.Valid {
			return c.String
		}
	}
	return ""
}

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
//
// Column presence is not, by itself, proof the backfill below ever ran: a
// process killed after the ADD COLUMNs but before the backfill leaves all
// three target columns present but unpopulated. Re-entry must not
// short-circuit on "columns exist" -- it re-runs the backfill whenever
// depends_on_id (the legacy source the backfill reads from) is still around,
// which is itself idempotent (each statement below is scoped to the rows it
// hasn't yet filled in). Skipping this matters because a later ignored
// migration (0005) drops depends_on_id once it assumes the split is done;
// after that the source data needed to finish an interrupted backfill is
// gone for good.
func ensureWispDependenciesSplitTargets(ctx context.Context, db DBConn) error {
	table, err := schemaTableExists(ctx, db, "wisp_dependencies")
	if err != nil {
		return fmt.Errorf("checking wisp_dependencies table: %w", err)
	}
	if !table {
		return nil
	}

	for _, col := range wispDependenciesSplitTargetColumns() {
		present, err := schemaColumnExists(ctx, db, "wisp_dependencies", col.name)
		if err != nil {
			return fmt.Errorf("checking wisp_dependencies.%s: %w", col.name, err)
		}
		if !present {
			if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies ADD COLUMN "+col.name+" "+col.definition); err != nil {
				return fmt.Errorf("adding wisp_dependencies.%s for migration 0053: %w", col.name, err)
			}
		}
	}

	legacyTarget, err := schemaColumnExists(ctx, db, "wisp_dependencies", "depends_on_id")
	if err != nil {
		return fmt.Errorf("checking wisp_dependencies.depends_on_id: %w", err)
	}
	if !legacyTarget {
		// Nothing left to backfill from: either a prior pass already
		// finished (depends_on_id has since been dropped) or this database
		// never had the legacy column to begin with.
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

// schemaHasPrimaryKey reports whether table currently carries a PRIMARY KEY
// constraint, regardless of which column(s) compose it.
func schemaHasPrimaryKey(ctx context.Context, db DBConn, table string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_TYPE = 'PRIMARY KEY'
	`, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// schemaColumnInPrimaryKey reports whether column is (one of) table's PRIMARY
// KEY column(s) specifically -- distinct from schemaHasPrimaryKey, which only
// says a primary key exists without saying which column(s) it covers.
func schemaColumnInPrimaryKey(ctx context.Context, db DBConn, table, column string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY'
	`, table, column).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
