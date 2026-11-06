-- Roll back the arrival_seq column (0073). Guarded so an issues-only or
-- partially-applied workspace rolls back as safely as it migrated up (0060
-- precedent). Dropping wisps.arrival_seq also drops uk_wisps_arrival_seq, the
-- clone-local unique index over it. ignored/0029 stays recorded and does not
-- run again, so it is 0073's up, applied again, that rebuilds the index.
--
-- Manual rollback: run this over a server connection or with dolt >= 2.3. A
-- pre-2.3 `dolt sql -f` silently no-ops the prepared ALTERs below while
-- reporting success (dolthub/dolt#11345).

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'arrival_seq'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE issues DROP COLUMN arrival_seq',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'arrival_seq'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE wisps DROP COLUMN arrival_seq',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
