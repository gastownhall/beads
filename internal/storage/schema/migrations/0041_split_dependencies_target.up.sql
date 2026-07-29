DELETE FROM dolt_nonlocal_tables;
CALL DOLT_COMMIT('-Am', 'disable nonlocal tables for fk migrations');
SET FOREIGN_KEY_CHECKS = 0;

SET @needs_drop_old_fk = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND CONSTRAINT_NAME = 'fk_dep_depends_on'
);
SET @sql = IF(@needs_drop_old_fk = 1,
    'ALTER TABLE dependencies DROP FOREIGN KEY fk_dep_depends_on',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_migrate = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND COLUMN_NAME = 'depends_on_wisp_id'
);

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD COLUMN depends_on_issue_id VARCHAR(255) NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD COLUMN depends_on_wisp_id VARCHAR(255) NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD COLUMN depends_on_external VARCHAR(255) NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'UPDATE dependencies SET depends_on_external = depends_on_id WHERE depends_on_id LIKE ''external:%''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @wisps_exists = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
);
SET @sql = IF(@needs_migrate = 1 AND @wisps_exists = 1,
    'UPDATE dependencies d JOIN wisps w ON w.id = d.depends_on_id SET d.depends_on_wisp_id = d.depends_on_id WHERE d.depends_on_external IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'UPDATE dependencies d JOIN issues i ON i.id = d.depends_on_id SET d.depends_on_issue_id = d.depends_on_id WHERE d.depends_on_external IS NULL AND d.depends_on_wisp_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'UPDATE dependencies SET depends_on_external = depends_on_id WHERE depends_on_external IS NULL AND depends_on_wisp_id IS NULL AND depends_on_issue_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies DROP INDEX idx_dependencies_depends_on',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies DROP INDEX idx_dependencies_depends_on_type',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies DROP COLUMN depends_on_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- NOTE: the FK on depends_on_issue_id must be added BEFORE the generated
-- depends_on_id column exists. Dolt 2.0.6 rejects adding a foreign key on the
-- base column of a stored generated column (errno 1105). The final schema is
-- identical to adding it last.
SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD CONSTRAINT fk_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD COLUMN depends_on_id VARCHAR(255) AS (COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) STORED',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD PRIMARY KEY (issue_id, depends_on_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD INDEX idx_dep_wisp_target (depends_on_wisp_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD INDEX idx_dep_issue_target (depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD INDEX idx_dep_external_target (depends_on_external)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD INDEX idx_dep_type_target (type, depends_on_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_migrate = 1,
    'ALTER TABLE dependencies ADD CONSTRAINT ck_dep_one_target CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
