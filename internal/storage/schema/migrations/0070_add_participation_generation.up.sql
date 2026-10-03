-- Migration 0070: participation_generation on issues and wisps (design
-- section 16.3 steps 4-5, be-dt74u amendment, be-h89oq).
--
-- Originally written as steps 4-5 appended to 0068_add_attribution_status
-- (be-h89oq's lineage), while that file was still unmerged. be-v33pa's own
-- attribution_status-only version of 0068 shipped to main first (2026-09-28
-- 15:46Z), freezing that file's content -- scripts/check-migration-hygiene.sh
-- Check C protects any migration already shipped on the base branch, so
-- these steps move here instead of reconciling by editing 0068 (mayor ruling
-- 2026-09-29 on tracker be-waare). Numbered 0070: 0068 and 0069 are already
-- taken on origin/main, the latter by an unrelated migration
-- (widen_issue_versions_datetime_precision) that landed independently while
-- this lineage was in flight.
--
-- participation_generation BIGINT NULL on issues, mirrored inertly on wisps.
-- NULL means legacy-unmigrated; any non-NULL is a positive declaration
-- sourced from store_epoch.epoch. RecordVersionInTx's write fence (design
-- §16.2b, in internal/storage/issueops/version_history.go) reads this column
-- to decide whether an update-shaped mutation against a legacy record mints
-- a version row at all -- a create-shaped mutation stamps a fresh value
-- instead. No backfill: NULL is the correct default for every existing row,
-- on both planes, so a plain ADD COLUMN with no DEFAULT is exactly what's
-- wanted.
--
-- This migration must run before versioned history is turned on. The fence
-- reads this column on every flag-on mint of an issues-plane row, and on a
-- store without it that read fails, so the mutation fails with it rather
-- than skipping. With the flag off (the default) the seam returns before
-- the read, so a store below this migration writes exactly as before.
--
-- Guarded the same way 0067/0068's ADD COLUMNs are (INFORMATION_SCHEMA probe
-- + PREPARE, since Dolt 2.2.3's ADD COLUMN has no MariaDB-only IF NOT
-- EXISTS), making a raw-SQL replay of this file a clean no-op on an
-- already-migrated store. Needs a CLI-bundle direct-DDL override
-- (cliMigration0070AddParticipationGeneration in cli_migrations.go), the same
-- dolthub/dolt#11345 escape hatch 0067/0068 use.
SET @issues_pg_needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'participation_generation'
);
SET @sql = IF(@issues_pg_needs_add = 1,
    'ALTER TABLE issues ADD COLUMN participation_generation BIGINT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- wisps.participation_generation -- shape parity only, never read or written
-- on this plane (design §16.2a). Guarded on the wisps table existing as well
-- as the column, mirroring 0067's wisps.current_revision guard exactly (see
-- that migration's header): wisps is dolt-ignored/clone-local, so a clone
-- that never synced the local wisp tables must no-op rather than abort. The
-- clone-local twin for a workspace that never synced the wisps table at all
-- is ignored/0028_add_wisps_participation_generation.up.sql.
SET @wisps_pg_needs_add = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisps'
          AND COLUMN_NAME = 'participation_generation') = 0,
    1, 0
);
SET @sql = IF(@wisps_pg_needs_add = 1,
    'ALTER TABLE wisps ADD COLUMN participation_generation BIGINT NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
