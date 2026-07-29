SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'child_counters'
      AND CONSTRAINT_NAME = 'fk_counter_parent'
);
SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE child_counters DROP FOREIGN KEY fk_counter_parent',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
