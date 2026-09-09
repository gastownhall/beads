-- Reverse of 0069: drop issue_versions.removed_restriction.
--
-- Guarded on INFORMATION_SCHEMA the same way 0067/0068's downs are, so a
-- partially-applied or already-rolled-back workspace rolls back safely.
-- Only migrations/*.up.sql is embedded into the CLI fresh bundle, so the
-- PREPARE hazard (cli_prepared_ddl.go) never reaches this file.
SET @issue_versions_rr_has = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'removed_restriction'
);
SET @sql = IF(@issue_versions_rr_has > 0,
    'ALTER TABLE issue_versions DROP COLUMN removed_restriction',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
