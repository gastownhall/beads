SET @needs_fix = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'fk_dep_issue'
      AND UPDATE_RULE != 'CASCADE'
);
SET @sql = IF(@needs_fix = 1, 'ALTER TABLE dependencies DROP FOREIGN KEY fk_dep_issue', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
SET @sql = IF(@needs_fix = 1,
    'ALTER TABLE dependencies ADD CONSTRAINT fk_dep_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_fix = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'fk_labels_issue'
      AND UPDATE_RULE != 'CASCADE'
);
SET @sql = IF(@needs_fix = 1, 'ALTER TABLE labels DROP FOREIGN KEY fk_labels_issue', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
SET @sql = IF(@needs_fix = 1,
    'ALTER TABLE labels ADD CONSTRAINT fk_labels_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_fix = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'fk_comments_issue'
      AND UPDATE_RULE != 'CASCADE'
);
SET @sql = IF(@needs_fix = 1, 'ALTER TABLE comments DROP FOREIGN KEY fk_comments_issue', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
SET @sql = IF(@needs_fix = 1,
    'ALTER TABLE comments ADD CONSTRAINT fk_comments_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_fix = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'fk_events_issue'
      AND UPDATE_RULE != 'CASCADE'
);
SET @sql = IF(@needs_fix = 1, 'ALTER TABLE events DROP FOREIGN KEY fk_events_issue', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
SET @sql = IF(@needs_fix = 1,
    'ALTER TABLE events ADD CONSTRAINT fk_events_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_fix = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'fk_snapshots_issue'
      AND UPDATE_RULE != 'CASCADE'
);
SET @sql = IF(@needs_fix = 1, 'ALTER TABLE issue_snapshots DROP FOREIGN KEY fk_snapshots_issue', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
SET @sql = IF(@needs_fix = 1,
    'ALTER TABLE issue_snapshots ADD CONSTRAINT fk_snapshots_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_fix = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'fk_comp_snap_issue'
      AND UPDATE_RULE != 'CASCADE'
);
SET @sql = IF(@needs_fix = 1, 'ALTER TABLE compaction_snapshots DROP FOREIGN KEY fk_comp_snap_issue', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
SET @sql = IF(@needs_fix = 1,
    'ALTER TABLE compaction_snapshots ADD CONSTRAINT fk_comp_snap_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
