-- Documentary rollback for 0056_add_holder_token. Down migrations are not
-- runtime-applied; operational rollback is binary rollback (old binaries
-- tolerate the additive column).

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME = 'holder_token'
);
SET @sql = IF(@has_col = 1, 'ALTER TABLE issues DROP COLUMN holder_token', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'holder_token'
);
SET @sql = IF(@has_col = 1, 'ALTER TABLE wisps DROP COLUMN holder_token', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
