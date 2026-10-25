-- Reverse of 0068 steps 6 and 7 only (attribution_status, and the
-- durable_state LONGBLOB retype), undone in reverse order. Steps 1-5 of
-- section 16.3 do not exist in this migration (see 0068's up file), so
-- there is nothing else to reverse here.
--
-- issue_versions is guaranteed empty in any real rollback scenario for the
-- same reason the up migration's NOT NULL-no-default is safe (Phase 2 is
-- this table's first writer, shipping in the same build as this column), so
-- dropping the column loses no data that could exist independent of this
-- migration.
--
-- Guarded on INFORMATION_SCHEMA the same way 0067's down is, so a
-- partially-applied or already-rolled-back workspace rolls back safely.
-- Only migrations/*.up.sql is embedded into the CLI fresh bundle, so the
-- PREPARE hazard (cli_prepared_ddl.go) never reaches this file.
--
-- Step 7 reverse: put durable_state back to the JSON type 0067 created it
-- with. Guarded on DATA_TYPE so a store that never took the retype, or was
-- already rolled back, no-ops. issue_versions is empty in any real rollback
-- scenario (see above), so there are no LONGBLOB bytes for the JSON type to
-- reject or renormalize on the way back.
SET @issue_versions_ds_is_longblob = (
    SELECT IF(DATA_TYPE = 'longblob', 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'durable_state'
);
SET @sql = IF(@issue_versions_ds_is_longblob = 1,
    'ALTER TABLE issue_versions MODIFY COLUMN durable_state JSON',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Step 6 reverse: drop attribution_status.
SET @issue_versions_as_has = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issue_versions'
      AND COLUMN_NAME = 'attribution_status'
);
SET @sql = IF(@issue_versions_as_has > 0,
    'ALTER TABLE issue_versions DROP COLUMN attribution_status',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
