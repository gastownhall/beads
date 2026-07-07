SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisp_dependencies'
          AND INDEX_NAME = 'idx_wisp_dep_type') > 0,
    'DROP INDEX idx_wisp_dep_type ON wisp_dependencies',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
