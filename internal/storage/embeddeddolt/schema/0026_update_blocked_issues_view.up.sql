CREATE OR REPLACE VIEW blocked_issues AS
WITH done_frozen AS (
    SELECT name FROM custom_statuses WHERE category IN ('done', 'frozen')
)
SELECT
    i.*,
    (SELECT COUNT(*)
     FROM dependencies d
     WHERE d.issue_id = i.id
       AND d.type = 'blocks'
       AND EXISTS (
         SELECT 1 FROM issues blocker
         WHERE blocker.id = d.depends_on_id
           AND blocker.status NOT IN ('closed', 'pinned')
           AND blocker.status NOT IN (SELECT name FROM done_frozen)
       )
    ) as blocked_by_count
FROM issues i
WHERE i.status NOT IN ('closed', 'pinned')
  AND i.status NOT IN (SELECT name FROM done_frozen)
  AND EXISTS (
    SELECT 1 FROM dependencies d
    WHERE d.issue_id = i.id
      AND d.type = 'blocks'
      AND EXISTS (
        SELECT 1 FROM issues blocker
        WHERE blocker.id = d.depends_on_id
          AND blocker.status NOT IN ('closed', 'pinned')
          AND blocker.status NOT IN (SELECT name FROM done_frozen)
      )
  );
