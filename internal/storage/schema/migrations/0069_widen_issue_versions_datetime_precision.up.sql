-- Migration 0069: widen issue_versions.change_at and .removed_at from
-- DATETIME (precision 0) to DATETIME(6) (microsecond) -- be-hs42e.8 /
-- gastownhall/beads#6132.
--
-- This was drafted as a step 8 appended to 0068 while 0068 was still
-- unmerged. 0068 then shipped on main (gastownhall/beads#6650) with steps 6
-- and 7 only, which froze it (scripts/check-migration-hygiene.sh check C):
-- every clone built since has applied it and recorded its content hash, so
-- the widen needs its own slot.
--
-- 0067 created both columns as plain DATETIME. Dolt's datetime(0) does not
-- truncate sub-second input -- it ROUNDS half-up (pinned for a different
-- column by testAuditImportCommentSubSecond in
-- backend/conformance/audit_labels-comments-events.go, reconfirmed directly
-- against issue_versions by
-- TestMigration0069ChangeAtSurvivesSubSecondPrecisionThroughDoltCLI in
-- internal/storage/schema). For a column whose entire job is placing
-- history in order, precision 0 has two consequences: a sub-second write
-- can read back rounded into the *next* second, and two writes less than a
-- second apart in real time can round onto the identical stored value and
-- become indistinguishable by change_at.
--
-- removed_at widens in lockstep even though no Go code writes it yet (a
-- repo-wide grep for removed_at/RemovedAt turns up only schema: 0067's
-- CREATE TABLE and the CLI-bundle mirror of it in cli_migrations.go). It is
-- change_at's paired lifecycle column on the same row of the same table, so
-- leaving it at precision 0 while change_at widens would be exactly the
-- asymmetry this bead's own title warns against ("before any store
-- accumulates real history").
--
-- No data conversion is needed. Widening precision is lossless -- every
-- whole-second value is exactly representable at precision 6 -- so each
-- MODIFY changes only the column type, whatever the table holds. In
-- practice it holds nothing yet: RecordVersionInTx
-- (internal/storage/issueops/version_history.go), the table's only writer,
-- mints only while versioned history is activated, and no build activates
-- it.
--
-- Guarded on DATETIME_PRECISION the same way 0068 guards on
-- COLUMN_NAME/DATA_TYPE, so a raw-SQL replay of this file onto an
-- already-widened store is a clean no-op (internal/storage/dolt's pr4107
-- replay harness requires this of every migration >= 0046), and a missing
-- table makes the probe yield NULL, which takes the SELECT 1 branch. Needs
-- a CLI-bundle direct-DDL override
-- (cliMigration0069WidenIssueVersionsDatetimePrecision in
-- cli_migrations.go), the same dolthub/dolt#11345 escape hatch 0067 and 0068
-- use, guarded by TestBundleMigrationsWithPreparedALTERAreOverriddenOrJustified.
--
-- No wisps twin needed: issue_versions has no wisps-side counterpart table
-- at all (design section 16.3), so cliSubstituteAssumesWispTables does not
-- apply to this migration either.
--
-- DOLT PLANE, stated here because 0067 created these tables without saying
-- so and a reader has to know before writing to them: issue_versions and
-- store_epoch REPLICATE. They are ordinary synced tables, deliberately NOT
-- registered dolt_ignore'd the way 0064 registers the events journal's
-- clone-local pair. A bead's version history is part of the bead and has to
-- travel with it -- and the single-writer constraint in 0068's header is a
-- statement about what happens when two clones MERGE these tables, which
-- only means anything for a table that replicates at all.
--
-- The obligation that buys: every operation that mints must STAGE what it
-- minted (issue_versions, store_epoch, and issues for the current_revision
-- advance), or the rows sit dirty in the working set -- outside the
-- DOLT_COMMIT of the very mutation they describe, unreplicated, and able to
-- trip DirtyTablesError on the next migration. The staging paths do this
-- from one list, issueops.VersionedHistoryStagedTables, keyed off whether
-- versioned history is active so no call site has to remember it.
SET @issue_versions_change_at_needs_widen = (
    SELECT IF(DATETIME_PRECISION <> 6, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'change_at'
);
SET @sql = IF(@issue_versions_change_at_needs_widen = 1,
    'ALTER TABLE issue_versions MODIFY COLUMN change_at DATETIME(6) NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @issue_versions_removed_at_needs_widen = (
    SELECT IF(DATETIME_PRECISION <> 6, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'removed_at'
);
SET @sql = IF(@issue_versions_removed_at_needs_widen = 1,
    'ALTER TABLE issue_versions MODIFY COLUMN removed_at DATETIME(6)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
