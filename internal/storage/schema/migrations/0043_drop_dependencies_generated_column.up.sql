-- NOTE (gastownhall/beads#4259): this migration introduces the surrogate
-- `id CHAR(36) ... DEFAULT (UUID())` primary key below. UUID() is per-clone-random
-- and makes the dependencies table merge-unsafe across Dolt clones. The fix lives
-- in migration 0050 + the rekeyDependencyIDs backfill, which derive id
-- deterministically from (issue_id, target) and drop this random default. As a
-- belt-and-suspenders for clones that have NOT yet applied this migration, the
-- random default is also dropped at the end of this file, so a freshly migrated
-- clone reaches the no-default schema directly here rather than only at 0050.
SET FOREIGN_KEY_CHECKS = 0;

SET @needs_drop = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND COLUMN_NAME = 'depends_on_id'
);

SET @has_idx_type_target = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND INDEX_NAME = 'idx_dep_type_target'
);

SET @sql = IF(@needs_drop = 1 AND @has_idx_type_target = 1,
    'ALTER TABLE dependencies DROP INDEX idx_dep_type_target',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_fk_issue_target = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND CONSTRAINT_NAME = 'fk_dep_issue_target'
);
SET @sql = IF(@needs_drop = 1 AND @has_fk_issue_target = 1,
    'ALTER TABLE dependencies DROP FOREIGN KEY fk_dep_issue_target',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_fk_issue = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND CONSTRAINT_NAME = 'fk_dep_issue'
);
SET @sql = IF(@needs_drop = 1 AND @has_fk_issue = 1,
    'ALTER TABLE dependencies DROP FOREIGN KEY fk_dep_issue',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies DROP COLUMN depends_on_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies ADD COLUMN id CHAR(36) NOT NULL DEFAULT (UUID()) PRIMARY KEY FIRST',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies ADD UNIQUE KEY uk_dep_issue_target (issue_id, depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies ADD UNIQUE KEY uk_dep_wisp_target (issue_id, depends_on_wisp_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies ADD UNIQUE KEY uk_dep_external_target (issue_id, depends_on_external)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies ADD INDEX idx_dep_type_issue (type, depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies ADD INDEX idx_dep_type_wisp (type, depends_on_wisp_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1,
    'ALTER TABLE dependencies ADD INDEX idx_dep_type_external (type, depends_on_external)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1 AND @has_fk_issue = 1,
    'ALTER TABLE dependencies ADD CONSTRAINT fk_dep_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@needs_drop = 1 AND @has_fk_issue_target = 1,
    'ALTER TABLE dependencies ADD CONSTRAINT fk_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- #4259: drop the per-clone-random DEFAULT (UUID()) so the application's
-- deterministic id (set explicitly at every insert site) is the only source of
-- the primary key. Guarded on COLUMN_DEFAULT so it is idempotent and a no-op when
-- the default is already gone (e.g. after migration 0050 has run). The existing
-- rows' transient random ids are rewritten by the rekeyDependencyIDs backfill.
SET @id_has_default = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'dependencies'
      AND COLUMN_NAME = 'id'
      AND COLUMN_DEFAULT IS NOT NULL
);
SET @sql = IF(@id_has_default = 1,
    'ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
