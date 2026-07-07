SET FOREIGN_KEY_CHECKS = 0;

-- Some released 1.0.x databases have main schema migrations through 0046 but
-- have not yet run the ignored/local-table migration that splits
-- wisp_dependencies.depends_on_id into issue/wisp/external target columns.
-- This main migration reads those split columns below, so normalize the
-- ignored table first. The ignored 0003/0005 migrations are idempotent and
-- will no-op/drop the generated compatibility column afterwards.
SET @wisp_dependencies_needs_split = (
    SELECT IF(
        (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
         WHERE TABLE_SCHEMA = DATABASE()
           AND TABLE_NAME = 'wisp_dependencies'
           AND COLUMN_NAME = 'depends_on_id') > 0
        AND
        (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
         WHERE TABLE_SCHEMA = DATABASE()
           AND TABLE_NAME = 'wisp_dependencies'
           AND COLUMN_NAME = 'depends_on_wisp_id') = 0,
        1, 0)
);

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD COLUMN depends_on_issue_id VARCHAR(255) NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD COLUMN depends_on_wisp_id VARCHAR(255) NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD COLUMN depends_on_external VARCHAR(255) NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'UPDATE wisp_dependencies SET depends_on_external = depends_on_id WHERE depends_on_id LIKE ''external:%''',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'UPDATE wisp_dependencies wd JOIN wisps w ON w.id = wd.depends_on_id SET wd.depends_on_wisp_id = wd.depends_on_id WHERE wd.depends_on_external IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'UPDATE wisp_dependencies wd JOIN issues i ON i.id = wd.depends_on_id SET wd.depends_on_issue_id = wd.depends_on_id WHERE wd.depends_on_external IS NULL AND wd.depends_on_wisp_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'UPDATE wisp_dependencies SET depends_on_external = depends_on_id WHERE depends_on_external IS NULL AND depends_on_wisp_id IS NULL AND depends_on_issue_id IS NULL',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies DROP INDEX idx_wisp_dep_depends',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies DROP INDEX idx_wisp_dep_type_depends',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies DROP PRIMARY KEY',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies DROP COLUMN depends_on_id',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD INDEX idx_wisp_dep_wisp_target (depends_on_wisp_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD INDEX idx_wisp_dep_issue_target (depends_on_issue_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD INDEX idx_wisp_dep_external_target (depends_on_external)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT ck_wisp_dep_one_target CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD COLUMN depends_on_id VARCHAR(255) AS (COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) STORED',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD PRIMARY KEY (issue_id, depends_on_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD INDEX idx_wisp_dep_type_target (type, depends_on_id)',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@wisp_dependencies_needs_split = 1,
    'ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET FOREIGN_KEY_CHECKS = 1;

UPDATE issues SET is_blocked = 0;

WITH RECURSIVE
  directly_blocked(kind, id) AS (
    SELECT DISTINCT 'issue', i.id
    FROM issues i
    WHERE i.status NOT IN ('closed', 'pinned')
      AND (
        EXISTS (
          SELECT 1
          FROM dependencies d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.issue_id = i.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM dependencies d
          JOIN wisps t ON t.id = d.depends_on_wisp_id
          WHERE d.issue_id = i.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM dependencies d
          WHERE d.issue_id = i.id
            AND d.type = 'waits-for'
            AND (
              EXISTS (
                SELECT 1
                FROM dependencies cd
                JOIN issues child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
              OR EXISTS (
                SELECT 1
                FROM wisp_dependencies cd
                JOIN wisps child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
            )
            AND NOT (
              JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')) = 'any-children'
              AND (
                EXISTS (
                  SELECT 1
                  FROM dependencies cd
                  JOIN issues child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
                OR EXISTS (
                  SELECT 1
                  FROM wisp_dependencies cd
                  JOIN wisps child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
              )
            )
        )
      )
    UNION
    SELECT DISTINCT 'wisp', w.id
    FROM wisps w
    WHERE w.status NOT IN ('closed', 'pinned')
      AND (
        EXISTS (
          SELECT 1
          FROM wisp_dependencies d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM wisp_dependencies d
          JOIN wisps t ON t.id = d.depends_on_wisp_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM wisp_dependencies d
          WHERE d.issue_id = w.id
            AND d.type = 'waits-for'
            AND (
              EXISTS (
                SELECT 1
                FROM dependencies cd
                JOIN issues child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
              OR EXISTS (
                SELECT 1
                FROM wisp_dependencies cd
                JOIN wisps child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
            )
            AND NOT (
              JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')) = 'any-children'
              AND (
                EXISTS (
                  SELECT 1
                  FROM dependencies cd
                  JOIN issues child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
                OR EXISTS (
                  SELECT 1
                  FROM wisp_dependencies cd
                  JOIN wisps child ON child.id = cd.issue_id
                  WHERE cd.type = 'parent-child'
                    AND (
                      (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                      OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                    )
                    AND child.status = 'closed'
                )
              )
            )
        )
      )
  ),
  reachable(kind, id) AS (
    SELECT kind, id FROM directly_blocked
    UNION
    SELECT 'issue', d.issue_id
    FROM reachable r
    JOIN dependencies d
      ON d.type = 'parent-child'
     AND (
       (r.kind = 'issue' AND d.depends_on_issue_id = r.id)
       OR (r.kind = 'wisp' AND d.depends_on_wisp_id = r.id)
     )
    JOIN issues child ON child.id = d.issue_id
    WHERE child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN wisp_dependencies d
      ON d.type = 'parent-child'
     AND (
       (r.kind = 'issue' AND d.depends_on_issue_id = r.id)
       OR (r.kind = 'wisp' AND d.depends_on_wisp_id = r.id)
     )
    JOIN wisps child ON child.id = d.issue_id
    WHERE child.status NOT IN ('closed', 'pinned')
  )
UPDATE issues
SET is_blocked = 1
WHERE id IN (SELECT id FROM reachable WHERE kind = 'issue')
  AND status NOT IN ('closed', 'pinned');
