DROP TABLE IF EXISTS __temp__wisp_child_counters;
CREATE TABLE __temp__wisp_child_counters (
    parent_id VARCHAR(255) PRIMARY KEY,
    last_child INT NOT NULL DEFAULT 0
);

SET @exists = (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_child_counters');
SET @sql = IF(@exists = 0, 'RENAME TABLE __temp__wisp_child_counters TO wisp_child_counters', 'DROP TABLE __temp__wisp_child_counters');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @both_exist = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_child_counters') > 0,
    1, 0);

SET @sql = IF(@both_exist = 1,
    'INSERT IGNORE INTO wisp_child_counters (parent_id, last_child) SELECT cc.parent_id, cc.last_child FROM child_counters cc INNER JOIN wisps w ON w.id = cc.parent_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@both_exist = 1,
    'DELETE FROM child_counters WHERE parent_id IN (SELECT id FROM wisps)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 0;

SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'child_counters'
      AND CONSTRAINT_NAME = 'fk_counter_parent'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE child_counters ADD CONSTRAINT fk_counter_parent FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;
