SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies') > 0,
    'CREATE INDEX IF NOT EXISTS idx_wisp_dep_type ON wisp_dependencies (type)',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_split_targets = (
    SELECT COUNT(*) = 3
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME IN ('depends_on_issue_id', 'depends_on_wisp_id', 'depends_on_external')
);
SET @sql = IF(@has_split_targets,
    'CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_issue ON wisp_dependencies (type, depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_split_targets,
    'CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_wisp ON wisp_dependencies (type, depends_on_wisp_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_split_targets,
    'CREATE INDEX IF NOT EXISTS idx_wisp_dep_type_external ON wisp_dependencies (type, depends_on_external)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
