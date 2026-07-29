SET FOREIGN_KEY_CHECKS = 0;

SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_labels'
      AND CONSTRAINT_NAME = 'fk_wisp_labels_issue'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisp_labels ADD CONSTRAINT fk_wisp_labels_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_events'
      AND CONSTRAINT_NAME = 'fk_wisp_events_issue'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisp_events ADD CONSTRAINT fk_wisp_events_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
      AND CONSTRAINT_NAME = 'fk_wisp_comments_issue'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisp_comments ADD CONSTRAINT fk_wisp_comments_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_child_counters'
      AND CONSTRAINT_NAME = 'fk_wisp_child_counters_parent'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE wisp_child_counters ADD CONSTRAINT fk_wisp_child_counters_parent FOREIGN KEY (parent_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;

