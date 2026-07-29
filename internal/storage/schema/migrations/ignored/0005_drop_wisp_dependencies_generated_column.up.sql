SET FOREIGN_KEY_CHECKS = 0;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME = 'depends_on_id'
);

SET @has_idx_type_target = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND INDEX_NAME = 'idx_wisp_dep_type_target'
);

SET @sql = IF(@needs_drop = 1 AND @has_idx_type_target = 1,
    'ALTER TABLE wisp_dependencies DROP INDEX idx_wisp_dep_type_target',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_fk_issue = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND CONSTRAINT_NAME = 'fk_wisp_dep_issue'
);
SET @sql = IF(@needs_drop = 1 AND @has_fk_issue = 1,
    'ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_issue',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_fk_wisp_target = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND CONSTRAINT_NAME = 'fk_wisp_dep_wisp_target'
);
SET @sql = IF(@needs_drop = 1 AND @has_fk_wisp_target = 1,
    'ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_wisp_target',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_fk_issue_target = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND CONSTRAINT_NAME = 'fk_wisp_dep_issue_target'
);
SET @sql = IF(@needs_drop = 1 AND @has_fk_issue_target = 1,
    'ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_issue_target',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies DROP COLUMN depends_on_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies ADD COLUMN id CHAR(36) NOT NULL DEFAULT (UUID()) PRIMARY KEY FIRST',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies ADD UNIQUE KEY uk_wisp_dep_issue_target (issue_id, depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies ADD UNIQUE KEY uk_wisp_dep_wisp_target (issue_id, depends_on_wisp_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies ADD UNIQUE KEY uk_wisp_dep_external_target (issue_id, depends_on_external)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies ADD INDEX idx_wisp_dep_type_issue (type, depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies ADD INDEX idx_wisp_dep_type_wisp (type, depends_on_wisp_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE wisp_dependencies ADD INDEX idx_wisp_dep_type_external (type, depends_on_external)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1 AND @has_fk_issue = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1 AND @has_fk_wisp_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1 AND @has_fk_issue_target = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
