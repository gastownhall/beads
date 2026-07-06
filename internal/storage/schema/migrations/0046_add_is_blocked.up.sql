SET @needs_add = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND COLUMN_NAME = 'is_blocked'
);
SET @sql = IF(@needs_add = 1,
    'ALTER TABLE issues ADD COLUMN is_blocked TINYINT(1) NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @needs_index = (
    SELECT IF(COUNT(*) = 0, 1, 0)
    FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'issues'
      AND INDEX_NAME = 'idx_issues_is_blocked'
);
SET @sql = IF(@needs_index = 1,
    'CREATE INDEX idx_issues_is_blocked ON issues(is_blocked, status)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

UPDATE issues SET is_blocked = 0;

WITH RECURSIVE
  directly_blocked(id) AS (
    SELECT DISTINCT i.id
    FROM issues i
    JOIN dependencies d ON d.issue_id = i.id
    JOIN issues t ON t.id = d.depends_on_issue_id
    WHERE i.status NOT IN ('closed', 'pinned')
      AND d.type IN ('blocks', 'conditional-blocks', 'waits-for')
      AND t.status NOT IN ('closed', 'pinned')
  ),
  reachable(id) AS (
    SELECT id FROM directly_blocked
    UNION
    SELECT d.issue_id
    FROM reachable r
    JOIN dependencies d
      ON d.depends_on_issue_id = r.id
     AND d.type = 'parent-child'
  )
UPDATE issues
SET is_blocked = 1
WHERE id IN (SELECT id FROM reachable)
  AND status NOT IN ('closed', 'pinned');
