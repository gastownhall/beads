-- Roll back the storage_class marker column (bd-8rifr). Guarded so an
-- issues-only or partially-applied workspace rolls back as safely as it
-- migrated up (0054 precedent).

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'storage_class'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE issues DROP COLUMN storage_class',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'storage_class'
);
SET @sql = IF(@has_col > 0,
    'ALTER TABLE wisps DROP COLUMN storage_class',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
