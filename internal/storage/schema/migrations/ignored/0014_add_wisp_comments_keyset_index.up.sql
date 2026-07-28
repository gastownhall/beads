-- Keyset comment-page index on wisp_comments (issue_id, created_at, id) — the
-- wisp twin of main migration 0056's index on durable comments. The ignored
-- chain runs per clone with its own cursor, so BOTH fresh clones (which
-- materialize wisp_comments via ignored/0001) and already-upgraded workspaces
-- pick this up; main 0056 covers only workspaces whose wisp tables existed
-- when it ran. Guarded like ignored/0006: create only when the table exists
-- and the index is absent.
SET @needs_index = IF(
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments') > 0
    AND
    (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE()
          AND TABLE_NAME = 'wisp_comments'
          AND INDEX_NAME = 'idx_wisp_comments_issue_created_id') = 0,
    1, 0
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_wisp_comments_issue_created_id ON wisp_comments(issue_id, created_at, id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
