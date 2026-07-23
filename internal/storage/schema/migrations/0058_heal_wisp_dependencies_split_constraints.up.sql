-- Migration 0058: heal wisp_dependencies constraints missed by the 0047
-- delegate repair path (#4555 residual, mybd-mq9b, discovered while closing
-- gastownhall/beads#4878's review coverage gaps).
--
-- migration_repairs.go's ensureWispDependenciesSplitTargets (dispatched from
-- 0047's preMigrationRepair when wisps AND wisp_dependencies both already
-- exist -- the "delegate" branch, as opposed to recreating either table from
-- scratch) adds wisp_dependencies' three split-target columns
-- (depends_on_issue_id/depends_on_wisp_id/depends_on_external) and backfills
-- them from the legacy depends_on_id column, but does not add the
-- fk_wisp_dep_wisp_target/fk_wisp_dep_issue_target foreign keys or the
-- ck_wisp_dep_one_target CHECK constraint those columns need. Those three
-- constraints are added by two OTHER migrations, but both only fire under
-- conditions the delegate path itself defeats:
--   - ignored/0003_split_wisp_dependencies_target's own inline split (which
--     adds all three) is gated on depends_on_wisp_id being ABSENT; the
--     delegate already added it, so 0003 takes its no-op branch.
--   - ignored/0005_drop_wisp_dependencies_generated_column only RESTORES a
--     foreign key if it detects that exact constraint present BEFORE its own
--     drop of depends_on_id; a genuinely legacy (pre-split) table never had
--     fk_wisp_dep_wisp_target/fk_wisp_dep_issue_target to begin with (their
--     target columns did not exist yet), so there is nothing for 0005 to
--     detect and restore.
--
-- The net effect: any database that takes the delegate path converges
-- structurally (columns, surrogate id, primary key, unique keys) but
-- silently ends up permanently missing two foreign keys and the one-target
-- CHECK constraint. Migration 0047, ignored/0003, and ignored/0005 are all
-- shipped and frozen (scripts/check-migration-hygiene.sh Check C forbids
-- editing a migration file that already exists on main), and a database
-- already affected has its schema_migrations cursor sitting at latest --
-- fixing only the forward-path files could never reach it, since none of
-- them are pending on that database again. A new, idempotent migration is
-- the only way to heal databases already in this state, matching the
-- 0053/0057 repair-migration precedent from the migration-chain-hardening
-- branch (#4878).
--
-- Each constraint is guarded independently by its own INFORMATION_SCHEMA
-- checks (constraint absent, and its required table/columns present) so
-- this safely no-ops on every population: a fresh-recreate-path database
-- (constraints already present from creation) or a database that already
-- carries them (idempotent replay) sees no-ops on all three; a
-- delegate-path database sees exactly the missing ones added; and a
-- database whose wisp_dependencies table (or a required column) is for any
-- other reason absent by the time this migration runs is left untouched
-- rather than assumed into a shape it may not have.
--
-- Adding a constraint over data that accumulated during the window it was
-- missing is exactly the failure class ignored/0011 (#4534) documents:
-- foreign keys re-added under FOREIGN_KEY_CHECKS = 0 accept whatever
-- orphaned rows are already there, and "Dolt then fails constraint
-- validation on subsequent writes, so one legacy orphan bricks every bd
-- create on an otherwise healthy database." The delegate path's
-- unconstrained window can leave two more classes of row that would abort a
-- later PREPARE'd ADD CONSTRAINT the same way, so this migration cleans up
-- both, immediately before the constraint that would otherwise reject them,
-- mirroring ignored/0011's guarded LEFT JOIN idiom:
--
--   1. FK orphans (before the two ADD CONSTRAINT ... FOREIGN KEY below): a
--      depends_on_wisp_id/depends_on_issue_id pointing at a row that no
--      longer exists (deleted while no CASCADE was in force). DELETE the
--      whole row -- exactly what ON DELETE CASCADE would have done had the
--      FK been in force when the target went away -- rather than null just
--      the dangling column, matching how a real CASCADE never leaves a
--      partial row behind.
--   2. CHECK-invalid rows (before ADD CONSTRAINT ck_wisp_dep_one_target): a
--      row with zero targets set is semantically meaningless (there is
--      nothing here to be blocked on) and is deleted outright. A row with
--      more than one target set is normalized to exactly one by applying
--      the SAME precedence the delegate's own backfill
--      (wispDependenciesSplitTargetBackfillSQL) already trusts when a
--      single legacy id resolves ambiguously -- confirmed by that function's
--      statement order (the external-prefix UPDATE runs first and is
--      excluded from the wisp UPDATE's guard; the wisp UPDATE runs next and
--      is excluded from the issue UPDATE's guard) and by its sibling test
--      TestWispDependenciesSplitTargetBackfillPrefersWispOverIssueThroughDoltCLI,
--      which seeds one id that resolves as both a wisp and an issue and
--      asserts the wisp reading wins. That gives external > wisp > issue;
--      normalizing here nulls the lower-precedence column(s) rather than
--      picking one arbitrarily or deleting the row (unlike a zero-target
--      row, a multi-target row already names a real, resolvable target --
--      just more than one -- so keeping the highest-precedence one preserves
--      real edge data instead of discarding it).
--
-- Both cleanup steps are scoped to exactly the rows the corresponding
-- constraint would otherwise reject, guarded by the same @needs_fk_*/
-- @needs_ck_one_target flags the ADD CONSTRAINT statements use: a database
-- that already has (or never needed) a constraint skips its cleanup too, so
-- a converged database never pays this scan, and a second pass after the
-- constraints are added is a no-op (no violating rows are left to find).

SET FOREIGN_KEY_CHECKS = 0;

SET @has_wisp_dependencies = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
);
SET @has_wisps = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
);
SET @has_issues = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
);
SET @has_col_issue_target = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME = 'depends_on_issue_id'
);
SET @has_col_wisp_target = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME = 'depends_on_wisp_id'
);
SET @has_col_external_target = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME = 'depends_on_external'
);
SET @has_fk_wisp_target = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND CONSTRAINT_NAME = 'fk_wisp_dep_wisp_target'
);
SET @has_fk_issue_target = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND CONSTRAINT_NAME = 'fk_wisp_dep_issue_target'
);
SET @has_ck_one_target = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND CONSTRAINT_NAME = 'ck_wisp_dep_one_target'
);

SET @needs_fk_wisp_target = IF(
    @has_wisp_dependencies > 0 AND @has_wisps > 0
    AND @has_col_wisp_target > 0 AND @has_fk_wisp_target = 0,
    1, 0);
SET @needs_fk_issue_target = IF(
    @has_wisp_dependencies > 0 AND @has_issues > 0
    AND @has_col_issue_target > 0 AND @has_fk_issue_target = 0,
    1, 0);
SET @needs_ck_one_target = IF(
    @has_wisp_dependencies > 0
    AND @has_col_issue_target > 0 AND @has_col_wisp_target > 0 AND @has_col_external_target > 0
    AND @has_ck_one_target = 0,
    1, 0);

-- --- Cleanup 1: FK orphans (#4534-class, see ignored/0011) ------------------

SET @sql = IF(@needs_fk_wisp_target = 1,
    'DELETE wd FROM wisp_dependencies wd LEFT JOIN wisps w ON w.id = wd.depends_on_wisp_id WHERE wd.depends_on_wisp_id IS NOT NULL AND w.id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_fk_issue_target = 1,
    'DELETE wd FROM wisp_dependencies wd LEFT JOIN issues i ON i.id = wd.depends_on_issue_id WHERE wd.depends_on_issue_id IS NOT NULL AND i.id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- --- Cleanup 2: CHECK-invalid rows (zero-target delete, multi-target -------
-- --- normalize to external > wisp > issue precedence) ----------------------

SET @sql = IF(@needs_ck_one_target = 1,
    'DELETE FROM wisp_dependencies WHERE depends_on_issue_id IS NULL AND depends_on_wisp_id IS NULL AND depends_on_external IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_ck_one_target = 1,
    'UPDATE wisp_dependencies SET depends_on_wisp_id = NULL, depends_on_issue_id = NULL WHERE depends_on_external IS NOT NULL AND (depends_on_wisp_id IS NOT NULL OR depends_on_issue_id IS NOT NULL)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_ck_one_target = 1,
    'UPDATE wisp_dependencies SET depends_on_issue_id = NULL WHERE depends_on_external IS NULL AND depends_on_wisp_id IS NOT NULL AND depends_on_issue_id IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- --- Add the constraints -----------------------------------------------------

SET @sql = IF(@needs_fk_wisp_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_fk_issue_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_ck_one_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT ck_wisp_dep_one_target CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
