-- Roll back the closed_by column (0075). Guarded so an issues-only or
-- partially-applied workspace rolls back as safely as it migrated up (0060
-- precedent). Dropping the column drops no data this series owns elsewhere:
-- the events journal keeps its own closed records.
--
-- Manual rollback: run this over a server connection or with dolt >= 2.3. A
-- pre-2.3 `dolt sql -f` silently no-ops the prepared ALTERs below while
-- reporting success (dolthub/dolt#11345), the same reason 0073's down warns.

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'closed_by'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE issues DROP COLUMN closed_by',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'closed_by'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE wisps DROP COLUMN closed_by',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
