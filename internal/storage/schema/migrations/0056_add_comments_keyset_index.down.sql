-- Reverse 0056: drop the (issue_id, created_at, id) composite indexes on
-- comments and wisp_comments. Guarded for the same idempotency reason as up so
-- it must not error when the index is already absent. The "index present" guard
-- also covers a missing wisp_comments table (an absent table has no index), so
-- the wisp half is a no-op on a fresh clone too.

SET @has_c = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'comments'
      AND INDEX_NAME = 'idx_comments_issue_created_id'
);
SET @sql = IF(@has_c = 1, 'DROP INDEX idx_comments_issue_created_id ON comments', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_w = (
    SELECT IF(COUNT(*) > 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'wisp_comments'
      AND INDEX_NAME = 'idx_wisp_comments_issue_created_id'
);
SET @sql = IF(@has_w = 1, 'DROP INDEX idx_wisp_comments_issue_created_id ON wisp_comments', 'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
