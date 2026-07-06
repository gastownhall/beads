SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'schema_migrations'
      AND COLUMN_NAME = 'applied_at'
);
SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE schema_migrations DROP COLUMN applied_at',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
