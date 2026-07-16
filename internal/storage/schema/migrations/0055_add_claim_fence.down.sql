-- Documentary rollback for 0055_add_claim_fence. NOTE: down migrations are
-- not runtime-applied (the embed is up-only); the operational rollback for
-- this slice is binary rollback — old binaries tolerate the additive column.

SET @has_col = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'claim_fence'
);
SET @sql = IF(@has_col = 1,
    'ALTER TABLE issues DROP COLUMN claim_fence',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_col = (
    SELECT COUNT(*)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME = 'claim_fence'
);
SET @sql = IF(@has_col = 1,
    'ALTER TABLE wisps DROP COLUMN claim_fence',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
