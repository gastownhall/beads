-- Reverse of migration 0070 (participation_generation on issues and wisps,
-- design section 16.3 steps 4-5, be-dt74u amendment, be-h89oq), undone in
-- reverse order.
--
-- issue_versions is guaranteed empty in any real rollback scenario for the
-- same reason 0068's NOT NULL-no-default is safe (Phase 2 is that table's
-- first writer), but this migration touches issues/wisps, not
-- issue_versions -- participation_generation is dropped unconditionally on
-- both planes if present, guarded on INFORMATION_SCHEMA the same way 0068's
-- down is, so a partially-applied or already-rolled-back workspace rolls
-- back safely. Only migrations/*.up.sql is embedded into the CLI fresh
-- bundle, so the PREPARE hazard (cli_prepared_ddl.go) never reaches this
-- file.
SET @issues_pg_has = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'participation_generation'
);
SET @sql = IF(@issues_pg_has > 0,
    'ALTER TABLE issues DROP COLUMN participation_generation',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @wisps_pg_has = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'participation_generation'
);
SET @sql = IF(@wisps_pg_has > 0,
    'ALTER TABLE wisps DROP COLUMN participation_generation',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
