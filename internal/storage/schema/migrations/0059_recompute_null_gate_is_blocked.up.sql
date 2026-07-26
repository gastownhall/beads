-- Recompute issues.is_blocked with a NULL-safe waits-for gate predicate.
--
-- Migration 0047 (and runtime recompute before this release) evaluated
-- JSON_EXTRACT(d.metadata, '$.gate') = 'any-children' directly. Waits-for
-- dependencies created without gate metadata (e.g. 'bd dep add --type
-- waits-for' stores no metadata) yield NULL there, and NULL poisons the
-- enclosing NOT(... AND ...) so the waiter was computed unblocked as soon
-- as any child closed. COALESCE to the all-children default (matching
-- internal/storage/issueops/blocked_state.go) and re-run the recompute so
-- rows mis-set by 0047 are repaired. The wisps-side twin is
-- ignored/0015_recompute_null_gate_wisp_is_blocked.up.sql.
--
-- Why stand-in tables instead of a conditional recompute variant:
--
-- The recompute wants the clone-local wisps/wisp_dependencies tables when
-- they exist. Those are dolt-ignored and are NOT present during the
-- main-source migration pass on a freshly materialized (baseline/remote-backed)
-- clone — but issues/dependencies are dolt-versioned, so a fresh clone can
-- still carry rows mis-set by 0047 and the issues repair must not be skipped
-- there. And even when the tables exist, clone-local wisp tables created by a
-- pre-ignored/0003 binary can still carry the legacy depends_on_id shape
-- while the synced main cursor is current; the main-source pass runs before
-- the ignored pass, so referencing the post-split target columns there would
-- fail on an unknown column before ignored/0003 ever gets to split the table
-- (the hazard class 0047 guards with its column-shape check).
--
-- A PREPARE'd conditional recompute (the obvious answer, and 0047's pattern
-- for its DDL) is NOT safe for the writes here: the Dolt CLI batch path
-- (dolt sql -q/-f, the AllMigrationsSQL()/fresh-bundle route) silently fails
-- to apply prepared DML — a prepared UPDATE reports success and changes
-- nothing (observed on dolt 2.2.0; the same limitation class
-- cli_migrations.go documents for prepared ALTER TABLE). A dynamic recompute
-- would zero is_blocked and then silently never restore it on that path.
--
-- So: every write to a real table below is DIRECT SQL, and the recompute is
-- a single unconditional statement that reads fixed-shape stand-in copies of
-- the wisp tables. The only dynamic statements are the two best-effort
-- INSERT..SELECT copies into the stand-ins, guarded on the real tables
-- existing in post-split shape. Where prepared DML works (the runtime
-- migration path), the copies run and the recompute sees full wisp context;
-- where it does not, or the wisp tables are absent/legacy-shaped, the
-- stand-ins stay empty and the recompute degrades to issues/dependencies-only
-- semantics — the same compromise as a fresh clone, with ignored/0015 and the
-- runtime recompute repairing wisp-linked state once the clone-local chain
-- catches up. It must never wedge the pass or zero without restoring.

DROP TABLE IF EXISTS __bd_0059_recompute_wisps;
CREATE TABLE __bd_0059_recompute_wisps (
    id VARCHAR(255) NOT NULL,
    status VARCHAR(32),
    PRIMARY KEY (id)
);
DROP TABLE IF EXISTS __bd_0059_recompute_wisp_deps;
CREATE TABLE __bd_0059_recompute_wisp_deps (
    issue_id VARCHAR(255),
    depends_on_issue_id VARCHAR(255),
    depends_on_wisp_id VARCHAR(255),
    type VARCHAR(32),
    metadata JSON
);

SET @has_wisps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('wisps', 'wisp_dependencies')
);
SET @has_split_wisp_deps = (
    SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
      AND COLUMN_NAME IN ('depends_on_issue_id', 'depends_on_wisp_id')
);

SET @sql = IF(@has_wisps > 1 AND @has_split_wisp_deps > 1,
    'INSERT INTO __bd_0059_recompute_wisps (id, status) SELECT id, status FROM wisps',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @sql = IF(@has_wisps > 1 AND @has_split_wisp_deps > 1,
    'INSERT INTO __bd_0059_recompute_wisp_deps (issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata) SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata FROM wisp_dependencies',
    'SELECT 1');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Self-assign updated_at: is_blocked is derived state and issues.updated_at
-- carries ON UPDATE CURRENT_TIMESTAMP; letting the recompute bump it plants
-- per-clone wall clock in a synced table (see blocked_state.go, bd-578h9.19).
UPDATE issues SET is_blocked = 0, updated_at = updated_at;

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
          JOIN __bd_0059_recompute_wisps t ON t.id = d.depends_on_wisp_id
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
                FROM __bd_0059_recompute_wisp_deps cd
                JOIN __bd_0059_recompute_wisps child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
            )
            AND NOT (
              COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
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
                  FROM __bd_0059_recompute_wisp_deps cd
                  JOIN __bd_0059_recompute_wisps child ON child.id = cd.issue_id
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
    FROM __bd_0059_recompute_wisps w
    WHERE w.status NOT IN ('closed', 'pinned')
      AND (
        EXISTS (
          SELECT 1
          FROM __bd_0059_recompute_wisp_deps d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM __bd_0059_recompute_wisp_deps d
          JOIN __bd_0059_recompute_wisps t ON t.id = d.depends_on_wisp_id
          WHERE d.issue_id = w.id
            AND d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR EXISTS (
          SELECT 1
          FROM __bd_0059_recompute_wisp_deps d
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
                FROM __bd_0059_recompute_wisp_deps cd
                JOIN __bd_0059_recompute_wisps child ON child.id = cd.issue_id
                WHERE cd.type = 'parent-child'
                  AND (
                    (d.depends_on_issue_id IS NOT NULL AND cd.depends_on_issue_id = d.depends_on_issue_id)
                    OR (d.depends_on_wisp_id IS NOT NULL AND cd.depends_on_wisp_id = d.depends_on_wisp_id)
                  )
                  AND child.status NOT IN ('closed', 'pinned')
              )
            )
            AND NOT (
              COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
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
                  FROM __bd_0059_recompute_wisp_deps cd
                  JOIN __bd_0059_recompute_wisps child ON child.id = cd.issue_id
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
    JOIN __bd_0059_recompute_wisp_deps d
      ON d.type = 'parent-child'
     AND (
       (r.kind = 'issue' AND d.depends_on_issue_id = r.id)
       OR (r.kind = 'wisp' AND d.depends_on_wisp_id = r.id)
     )
    JOIN __bd_0059_recompute_wisps child ON child.id = d.issue_id
    WHERE child.status NOT IN ('closed', 'pinned')
  )
UPDATE issues
SET is_blocked = 1, updated_at = updated_at
WHERE id IN (SELECT id FROM reachable WHERE kind = 'issue')
  AND status NOT IN ('closed', 'pinned');

DROP TABLE __bd_0059_recompute_wisps;
DROP TABLE __bd_0059_recompute_wisp_deps;
