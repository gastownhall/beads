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
SET @sql = IF(@needs_fk_wisp_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_fk_issue_target = IF(
    @has_wisp_dependencies > 0 AND @has_issues > 0
    AND @has_col_issue_target > 0 AND @has_fk_issue_target = 0,
    1, 0);
SET @sql = IF(@needs_fk_issue_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_ck_one_target = IF(
    @has_wisp_dependencies > 0
    AND @has_col_issue_target > 0 AND @has_col_wisp_target > 0 AND @has_col_external_target > 0
    AND @has_ck_one_target = 0,
    1, 0);
SET @sql = IF(@needs_ck_one_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT ck_wisp_dep_one_target CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
