-- Reverse of 0067. Every surface this reverses is still unread and unwritten
-- in Phase 1, so this is a plain schema rollback, not a data-loss concern.
--
-- Guarded on INFORMATION_SCHEMA the same way the up migration is, so an
-- issues-only or partially-applied workspace rolls back as safely as it
-- migrated up (0060's down is the precedent). Only migrations/*.up.sql is
-- embedded into the CLI fresh bundle, so the PREPARE hazard
-- (cli_prepared_ddl.go) never reaches this file through the bundle. It does
-- reach a manual rollback: run this through a server connection or dolt
-- >= 2.3, because a pre-2.3 `dolt sql -f` silently no-ops the guarded DROP
-- COLUMNs (dolthub/dolt#11345) while still dropping the tables, leaving both
-- current_revision columns behind. The residue is inert in Phase 1 -- nothing
-- reads the columns, both planes keep them so parity holds, and re-running
-- the up migration no-ops -- but the rollback is partial while reporting
-- success. 0060's down carries the same hazard.
SET @issues_cr_has = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'current_revision'
);
SET @sql = IF(@issues_cr_has > 0,
    'ALTER TABLE issues DROP COLUMN current_revision',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @wisps_cr_has = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'current_revision'
);
SET @sql = IF(@wisps_cr_has > 0,
    'ALTER TABLE wisps DROP COLUMN current_revision',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS store_epoch;

DROP TABLE IF EXISTS issue_versions;
