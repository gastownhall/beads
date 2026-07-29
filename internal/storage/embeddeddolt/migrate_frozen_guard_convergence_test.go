//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// This file covers two coverage gaps flagged in gastownhall/beads#4878's
// review (mybd-i9un): migration 0057's guarded TEXT->LONGTEXT conversion
// branch, and 0047's Go-level delegate repair converging through the ignored
// sequence's own structural split (#4555).
//
// Both were first attempted as dolt-CLI (`dolt sql -f`) tests, matching the
// existing ThroughDoltCLI pattern in internal/storage/schema/schema_test.go.
// That approach does not work for either gap: dolt CLI v2.2.2 (the current
// "latest" release, matching what CI installs) silently no-ops any
// PREPARE/EXECUTE-driven ALTER TABLE run through `dolt sql -f`/`-q`/piped
// stdin -- confirmed by direct repro, not just by these two tests failing.
// A plain (non-prepared) ALTER TABLE always applies; a PREPARE'd one, guarded
// or not, never does, and raises no error either. Every one of 0057, 0047's
// own inline wisp_dependencies split, and ignored/0003 and ignored/0005 use
// exactly this `SET @sql = IF(...); PREPARE stmt FROM @sql; EXECUTE stmt;`
// shape, so no existing ThroughDoltCLI test has ever actually exercised one
// of these guards' "fires and converts" branch: each either substitutes
// hand-written direct DDL (cli_migrations.go's cliCompatibleMigrationSQL) or
// only proves the no-op branch. A substitute would not catch a bug in the
// real guarded SQL either, which is precisely what the #4878 review nits
// asked this coverage to catch.
//
// These tests instead run the exact frozen migration file content through
// the same path production uses: a single db.ExecContext(ctx, string(data))
// against a real embedded (cgo) go-mysql-server/Dolt engine -- see
// schema.go's runMigrations, and schema.MigrateUp for the full orchestration
// including preMigrationRepair. PREPARE/EXECUTE works normally through that
// real SQL engine; the limitation above is specific to the dolt CLI's
// `sql -f`/`-q` batch executor, which nothing in production uses.
//
// Both tests require BEADS_TEST_EMBEDDED_DOLT=1 (requireEmbedded, defined in
// migrate_selfheal_test.go), the same gate every other cgo embedded-dolt test
// in this package uses.

// TestEmbeddedMigration0057ConvertsTextEventsColumnsToLongtext exercises
// 0057's actual MODIFY branch: on any fresh chain 0048 has already widened
// both columns to LONGTEXT, so @old_value_needs_fix/@new_value_needs_fix
// read 0 and the MODIFY statements never fire under any pre-existing test.
// Force the columns back to TEXT after a full migrate, then run 0057's real
// frozen SQL (read from migrations/0057_events_value_columns_idempotent_longtext.up.sql)
// and assert each column converts independently.
func TestEmbeddedMigration0057ConvertsTextEventsColumnsToLongtext(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	migrationSQL, err := schema.MigrationSQL("0057_events_value_columns_idempotent_longtext.up.sql")
	if err != nil {
		t.Fatalf("read 0057 migration: %v", err)
	}

	t.Run("both columns drifted to TEXT", func(t *testing.T) {
		dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		execFrozenGuard(t, ctx, conn, `
ALTER TABLE events MODIFY COLUMN old_value TEXT;
ALTER TABLE events MODIFY COLUMN new_value TEXT;
`)
		requireEventsColumnType(t, ctx, conn, "old_value", "text")
		requireEventsColumnType(t, ctx, conn, "new_value", "text")

		execFrozenGuard(t, ctx, conn, migrationSQL)
		requireEventsColumnType(t, ctx, conn, "old_value", "longtext")
		requireEventsColumnType(t, ctx, conn, "new_value", "longtext")

		// Idempotent: a second pass, with both guards now reading 0, must
		// take the no-op branch for both columns rather than re-issue MODIFY
		// (the #4353 encoding-flip risk 0057 exists to guard against).
		execFrozenGuard(t, ctx, conn, migrationSQL)
		requireEventsColumnType(t, ctx, conn, "old_value", "longtext")
		requireEventsColumnType(t, ctx, conn, "new_value", "longtext")
	})

	t.Run("mixed: only old_value drifted to TEXT", func(t *testing.T) {
		dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		// The header comment's motivating case: a database that picked up
		// 0048 unevenly, so one column already reads LONGTEXT while the
		// other is still TEXT. The two guards must act independently -- a
		// single shared check would either skip converting old_value here or
		// re-issue a MODIFY on new_value despite it already being LONGTEXT.
		execFrozenGuard(t, ctx, conn, "ALTER TABLE events MODIFY COLUMN old_value TEXT;")
		requireEventsColumnType(t, ctx, conn, "old_value", "text")
		requireEventsColumnType(t, ctx, conn, "new_value", "longtext")

		execFrozenGuard(t, ctx, conn, migrationSQL)
		requireEventsColumnType(t, ctx, conn, "old_value", "longtext")
		requireEventsColumnType(t, ctx, conn, "new_value", "longtext")

		execFrozenGuard(t, ctx, conn, migrationSQL)
		requireEventsColumnType(t, ctx, conn, "old_value", "longtext")
		requireEventsColumnType(t, ctx, conn, "new_value", "longtext")
	})
}

// TestEmbeddedMigration0047DelegatesToSplitTargetRepairAndConvergesThroughIgnoredChain
// covers #4555's delegate path end-to-end: wisp_dependencies present but
// still in its legacy pre-split shape (only depends_on_id, no
// issue/wisp/external target columns) while wisps (and every other wisp_*
// aux table) are present and current. migration_repairs.go's
// ensureWispTablesForMixedBlockedRecompute finds both wisp tables already
// present and delegates to ensureWispDependenciesSplitTargets rather than
// recreating either table; that delegate adds the three split columns and
// backfills them from depends_on_id but does NOT drop depends_on_id or
// restore the surrogate id primary key -- it flips 0047's own inline
// @wisp_dependencies_needs_split guard to 0 and defers the structural split
// (dropping depends_on_id, adding id, restoring the uk_/fk_/ck_ constraints)
// to the ignored sequence's own migration 0005.
//
// This was previously only covered by a sqlmock unit test
// (TestEnsureWispTablesForMigration0047DelegatesSplitTargetRepairWhenWispDependenciesExists,
// internal/storage/schema/schema_test.go) with the split columns already
// present. This drives the real legacy interaction through the actual
// production entrypoint, schema.MigrateUp -- which runs the main sequence
// (invoking the Go-level delegate repair for version 47), then the ignored
// sequence (applying ignored/0003 and ignored/0005's own frozen, PREPARE'd
// SQL for real) -- and asserts the same canonical convergence
// TestFullChainFromPreWispsAndMissingDependenciesIDConvergesThroughDoltCLI
// (schema_test.go) asserts for the parallel dependencies-table repair,
// applied here to wisp_dependencies.
func TestEmbeddedMigration0047DelegatesToSplitTargetRepairAndConvergesThroughIgnoredChain(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 46)

	const blockerIssueID = "wisp-deps-delegate-blocker"
	const blockedWispID = "wisp-deps-delegate-blocked"

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	// Reproduce #4555: recreate wisp_dependencies in the legacy pre-split
	// shape -- the same hand-written shape
	// TestWispDependenciesSplitTargetBackfillPrefersWispOverIssueThroughDoltCLI
	// (schema_test.go) uses -- and seed one wisp-depends-on-issue edge
	// through the legacy depends_on_id column.
	if _, err := conn.ExecContext(ctx, "DROP TABLE wisp_dependencies"); err != nil {
		closeConn()
		t.Fatalf("drop wisp_dependencies: %v", err)
	}
	// Unlike TestWispDependenciesSplitTargetBackfillPrefersWispOverIssueThroughDoltCLI's
	// minimal legacy shape (which only needs to prove backfill correctness),
	// this one carries a realistic fk_wisp_dep_issue foreign key: ignored/0005
	// only restores a FK after the structural split if @has_fk_<name> found it
	// present BEFORE the drop, so a FK-less seed would trivially (and
	// misleadingly) converge without ever exercising that preserve-and-restore
	// path.
	if _, err := conn.ExecContext(ctx, `
CREATE TABLE wisp_dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    PRIMARY KEY (issue_id, depends_on_id),
    CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE
)`); err != nil {
		closeConn()
		t.Fatalf("create legacy wisp_dependencies: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'blocker issue', '', '', '', '', 'open', 2, 'task')",
		blockerIssueID); err != nil {
		closeConn()
		t.Fatalf("insert blocker issue: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES (?, 'blocked wisp', '', '', '', '', 'open', 2, 'task', 1)",
		blockedWispID); err != nil {
		closeConn()
		t.Fatalf("insert blocked wisp: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata) VALUES (?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())",
		blockedWispID, blockerIssueID); err != nil {
		closeConn()
		t.Fatalf("insert legacy wisp_dependencies edge: %v", err)
	}
	// Commit the seed before calling MigrateUp: `issues` is a synced table,
	// and several pending main migrations (47's own recompute among them)
	// touch it, so leaving the insert uncommitted would trip MigrateUp's
	// pre-existing-dirty-table guard (same reasoning
	// TestEmbeddedMigrateRepairedDependenciesIDColumnCommitsAtomicallyWithVersion53_4690
	// documents for its own seed). wisps/wisp_dependencies are dolt_ignore'd
	// and exempt from that guard either way.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		closeConn()
		t.Fatalf("stage legacy wisp_dependencies seed: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'test: reproduce #4555 legacy wisp_dependencies')"); err != nil {
		closeConn()
		t.Fatalf("commit legacy wisp_dependencies seed: %v", err)
	}
	closeConn()

	conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
	defer closeConn2()
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	if v := currentMainVersion(t, ctx, conn2); v != schema.LatestVersion() {
		t.Fatalf("main schema version = %d, want latest %d", v, schema.LatestVersion())
	}
	var ignoredVersion int
	if err := conn2.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM ignored_schema_migrations").Scan(&ignoredVersion); err != nil {
		t.Fatalf("read ignored schema version: %v", err)
	}
	if ignoredVersion != schema.LatestIgnoredVersion() {
		t.Fatalf("ignored schema version = %d, want latest %d", ignoredVersion, schema.LatestIgnoredVersion())
	}

	// Canonical convergence: the same shape
	// TestFullChainFromPreWispsAndMissingDependenciesIDConvergesThroughDoltCLI
	// asserts for the parallel dependencies-table repair, applied here to
	// wisp_dependencies.
	requireColumnCount(t, ctx, conn2, "wisp_dependencies", "depends_on_issue_id", 1)
	requireColumnCount(t, ctx, conn2, "wisp_dependencies", "depends_on_wisp_id", 1)
	requireColumnCount(t, ctx, conn2, "wisp_dependencies", "depends_on_external", 1)
	requireColumnCount(t, ctx, conn2, "wisp_dependencies", "depends_on_id", 0)
	requireColumnCount(t, ctx, conn2, "wisp_dependencies", "id", 1)

	var idIsPrimaryKey int
	if err := conn2.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
  AND COLUMN_NAME = 'id' AND CONSTRAINT_NAME = 'PRIMARY'`).Scan(&idIsPrimaryKey); err != nil {
		t.Fatalf("check wisp_dependencies.id primary key: %v", err)
	}
	if idIsPrimaryKey != 1 {
		t.Fatalf("wisp_dependencies.id primary key rows = %d, want 1", idIsPrimaryKey)
	}

	for _, name := range []string{"uk_wisp_dep_issue_target", "uk_wisp_dep_wisp_target", "uk_wisp_dep_external_target"} {
		requireConstraintCount(t, ctx, conn2, name, "", 1)
	}
	for _, name := range []string{"fk_wisp_dep_issue", "fk_wisp_dep_wisp_target", "fk_wisp_dep_issue_target"} {
		requireConstraintCount(t, ctx, conn2, name, "FOREIGN KEY", 1)
	}
	requireConstraintCount(t, ctx, conn2, "ck_wisp_dep_one_target", "", 1)
	// Bare name/type presence isn't proof these reference the right
	// table/column with CASCADE actions -- confirm via KEY_COLUMN_USAGE and
	// REFERENTIAL_CONSTRAINTS, and exercise ck_wisp_dep_one_target
	// behaviorally (PR #4987 review, minor).
	requireForeignKeyReferences(t, ctx, conn2, "fk_wisp_dep_wisp_target", "depends_on_wisp_id", "wisps", "id")
	requireForeignKeyReferences(t, ctx, conn2, "fk_wisp_dep_issue_target", "depends_on_issue_id", "issues", "id")
	requireForeignKeyReferences(t, ctx, conn2, "fk_wisp_dep_issue", "issue_id", "wisps", "id")

	// The single legacy row backfilled to its issue target (not wisp or
	// external) and survived the structural split unchanged.
	var rowCount int
	if err := conn2.QueryRowContext(ctx, `
SELECT COUNT(*) FROM wisp_dependencies
WHERE issue_id = ? AND depends_on_issue_id = ? AND depends_on_wisp_id IS NULL AND depends_on_external IS NULL`,
		blockedWispID, blockerIssueID).Scan(&rowCount); err != nil {
		t.Fatalf("count backfilled wisp_dependencies row: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("backfilled wisp_dependencies row count = %d, want 1", rowCount)
	}
	var totalRows int
	if err := conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies").Scan(&totalRows); err != nil {
		t.Fatalf("count wisp_dependencies rows: %v", err)
	}
	if totalRows != 1 {
		t.Fatalf("wisp_dependencies row count = %d, want 1", totalRows)
	}

	// Behavioral CHECK probe: dedicated fresh wisp/issue, and the helper
	// deletes its own successful probe row, so this doesn't disturb the row
	// count just asserted or the clean-working-set check below.
	const (
		checkProbeWispID  = "wisp-deps-delegate-check-probe"
		checkProbeIssueID = "issue-deps-delegate-check-probe"
	)
	if _, err := conn2.ExecContext(ctx,
		"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES (?, 'check probe wisp', '', '', '', '', 'open', 2, 'task', 1)",
		checkProbeWispID); err != nil {
		t.Fatalf("insert check probe wisp: %v", err)
	}
	if _, err := conn2.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'check probe issue', '', '', '', '', 'open', 2, 'task')",
		checkProbeIssueID); err != nil {
		t.Fatalf("insert check probe issue: %v", err)
	}
	requireCheckRejectsInvalidTargetCounts(t, ctx, conn2, checkProbeWispID, checkProbeIssueID)
	if _, err := conn2.ExecContext(ctx, "DELETE FROM wisps WHERE id = ?", checkProbeWispID); err != nil {
		t.Fatalf("cleanup check probe wisp: %v", err)
	}
	if _, err := conn2.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", checkProbeIssueID); err != nil {
		t.Fatalf("cleanup check probe issue: %v", err)
	}

	if dirty := dirtyTableNames(t, ctx, conn2); len(dirty) != 0 {
		t.Fatalf("working set after MigrateUp is dirty: %v, want clean", dirty)
	}
}

// TestEmbeddedMigration0058HealsAlreadyAffectedDatabase covers the case
// TestEmbeddedMigration0047DelegatesToSplitTargetRepairAndConvergesThroughIgnoredChain
// above cannot: a database that already took the 0047 delegate path (and is
// therefore already missing fk_wisp_dep_wisp_target,
// fk_wisp_dep_issue_target, and ck_wisp_dep_one_target) BEFORE migration
// 0058 existed, so its schema_migrations cursor is already sitting at the
// old latest version. Migrations 0047, ignored/0003, and ignored/0005 are
// frozen and never pending again on that database -- only a NEW migration
// (0058) can reach it. Simulate exactly that: take an otherwise fully
// converged database (built through the real chain, so every other aspect
// of the shape -- columns, backfill, surrogate id, primary key, unique keys
// -- is genuinely correct) and drop just the three constraints 0058 heals,
// mirroring the real gap
// TestEmbeddedMigration0047DelegatesToSplitTargetRepairAndConvergesThroughIgnoredChain
// found before the fix. Then run 0058's real frozen SQL directly (the same
// execFrozenGuard pattern the 0057 test above uses) and assert each
// constraint appears independently, and that a second pass is an idempotent
// no-op.
func TestEmbeddedMigration0058HealsAlreadyAffectedDatabase(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	migrationSQL, err := schema.MigrationSQL("0058_heal_wisp_dependencies_split_constraints.up.sql")
	if err != nil {
		t.Fatalf("read 0058 migration: %v", err)
	}

	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	execFrozenGuard(t, ctx, conn, `
ALTER TABLE wisp_dependencies DROP CONSTRAINT ck_wisp_dep_one_target;
ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_wisp_target;
ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_issue_target;
`)
	requireConstraintCount(t, ctx, conn, "ck_wisp_dep_one_target", "", 0)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_wisp_target", "FOREIGN KEY", 0)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_issue_target", "FOREIGN KEY", 0)
	// fk_wisp_dep_issue (the base issue_id FK, untouched by this drop) and
	// the uk_ unique keys were never affected by the delegate-path gap;
	// confirm the seed didn't disturb them either, so the healed run below
	// is proven to add exactly the three missing constraints, not paper
	// over a broader drift.
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_issue", "FOREIGN KEY", 1)
	for _, name := range []string{"uk_wisp_dep_issue_target", "uk_wisp_dep_wisp_target", "uk_wisp_dep_external_target"} {
		requireConstraintCount(t, ctx, conn, name, "", 1)
	}

	execFrozenGuard(t, ctx, conn, migrationSQL)
	requireConstraintCount(t, ctx, conn, "ck_wisp_dep_one_target", "", 1)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_wisp_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_issue_target", "FOREIGN KEY", 1)
	requireForeignKeyReferences(t, ctx, conn, "fk_wisp_dep_wisp_target", "depends_on_wisp_id", "wisps", "id")
	requireForeignKeyReferences(t, ctx, conn, "fk_wisp_dep_issue_target", "depends_on_issue_id", "issues", "id")

	// Behavioral CHECK probe on a dedicated fresh wisp/issue pair; the
	// helper cleans up its own successful probe row.
	const (
		checkProbeWispID  = "wisp-0058-heal-check-probe"
		checkProbeIssueID = "issue-0058-heal-check-probe"
	)
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES (?, 'check probe wisp', '', '', '', '', 'open', 2, 'task', 1)",
		checkProbeWispID); err != nil {
		t.Fatalf("insert check probe wisp: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'check probe issue', '', '', '', '', 'open', 2, 'task')",
		checkProbeIssueID); err != nil {
		t.Fatalf("insert check probe issue: %v", err)
	}
	requireCheckRejectsInvalidTargetCounts(t, ctx, conn, checkProbeWispID, checkProbeIssueID)

	// Idempotent: a second pass (e.g. a retry, or MigrateUp re-run after the
	// cursor already recorded 0058) must not error re-adding a constraint
	// that is already present.
	execFrozenGuard(t, ctx, conn, migrationSQL)
	requireConstraintCount(t, ctx, conn, "ck_wisp_dep_one_target", "", 1)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_wisp_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_issue_target", "FOREIGN KEY", 1)
}

// TestEmbeddedMigration0058AddsOnlyMissingConstraintInMixedPopulation covers
// the PR #4987 review's minor: a database missing only ONE of the three
// constraints (here, ck_wisp_dep_one_target -- both foreign keys already
// present) must have 0058 add exactly that one, without erroring on the two
// already-present foreign keys (each guarded independently, so a converged
// constraint's own @needs_* flag reads 0 and its ADD CONSTRAINT takes the
// no-op branch).
func TestEmbeddedMigration0058AddsOnlyMissingConstraintInMixedPopulation(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	migrationSQL, err := schema.MigrationSQL("0058_heal_wisp_dependencies_split_constraints.up.sql")
	if err != nil {
		t.Fatalf("read 0058 migration: %v", err)
	}

	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	execFrozenGuard(t, ctx, conn, "ALTER TABLE wisp_dependencies DROP CONSTRAINT ck_wisp_dep_one_target;")
	requireConstraintCount(t, ctx, conn, "ck_wisp_dep_one_target", "", 0)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_wisp_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_issue_target", "FOREIGN KEY", 1)

	execFrozenGuard(t, ctx, conn, migrationSQL)
	requireConstraintCount(t, ctx, conn, "ck_wisp_dep_one_target", "", 1)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_wisp_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn, "fk_wisp_dep_issue_target", "FOREIGN KEY", 1)
}

// TestEmbeddedMigration0058CleansOrphanedAndInvalidWispDependenciesRowsBeforeAddingConstraints
// covers the two majors from the paired review on PR #4987: adding a foreign
// key over data that accumulated during the window it was missing repeats
// the #4534 failure class ignored/0011 documents ("Dolt then fails
// constraint validation on subsequent writes, so one legacy orphan bricks
// every bd create"), and ADD CONSTRAINT ck_wisp_dep_one_target validates
// existing rows regardless of FOREIGN_KEY_CHECKS, so a zero- or multi-target
// row accumulated in the same window would abort the ADD CONSTRAINT itself,
// repeatedly failing MigrateUp before it ever records 0058.
//
// Seed exactly the three row shapes that unconstrained window can leave, all
// on a delegate-path database at cursor-latest (built through the real
// chain, then the three constraints dropped, matching
// TestEmbeddedMigration0058HealsAlreadyAffectedDatabase's technique):
//   - an FK orphan: a valid depends_on_wisp_id edge, then its target wisp
//     deleted with no CASCADE in force (the FK is dropped) to leave the
//     edge dangling.
//   - a zero-target row: no issue, no wisp, no external target.
//   - a multi-target row: both a valid wisp target and a valid issue target
//     set at once.
//
// Run 0058 and assert the orphan and zero-target rows are gone, the
// multi-target row is normalized to its single wisp-precedence target, all
// three constraints converge, and a second pass is an idempotent no-op.
func TestEmbeddedMigration0058CleansOrphanedAndInvalidWispDependenciesRowsBeforeAddingConstraints(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	migrationSQL, err := schema.MigrationSQL("0058_heal_wisp_dependencies_split_constraints.up.sql")
	if err != nil {
		t.Fatalf("read 0058 migration: %v", err)
	}

	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	execFrozenGuard(t, ctx, conn, `
ALTER TABLE wisp_dependencies DROP CONSTRAINT ck_wisp_dep_one_target;
ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_wisp_target;
ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_issue_target;
`)

	const (
		orphanSourceID     = "wisp-0058-orphan-source"
		danglingWispTarget = "wisp-0058-dangling-target"
		zeroSourceID       = "wisp-0058-zero-source"
		multiSourceID      = "wisp-0058-multi-source"
		multiWispTarget    = "wisp-0058-multi-wisp-target"
		multiIssueTarget   = "issue-0058-multi-issue-target"
	)
	for _, id := range []string{orphanSourceID, danglingWispTarget, zeroSourceID, multiSourceID, multiWispTarget} {
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES (?, ?, '', '', '', '', 'open', 2, 'task', 1)",
			id, id); err != nil {
			t.Fatalf("insert wisp %s: %v", id, err)
		}
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
		multiIssueTarget, multiIssueTarget); err != nil {
		t.Fatalf("insert issue %s: %v", multiIssueTarget, err)
	}

	// FK orphan: a valid edge to danglingWispTarget, then delete that wisp
	// out from under it -- with the FK dropped, nothing cascades, leaving a
	// row whose target no longer resolves.
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (issue_id, depends_on_wisp_id, type, created_at, created_by, metadata) VALUES (?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())",
		orphanSourceID, danglingWispTarget); err != nil {
		t.Fatalf("insert orphan-to-be row: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM wisps WHERE id = ?", danglingWispTarget); err != nil {
		t.Fatalf("delete dangling wisp target: %v", err)
	}

	// Zero-target: no issue, no wisp, no external target -- semantically
	// meaningless, and would fail ck_wisp_dep_one_target's ADD CONSTRAINT.
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (issue_id, type, created_at, created_by, metadata) VALUES (?, 'blocks', NOW(), 'tester', JSON_OBJECT())",
		zeroSourceID); err != nil {
		t.Fatalf("insert zero-target row: %v", err)
	}

	// Multi-target: both a valid wisp target and a valid issue target set at
	// once. wispDependenciesSplitTargetBackfillSQL's own statement order
	// (external-prefix match first, then wisp, then issue, each guarded on
	// the earlier ones not having already matched) and its sibling test
	// TestWispDependenciesSplitTargetBackfillPrefersWispOverIssueThroughDoltCLI
	// (an id resolving as both a wisp and an issue backfills to the wisp
	// reading) establish wisp > issue precedence; 0058 must normalize this
	// row to depends_on_wisp_id only.
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (issue_id, depends_on_wisp_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())",
		multiSourceID, multiWispTarget, multiIssueTarget); err != nil {
		t.Fatalf("insert multi-target row: %v", err)
	}
	closeConn()

	conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
	defer closeConn2()

	execFrozenGuard(t, ctx, conn2, migrationSQL)

	var orphanCount, zeroCount int
	if err := conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", orphanSourceID).Scan(&orphanCount); err != nil {
		t.Fatalf("count orphan row: %v", err)
	}
	if orphanCount != 0 {
		t.Fatalf("orphan row count = %d, want 0 (0058 must delete rows whose target no longer resolves)", orphanCount)
	}
	if err := conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", zeroSourceID).Scan(&zeroCount); err != nil {
		t.Fatalf("count zero-target row: %v", err)
	}
	if zeroCount != 0 {
		t.Fatalf("zero-target row count = %d, want 0", zeroCount)
	}

	var multiWisp, multiIssue sql.NullString
	if err := conn2.QueryRowContext(ctx,
		"SELECT depends_on_wisp_id, depends_on_issue_id FROM wisp_dependencies WHERE issue_id = ?",
		multiSourceID).Scan(&multiWisp, &multiIssue); err != nil {
		t.Fatalf("read normalized multi-target row: %v", err)
	}
	if !multiWisp.Valid || multiWisp.String != multiWispTarget {
		t.Fatalf("multi-target row depends_on_wisp_id = %+v, want %s", multiWisp, multiWispTarget)
	}
	if multiIssue.Valid {
		t.Fatalf("multi-target row depends_on_issue_id = %s, want NULL (wisp precedence)", multiIssue.String)
	}

	requireConstraintCount(t, ctx, conn2, "fk_wisp_dep_wisp_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn2, "fk_wisp_dep_issue_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn2, "ck_wisp_dep_one_target", "", 1)

	// Idempotent: a second pass finds no more orphans or invalid rows (the
	// cleanup guards already read 0, since the constraints are now present)
	// and no error re-adding an already-present constraint; the normalized
	// row survives unchanged.
	execFrozenGuard(t, ctx, conn2, migrationSQL)
	requireConstraintCount(t, ctx, conn2, "fk_wisp_dep_wisp_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn2, "fk_wisp_dep_issue_target", "FOREIGN KEY", 1)
	requireConstraintCount(t, ctx, conn2, "ck_wisp_dep_one_target", "", 1)
	var survivorCount int
	if err := conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", multiSourceID).Scan(&survivorCount); err != nil {
		t.Fatalf("count surviving multi-target row after second pass: %v", err)
	}
	if survivorCount != 1 {
		t.Fatalf("surviving multi-target row count after second pass = %d, want 1", survivorCount)
	}
}

// execFrozenGuard runs a raw SQL blob (typically the exact content of a
// frozen migration file) through a single db.ExecContext call, mirroring
// exactly how schema.go's runMigrations applies a migration's SQL in
// production -- one call per file, not one per statement.
func execFrozenGuard(t *testing.T, ctx context.Context, conn *sql.Conn, sqlText string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, sqlText); err != nil {
		t.Fatalf("exec failed: %v\nSQL:\n%s", err, sqlText)
	}
}

func requireEventsColumnType(t *testing.T, ctx context.Context, conn *sql.Conn, column, want string) {
	t.Helper()
	var got string
	if err := conn.QueryRowContext(ctx, `
SELECT COLUMN_TYPE FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'events' AND COLUMN_NAME = ?`, column).Scan(&got); err != nil {
		t.Fatalf("read events.%s column type: %v", column, err)
	}
	if got != want {
		t.Fatalf("events.%s COLUMN_TYPE = %s, want %s", column, got, want)
	}
}

func requireColumnCount(t *testing.T, ctx context.Context, conn *sql.Conn, table, column string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, column).Scan(&got); err != nil {
		t.Fatalf("read %s.%s column presence: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s.%s column presence count = %d, want %d", table, column, got, want)
	}
}

// requireConstraintCount asserts the given constraint's presence count on
// wisp_dependencies matches want (1 present, 0 absent), optionally scoped to
// a constraint type (pass "" to skip the type filter -- unique keys and
// check constraints both report as CONSTRAINT_TYPE values other than a
// single well-known literal in this INFORMATION_SCHEMA view, so only the
// FOREIGN KEY case is filtered explicitly).
func requireConstraintCount(t *testing.T, ctx context.Context, conn *sql.Conn, name, constraintType string, want int) {
	t.Helper()
	query := `SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND CONSTRAINT_NAME = ?`
	args := []any{name}
	if constraintType != "" {
		query += " AND CONSTRAINT_TYPE = ?"
		args = append(args, constraintType)
	}
	var got int
	if err := conn.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("read wisp_dependencies constraint %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("wisp_dependencies constraint %s count = %d, want %d", name, got, want)
	}
}

// requireForeignKeyReferences asserts a foreign key's referenced
// table/column via INFORMATION_SCHEMA.KEY_COLUMN_USAGE and its
// DELETE_RULE/UPDATE_RULE via INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS --
// requireConstraintCount's bare name/type presence check would still pass a
// foreign key pointed at the wrong table/column or missing its CASCADE
// actions (PR #4987 review, minor).
func requireForeignKeyReferences(t *testing.T, ctx context.Context, conn *sql.Conn, name, column, refTable, refColumn string) {
	t.Helper()
	var gotRefTable, gotRefColumn string
	if err := conn.QueryRowContext(ctx, `
SELECT REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND CONSTRAINT_NAME = ? AND COLUMN_NAME = ?`,
		name, column).Scan(&gotRefTable, &gotRefColumn); err != nil {
		t.Fatalf("read %s KEY_COLUMN_USAGE: %v", name, err)
	}
	if gotRefTable != refTable || gotRefColumn != refColumn {
		t.Fatalf("%s references %s(%s), want %s(%s)", name, gotRefTable, gotRefColumn, refTable, refColumn)
	}
	var updateRule, deleteRule string
	if err := conn.QueryRowContext(ctx, `
SELECT UPDATE_RULE, DELETE_RULE FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND CONSTRAINT_NAME = ?`,
		name).Scan(&updateRule, &deleteRule); err != nil {
		t.Fatalf("read %s REFERENTIAL_CONSTRAINTS: %v", name, err)
	}
	if updateRule != "CASCADE" || deleteRule != "CASCADE" {
		t.Fatalf("%s UPDATE_RULE/DELETE_RULE = %s/%s, want CASCADE/CASCADE", name, updateRule, deleteRule)
	}
}

// requireCheckRejectsInvalidTargetCounts behaviorally exercises
// ck_wisp_dep_one_target rather than string-matching
// INFORMATION_SCHEMA.CHECK_CONSTRAINTS.CHECK_CLAUSE -- the engine reformats
// the clause on storage (e.g. "x IS NOT NULL" becomes "NOT(x IS NULL)"), so
// an exact-text assertion would be brittle against the engine's own
// normalization rather than the migration's actual behavior. Attempts a
// zero-target insert and a two-target insert against sourceWispID (both
// must be rejected), then a valid one-target insert against
// validIssueTargetID (must succeed, proving the constraint isn't simply
// refusing everything) -- and cleans that row back up before returning, so
// callers can use it without disturbing row-count or dolt_status
// assertions elsewhere in the same test. sourceWispID and validIssueTargetID
// must be real rows (via fk_wisp_dep_issue/fk_wisp_dep_issue_target) not
// otherwise used by the caller's own wisp_dependencies rows. id is supplied
// explicitly on every insert (google/uuid, already a repo dependency)
// instead of relying on the column's DEFAULT (UUID()): a wisp_dependencies
// table that reached its id column via ignored/0005's ALTER TABLE ... ADD
// COLUMN id ... DEFAULT (UUID()) (the delegate-path repair route) does not
// reliably honor that default on a later plain INSERT the way the fresh
// CREATE TABLE route does, which is an unrelated engine/migration quirk
// this probe must not depend on to stay focused on ck_wisp_dep_one_target.
func requireCheckRejectsInvalidTargetCounts(t *testing.T, ctx context.Context, conn *sql.Conn, sourceWispID, validIssueTargetID string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, type, created_at, created_by, metadata) VALUES (?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())",
		uuid.NewString(), sourceWispID); err == nil {
		t.Fatal("insert with zero targets succeeded, want ck_wisp_dep_one_target to reject it")
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_at, created_by, metadata) VALUES (?, ?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())",
		uuid.NewString(), sourceWispID, validIssueTargetID, sourceWispID); err == nil {
		t.Fatal("insert with two targets succeeded, want ck_wisp_dep_one_target to reject it")
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())",
		uuid.NewString(), sourceWispID, validIssueTargetID); err != nil {
		t.Fatalf("insert with exactly one target failed, want ck_wisp_dep_one_target to accept it: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"DELETE FROM wisp_dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
		sourceWispID, validIssueTargetID); err != nil {
		t.Fatalf("cleanup valid-target probe row: %v", err)
	}
}
