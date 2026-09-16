-- Roll back the recurrence columns. Guarded so an issues-only or
-- partially-applied workspace rolls back as safely as it migrated up
-- (0054/0060 precedent).
--
-- 0068 is schema-only and wrote no rows, so there is nothing else to undo:
-- dropping the columns returns every bead to non-recurring, which is what it
-- was before the migration ran.

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME = 'repeat_pattern'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE issues DROP COLUMN repeat_pattern', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME = 'repeat_start'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE issues DROP COLUMN repeat_start', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME = 'repeat_end'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE issues DROP COLUMN repeat_end', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'repeat_pattern'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE wisps DROP COLUMN repeat_pattern', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'repeat_start'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE wisps DROP COLUMN repeat_start', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'repeat_end'
);
SET @sql = IF(@has_col > 0, 'ALTER TABLE wisps DROP COLUMN repeat_end', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
