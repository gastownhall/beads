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
    CONCAT('INSERT IGNORE INTO issues (', @shared_cols, ') SELECT ', @shared_cols,
           ' FROM wisps WHERE issue_type IN (''agent'', ''rig'', ''role'', ''message'')')
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

UPDATE issues SET ephemeral = 0
WHERE issue_type IN ('agent', 'rig', 'role', 'message');

INSERT IGNORE INTO labels (issue_id, label)
SELECT issue_id, label FROM wisp_labels wl
WHERE EXISTS (SELECT 1 FROM issues i WHERE i.id = wl.issue_id
              AND i.issue_type IN ('agent', 'rig', 'role', 'message'));

SET @has_split_wisp_dependencies = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME IN ('depends_on_issue_id', 'depends_on_wisp_id', 'depends_on_external')
);
SET @sql = IF(
    @has_split_wisp_dependencies = 3,
    'INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id) SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external), type, created_at, created_by, metadata, thread_id FROM wisp_dependencies wd WHERE EXISTS (SELECT 1 FROM issues i WHERE i.id = wd.issue_id AND i.issue_type IN (''agent'', ''rig'', ''role'', ''message''))',
    'INSERT IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id) SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id FROM wisp_dependencies wd WHERE EXISTS (SELECT 1 FROM issues i WHERE i.id = wd.issue_id AND i.issue_type IN (''agent'', ''rig'', ''role'', ''message''))'
);
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

INSERT IGNORE INTO events (id, issue_id, event_type, actor, old_value, new_value, comment, created_at)
SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at FROM wisp_events we
WHERE EXISTS (SELECT 1 FROM issues i WHERE i.id = we.issue_id
              AND i.issue_type IN ('agent', 'rig', 'role', 'message'));

INSERT IGNORE INTO comments (id, issue_id, author, text, created_at)
SELECT id, issue_id, author, text, created_at FROM wisp_comments wc
WHERE EXISTS (SELECT 1 FROM issues i WHERE i.id = wc.issue_id
              AND i.issue_type IN ('agent', 'rig', 'role', 'message'));

DELETE FROM wisp_comments WHERE issue_id IN (SELECT id FROM issues WHERE issue_type IN ('agent', 'rig', 'role', 'message'));
DELETE FROM wisp_events WHERE issue_id IN (SELECT id FROM issues WHERE issue_type IN ('agent', 'rig', 'role', 'message'));
DELETE FROM wisp_dependencies WHERE issue_id IN (SELECT id FROM issues WHERE issue_type IN ('agent', 'rig', 'role', 'message'));
DELETE FROM wisp_labels WHERE issue_id IN (SELECT id FROM issues WHERE issue_type IN ('agent', 'rig', 'role', 'message'));
DELETE FROM wisps WHERE issue_type IN ('agent', 'rig', 'role', 'message');
