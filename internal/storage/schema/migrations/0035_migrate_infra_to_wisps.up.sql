SET @shared_cols = (
    SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY ORDINAL_POSITION SEPARATOR ', ')
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisps'
      AND COLUMN_NAME IN (
          SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
          WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues'
      )
);
SET @sql = IF(@shared_cols IS NULL OR @shared_cols = '',
    'SELECT 1',
    CONCAT('INSERT IGNORE INTO wisps (', @shared_cols, ') SELECT ', @shared_cols,
           ' FROM issues WHERE issue_type IN (''agent'', ''rig'', ''role'', ''message'')')
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0,
    'UPDATE wisps SET ephemeral = 1 WHERE issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_labels') > 0,
    'INSERT IGNORE INTO wisp_labels (issue_id, label) SELECT l.issue_id, l.label FROM labels l JOIN issues i ON i.id = l.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_split_wisp_dependencies = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME IN ('depends_on_issue_id', 'depends_on_wisp_id', 'depends_on_external')
);
SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies') > 0,
    IF(
        @has_split_wisp_dependencies = 3,
        'INSERT IGNORE INTO wisp_dependencies (issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT d.issue_id, CASE WHEN d.depends_on_id LIKE ''external:%'' THEN NULL WHEN EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_id) THEN NULL WHEN EXISTS (SELECT 1 FROM issues target_issue WHERE target_issue.id = d.depends_on_id) THEN d.depends_on_id ELSE NULL END, CASE WHEN d.depends_on_id LIKE ''external:%'' THEN NULL WHEN EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_id) THEN d.depends_on_id ELSE NULL END, CASE WHEN d.depends_on_id LIKE ''external:%'' THEN d.depends_on_id WHEN NOT EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_id) AND NOT EXISTS (SELECT 1 FROM issues target_issue WHERE target_issue.id = d.depends_on_id) THEN d.depends_on_id ELSE NULL END, d.type, d.created_at, d.created_by, d.metadata, d.thread_id FROM dependencies d JOIN issues i ON i.id = d.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
        'INSERT IGNORE INTO wisp_dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id) SELECT d.issue_id, d.depends_on_id, d.type, d.created_at, d.created_by, d.metadata, d.thread_id FROM dependencies d JOIN issues i ON i.id = d.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')'
    ),
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_events') > 0,
    'INSERT IGNORE INTO wisp_events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at) SELECT e.id, e.issue_id, e.event_type, e.actor, e.old_value, e.new_value, e.comment, e.created_at FROM events e JOIN issues i ON i.id = e.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments') > 0,
    'INSERT IGNORE INTO wisp_comments (id, issue_id, author, text, created_at) SELECT c.id, c.issue_id, c.author, c.text, c.created_at FROM comments c JOIN issues i ON i.id = c.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments') > 0,
    'DELETE c FROM comments c JOIN issues i ON i.id = c.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_events') > 0,
    'DELETE e FROM events e JOIN issues i ON i.id = e.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies') > 0,
    'DELETE d FROM dependencies d JOIN issues i ON i.id = d.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_labels') > 0,
    'DELETE l FROM labels l JOIN issues i ON i.id = l.issue_id WHERE i.issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps') > 0,
    'DELETE FROM issues WHERE issue_type IN (''agent'', ''rig'', ''role'', ''message'')',
    'SELECT 1'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
